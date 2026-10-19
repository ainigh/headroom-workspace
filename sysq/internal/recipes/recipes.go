// Package recipes loads the recipe library (design §19): Starlark functions
// with a recipe(...) header. Declared metadata is limited to name, doc,
// params, tags, example and rule fields; cost, sensitivity, primitives used
// and output schema are derived, never declared (design §23.5).
package recipes

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/plan"
	"github.com/ainigh/headroom-workspace/sysq/internal/policy"
	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/sugar"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

// Param is one declared recipe parameter.
type Param struct {
	Name string    `json:"name"`
	Spec argv.Spec `json:"spec"`
}

// Rule is attention-rule metadata (design §19.4).
type Rule struct {
	Severity string `json:"severity"`
	When     string `json:"when,omitempty"`
	Message  string `json:"message,omitempty"`
	Schedule string `json:"schedule,omitempty"`
	Baseline string `json:"baseline,omitempty"`
}

// Recipe is one loaded recipe.
type Recipe struct {
	Name     string   `json:"name"`
	Doc      string   `json:"doc"`
	Params   []Param  `json:"params,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Example  string   `json:"example,omitempty"`
	Rule     *Rule    `json:"rule,omitempty"`
	File     string   `json:"file"`
	Proposed bool     `json:"proposed,omitempty"`

	// Derived by analysis (never declared).
	Cost        string   `json:"cost"`
	Sensitivity string   `json:"sensitivity"`
	Units       int      `json:"cost_units"`
	Primitives  []string `json:"primitives"`
	Uses        []string `json:"uses,omitempty"`
	Network     bool     `json:"network,omitempty"`
	Unavailable string   `json:"unavailable,omitempty"`

	Source  string `json:"-"`
	Lowered string `json:"-"`

	prog *starlark.Program
}

// Library is the loaded recipe set.
type Library struct {
	Catalog  *catalog.Catalog
	Policy   policy.Policy
	FS       fs.FS // files load() may read (merged view)
	recipes  map[string]*Recipe
	proposed map[string]*Recipe
	Errors   []string
	key      *int
}

// Loader options.
type Options struct {
	Catalog *catalog.Catalog
	Policy  policy.Policy
	// Roots are recipe directories; later roots override earlier ones by
	// recipe name. A "proposed" directory in any root is loaded for review only.
	Roots []fs.FS
}

var (
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	headerKeys = map[string]bool{"name": true, "doc": true, "params": true, "tags": true, "example": true,
		"severity": true, "when": true, "message": true, "schedule": true, "baseline": true}
	derivedKeys = map[string]bool{"cost": true, "sensitivity": true, "primitives": true, "schema": true, "output": true}
)

type file struct {
	root     fs.FS
	path     string
	src      string
	lowered  string
	name     string
	ast      *syntax.File
	proposed bool
}

// Load reads every *.star under each root. Files whose name starts with "_"
// and files under lib/ are helper modules for load(), not recipes; examples/
// is skipped. Errors are collected per file; good recipes still load.
func Load(o Options) (*Library, error) {
	lib := &Library{Catalog: o.Catalog, Policy: o.Policy, recipes: map[string]*Recipe{}, proposed: map[string]*Recipe{}, key: new(int)}
	lib.FS = overlay(o.Roots)
	var files []*file
	for _, root := range o.Roots {
		if root == nil {
			continue
		}
		_ = fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "examples" || d.Name() == "lib" || d.Name() == "testdata" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".star") || strings.HasPrefix(path.Base(p), "_") {
				return nil
			}
			data, err := fs.ReadFile(root, p)
			if err != nil {
				lib.Errors = append(lib.Errors, fmt.Sprintf("%s: %v", p, err))
				return nil
			}
			f := &file{root: root, path: p, src: string(data), proposed: strings.HasPrefix(p, "proposed/") || strings.Contains(p, "/proposed/")}
			if err := f.parse(); err != nil {
				lib.Errors = append(lib.Errors, fmt.Sprintf("%s: %v", p, err))
				return nil
			}
			files = append(files, f)
			return nil
		})
	}
	// Pass 1 found every header name; compile with the full predeclared set.
	var names []string
	for _, f := range files {
		if !f.proposed {
			names = append(names, f.name)
		}
	}
	predeclared := script.PredeclaredNames(o.Catalog, names)
	predeclared["recipe"], predeclared["param"] = true, true
	for _, f := range files {
		r, err := lib.compile(f, predeclared)
		if err != nil {
			lib.Errors = append(lib.Errors, fmt.Sprintf("%s: %v", f.path, err))
			continue
		}
		if r.Proposed {
			lib.proposed[r.Name] = r
			continue
		}
		if _, isPrim := o.Catalog.Get(r.Name); isPrim {
			lib.Errors = append(lib.Errors, fmt.Sprintf("%s: recipe %s has the name of a primitive", f.path, r.Name))
			continue
		}
		lib.recipes[r.Name] = r
	}
	lib.derive()
	sort.Strings(lib.Errors)
	if len(lib.Errors) > 0 {
		return lib, fmt.Errorf("recipes: %s", strings.Join(lib.Errors, "; "))
	}
	return lib, nil
}

func (f *file) parse() error {
	low, err := sugar.Lower(f.src, sugar.Star)
	if err != nil {
		return err
	}
	f.lowered = low
	ast, err := script.FileOptions.Parse(f.path, low, 0)
	if err != nil {
		return err
	}
	f.ast = ast
	for _, st := range ast.Stmts {
		es, ok := st.(*syntax.ExprStmt)
		if !ok {
			continue
		}
		call, ok := es.X.(*syntax.CallExpr)
		if !ok {
			continue
		}
		if id, ok := call.Fn.(*syntax.Ident); !ok || id.Name != "recipe" {
			continue
		}
		for _, a := range call.Args {
			be, ok := a.(*syntax.BinaryExpr)
			if !ok || be.Op != syntax.EQ {
				continue
			}
			k, _ := be.X.(*syntax.Ident)
			if k == nil || k.Name != "name" {
				continue
			}
			lit, ok := be.Y.(*syntax.Literal)
			if !ok || lit.Token != syntax.STRING {
				return fmt.Errorf("recipe name must be a string literal")
			}
			f.name = lit.Value.(string)
		}
	}
	if f.name == "" {
		return fmt.Errorf("no recipe(name=...) header")
	}
	if !nameRe.MatchString(f.name) {
		return fmt.Errorf("bad recipe name %q (group.name, lower case)", f.name)
	}
	return nil
}

// header captures what recipe(...) and param(...) declare during a load-time
// execution of the module.
type header struct {
	kw     map[string]starlark.Value
	params []Param
	order  []string
}

type paramDecl struct {
	spec argv.Spec
}

func (p *paramDecl) String() string        { return "param(" + p.spec.Type + ")" }
func (p *paramDecl) Type() string          { return "param" }
func (p *paramDecl) Freeze()               {}
func (p *paramDecl) Truth() starlark.Bool  { return true }
func (p *paramDecl) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable") }

func paramBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("param(type, default=..., ...)")
	}
	var typ string
	switch t := args[0].(type) {
	case *starlark.Builtin:
		typ = map[string]string{"int": argv.TInt, "float": argv.TFloat, "str": argv.TStr, "bool": argv.TBool,
			"Path": argv.TPath, "Bytes": argv.TBytes, "Duration": argv.TDuration}[t.Name()]
	case starlark.String:
		typ = string(t)
	}
	if typ == "" {
		return nil, fmt.Errorf("param: type must be int, float, str, bool, Path, Bytes or Duration")
	}
	spec := argv.Spec{Type: typ}
	for _, kv := range kwargs {
		k := string(kv[0].(starlark.String))
		v := kv[1]
		switch k {
		case "default":
			spec.Default = defaultValue(v)
		case "min", "max":
			f, ok := starlark.AsFloat(v)
			if !ok {
				switch x := v.(type) {
				case script.Bytes:
					f = float64(x)
				case script.Duration:
					f = float64(x)
				default:
					return nil, fmt.Errorf("param %s must be a number", k)
				}
			}
			if k == "min" {
				spec.Min = &f
			} else {
				spec.Max = &f
			}
		case "values":
			l, ok := v.(*starlark.List)
			if !ok {
				return nil, fmt.Errorf("param values must be a list of strings")
			}
			for i := 0; i < l.Len(); i++ {
				s, ok := starlark.AsString(l.Index(i))
				if !ok {
					return nil, fmt.Errorf("param values must be strings")
				}
				spec.Values = append(spec.Values, s)
			}
			if spec.Type == argv.TStr {
				spec.Type = argv.TEnum
			}
		case "doc":
			spec.Doc, _ = starlark.AsString(v)
		case "required":
			spec.Required = bool(v.Truth())
		case "pattern":
			spec.Pattern, _ = starlark.AsString(v)
		case "max_len":
			n, _ := starlark.AsInt32(v)
			spec.MaxLen = n
		case "must_exist":
			spec.MustExist = bool(v.Truth())
		default:
			return nil, fmt.Errorf("param: unknown option %s", k)
		}
	}
	if spec.Default == nil && !spec.Required && spec.Type != argv.TStr && spec.Type != argv.TPath {
		// Optional typed params default to unset (None in run()).
	}
	return &paramDecl{spec: spec}, nil
}

func defaultValue(v starlark.Value) any {
	switch x := v.(type) {
	case script.Bytes:
		return int64(x)
	case script.Duration:
		return float64(x)
	case script.Path:
		return string(x)
	case starlark.String:
		return string(x)
	case starlark.Int:
		n, _ := x.Int64()
		return n
	case starlark.Float:
		return float64(x)
	case starlark.Bool:
		return bool(x)
	case starlark.NoneType:
		return nil
	}
	return v.String()
}

func (lib *Library) compile(f *file, predeclared map[string]bool) (*Recipe, error) {
	_, prog, err := starlark.SourceProgramOptions(script.FileOptions, f.path, f.lowered, func(n string) bool { return predeclared[n] })
	if err != nil {
		return nil, err
	}
	// Execute once with primitives unavailable to capture the header.
	h := &header{}
	pre := starlark.StringDict{}
	for n := range predeclared {
		pre[n] = starlark.None
	}
	for _, n := range []string{"Path", "Bytes", "Duration"} {
		pre[n] = loadTimeCtor(n)
	}
	pre["param"] = starlark.NewBuiltin("param", paramBuiltin)
	pre["recipe"] = starlark.NewBuiltin("recipe", func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) > 0 {
			return nil, fmt.Errorf("recipe() takes keyword arguments")
		}
		if h.kw != nil {
			return nil, fmt.Errorf("recipe() called twice")
		}
		h.kw = map[string]starlark.Value{}
		for _, kv := range kwargs {
			k := string(kv[0].(starlark.String))
			if derivedKeys[k] {
				return nil, fmt.Errorf("recipes may not declare %s: it is derived by analysis (design §23.5)", k)
			}
			if !headerKeys[k] {
				return nil, fmt.Errorf("recipe(): unknown field %s (allowed: name, doc, params, tags, example, severity, when, message, schedule, baseline)", k)
			}
			h.kw[k] = kv[1]
		}
		return starlark.None, nil
	})
	thread := &starlark.Thread{Name: "load:" + f.path, Load: lib.loadTime(pre)}
	thread.SetMaxExecutionSteps(100000)
	globals, err := prog.Init(thread, pre)
	if err != nil {
		return nil, fmt.Errorf("loading: %v", err)
	}
	if h.kw == nil {
		return nil, fmt.Errorf("no recipe(...) header")
	}
	r := &Recipe{Name: f.name, File: f.path, Source: f.src, Lowered: f.lowered, Proposed: f.proposed, prog: prog}
	str := func(k string) string {
		if v, ok := h.kw[k]; ok {
			s, _ := starlark.AsString(v)
			return s
		}
		return ""
	}
	r.Doc = str("doc")
	r.Example = str("example")
	if r.Doc == "" {
		return nil, fmt.Errorf("recipe %s has no doc", r.Name)
	}
	if tv, ok := h.kw["tags"]; ok {
		l, ok := tv.(*starlark.List)
		if !ok {
			return nil, fmt.Errorf("tags must be a list")
		}
		for i := 0; i < l.Len(); i++ {
			s, _ := starlark.AsString(l.Index(i))
			r.Tags = append(r.Tags, s)
		}
	}
	if sev := str("severity"); sev != "" {
		switch sev {
		case "low", "medium", "high", "critical", "info":
		default:
			return nil, fmt.Errorf("severity must be info, low, medium, high or critical")
		}
		r.Rule = &Rule{Severity: sev, When: str("when"), Message: str("message"), Schedule: str("schedule"), Baseline: str("baseline")}
	} else if str("when") != "" || str("message") != "" {
		return nil, fmt.Errorf("when/message need severity")
	}
	if pv, ok := h.kw["params"]; ok {
		d, ok := pv.(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("params must be a dict of param(...)")
		}
		for _, it := range d.Items() {
			k, _ := starlark.AsString(it[0])
			pd, ok := it[1].(*paramDecl)
			if !ok {
				return nil, fmt.Errorf("params[%q] must be param(...)", k)
			}
			r.Params = append(r.Params, Param{Name: k, Spec: pd.spec})
		}
	}
	run, ok := globals["run"].(*starlark.Function)
	if !ok {
		return nil, fmt.Errorf("recipe %s has no def run(...)", r.Name)
	}
	if run.NumParams() != len(r.Params) {
		return nil, fmt.Errorf("run() takes %d parameters but the header declares %d", run.NumParams(), len(r.Params))
	}
	declared := map[string]bool{}
	for _, p := range r.Params {
		declared[p.Name] = true
	}
	for i := 0; i < run.NumParams(); i++ {
		n, _ := run.Param(i)
		if !declared[n] {
			return nil, fmt.Errorf("run() parameter %s is not declared in params", n)
		}
	}
	return r, nil
}

// loadTime resolves load() while capturing headers: helper modules execute
// with placeholder builtins (their functions are not called at load time).
func (lib *Library) loadTime(pre starlark.StringDict) func(*starlark.Thread, string) (starlark.StringDict, error) {
	cache := map[string]starlark.StringDict{}
	return func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
		m := path.Clean(strings.TrimPrefix(strings.TrimPrefix(module, "//"), "recipes/"))
		if strings.HasPrefix(m, "..") || path.IsAbs(m) || !strings.HasSuffix(m, ".star") {
			return nil, fmt.Errorf("load: %q is outside the recipe library", module)
		}
		if g, ok := cache[m]; ok {
			return g, nil
		}
		data, err := fs.ReadFile(lib.FS, m)
		if err != nil {
			return nil, fmt.Errorf("load: %s not found", m)
		}
		low, err := sugar.Lower(string(data), sugar.Star)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", m, err)
		}
		g, err := starlark.ExecFileOptions(script.FileOptions, thread, m, low, pre)
		if err != nil {
			return nil, err
		}
		cache[m] = g
		return g, nil
	}
}

func loadTimeCtor(name string) *starlark.Builtin {
	return starlark.NewBuiltin(name, func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s(value)", name)
		}
		switch name {
		case "Bytes":
			n, err := argv.ParseBytes(goStr(args[0]))
			return script.Bytes(n), err
		case "Duration":
			d, err := argv.ParseDuration(goStr(args[0]))
			return script.Duration(d), err
		}
		s, _ := starlark.AsString(args[0])
		return script.Path(s), nil
	})
}

func goStr(v starlark.Value) any {
	if s, ok := starlark.AsString(v); ok {
		return s
	}
	if f, ok := starlark.AsFloat(v); ok {
		return f
	}
	return v.String()
}

// derive computes cost, sensitivity, primitives and dependencies for every
// recipe, transitively, and rejects dependency cycles.
func (lib *Library) derive() {
	direct := map[string][]string{}
	plan.SetNamespaces(lib.Catalog.Namespaces())
	shallow := resolver{lib: lib, facts: map[string]plan.RecipeFacts{}}
	for _, r := range lib.all() {
		p, err := plan.Analyze(r.Lowered, shallow)
		if err != nil {
			r.Unavailable = "analysis: " + err.Error()
			continue
		}
		direct[r.Name] = p.Recipes
	}
	// cycle detection (DFS)
	state := map[string]int{}
	var order []string
	var cyc []string
	var visit func(n string, stack []string) bool
	visit = func(n string, stack []string) bool {
		switch state[n] {
		case 1:
			cyc = append(stack, n)
			return false
		case 2:
			return true
		}
		state[n] = 1
		for _, d := range direct[n] {
			if d == n {
				cyc = []string{n, n}
				return false
			}
			if _, ok := lib.recipes[d]; !ok {
				continue
			}
			if !visit(d, append(stack, n)) {
				return false
			}
		}
		state[n] = 2
		order = append(order, n)
		return true
	}
	names := lib.Names()
	for _, n := range names {
		if state[n] == 0 {
			cyc = nil
			if !visit(n, nil) {
				msg := "dependency cycle: " + strings.Join(cyc, " → ")
				lib.Errors = append(lib.Errors, msg)
				for _, c := range cyc {
					if r, ok := lib.recipes[c]; ok {
						r.Unavailable = msg
						delete(lib.recipes, c)
					}
				}
			}
		}
	}
	full := resolver{lib: lib, facts: map[string]plan.RecipeFacts{}}
	for _, n := range order {
		r, ok := lib.recipes[n]
		if !ok {
			continue
		}
		lib.analyze(r, full)
		full.facts[n] = plan.RecipeFacts{Cost: r.Cost, Sensitivity: r.Sensitivity, Primitives: r.Primitives, Units: r.Units, Network: r.Network}
	}
	for _, r := range lib.proposed {
		lib.analyze(r, full)
	}
}

func (lib *Library) analyze(r *Recipe, res resolver) {
	p, err := plan.Analyze(r.Lowered, res)
	if err != nil {
		r.Unavailable = "analysis: " + err.Error()
		return
	}
	r.Cost, r.Sensitivity, r.Units, r.Network = p.Cost, p.Sensitivity, p.Units, p.Network
	r.Primitives, r.Uses = p.Primitives, p.Recipes
	if len(p.Unknown) > 0 {
		r.Unavailable = "refers to unknown names: " + strings.Join(p.Unknown, ", ")
	}
	if !lib.Policy.SensitivityEnabled(r.Sensitivity) {
		r.Unavailable = "needs sensitivity " + r.Sensitivity + ", which is not enabled"
	}
}

type resolver struct {
	lib   *Library
	facts map[string]plan.RecipeFacts
}

func (r resolver) Primitive(n string) (*catalog.Primitive, bool) { return r.lib.Catalog.Get(n) }
func (r resolver) Recipe(n string) (plan.RecipeFacts, bool) {
	if _, ok := r.lib.recipes[n]; !ok {
		return plan.RecipeFacts{}, false
	}
	return r.facts[n], true
}

func (lib *Library) all() []*Recipe {
	var out []*Recipe
	for _, n := range lib.Names() {
		out = append(out, lib.recipes[n])
	}
	return out
}

// Names lists callable recipes, sorted.
func (lib *Library) Names() []string {
	out := make([]string, 0, len(lib.recipes))
	for n := range lib.recipes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Get returns a callable recipe.
func (lib *Library) Get(name string) (*Recipe, bool) {
	r, ok := lib.recipes[name]
	return r, ok
}

// Proposed returns a proposed (not callable) recipe.
func (lib *Library) Proposed(name string) (*Recipe, bool) {
	r, ok := lib.proposed[name]
	return r, ok
}

// ProposedNames lists proposals.
func (lib *Library) ProposedNames() []string {
	out := make([]string, 0, len(lib.proposed))
	for n := range lib.proposed {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// All returns callable recipes sorted by name.
func (lib *Library) All() []*Recipe { return lib.all() }

// Rules returns recipes that declare severity.
func (lib *Library) Rules() []*Recipe {
	var out []*Recipe
	for _, r := range lib.all() {
		if r.Rule != nil {
			out = append(out, r)
		}
	}
	return out
}

// ParamOrder implements script.Recipes.
func (lib *Library) ParamOrder(name string) []string {
	r, ok := lib.recipes[name]
	if !ok {
		return nil
	}
	out := make([]string, len(r.Params))
	for i, p := range r.Params {
		out[i] = p.Name
	}
	return out
}

// Specs returns a recipe's param specs.
func (r *Recipe) Specs() map[string]argv.Spec {
	m := map[string]argv.Spec{}
	for _, p := range r.Params {
		m[p.Name] = p.Spec
	}
	return m
}

// Search finds recipes by name, doc or tag.
func (lib *Library) Search(q string) *table.Table {
	q = strings.ToLower(q)
	t := table.New([]table.Col{{Name: "name", Type: table.Str}, {Name: "doc", Type: table.Str}, {Name: "tags", Type: table.Str}, {Name: "cost", Type: table.Str}, {Name: "sensitivity", Type: table.Str}})
	for _, r := range lib.all() {
		hay := strings.ToLower(r.Name + " " + r.Doc + " " + strings.Join(r.Tags, " "))
		if strings.Contains(hay, q) {
			t.Append(r.Name, r.Doc, strings.Join(r.Tags, ","), r.Cost, r.Sensitivity)
		}
	}
	return t
}

// ---- running ---------------------------------------------------------------------

type modKey struct {
	lib  *int
	file string
}

// Call implements script.Recipes: validate params, then run the recipe body
// in the caller's runtime (same ledger, fuel and caps).
func (lib *Library) Call(rt *script.Runtime, name string, raw map[string]any) (starlark.Value, error) {
	r, ok := lib.recipes[name]
	if !ok {
		if _, proposed := lib.proposed[name]; proposed {
			return nil, fmt.Errorf("%s is a proposed recipe; a human must promote it before it can run", name)
		}
		return nil, fmt.Errorf("unknown recipe %q", name)
	}
	if r.Unavailable != "" {
		return nil, fmt.Errorf("%s is unavailable: %s", name, r.Unavailable)
	}
	if err := rt.Engine.Allowed(r.Sensitivity, rt.CallOpts); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	vals, err := argv.Validate(r.Specs(), raw, rt.Engine.PathPolicy())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	fn, err := lib.function(rt, r)
	if err != nil {
		return nil, err
	}
	var kwargs []starlark.Tuple
	for _, p := range r.Params {
		kwargs = append(kwargs, starlark.Tuple{starlark.String(p.Name), toParam(p.Spec, vals[p.Name])})
	}
	v, err := starlark.Call(rt.Thread(), fn, nil, kwargs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

var initMu sync.Mutex

func (lib *Library) function(rt *script.Runtime, r *Recipe) (*starlark.Function, error) {
	k := modKey{lib.key, r.File}
	if g, ok := rt.Ext(k).(starlark.StringDict); ok {
		return g["run"].(*starlark.Function), nil
	}
	pre := starlark.StringDict{}
	for kk, v := range rt.Predeclared() {
		pre[kk] = v
	}
	noop := starlark.NewBuiltin("noop", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		return starlark.None, nil
	})
	pre["recipe"], pre["param"] = noop, noop
	initMu.Lock()
	g, err := r.prog.Init(rt.Thread(), pre)
	initMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("%s: %v", r.Name, err)
	}
	rt.SetExt(k, g)
	fn, ok := g["run"].(*starlark.Function)
	if !ok {
		return nil, fmt.Errorf("%s: no run()", r.Name)
	}
	return fn, nil
}

func toParam(spec argv.Spec, v argv.Value) starlark.Value {
	if !v.Set {
		return starlark.None
	}
	switch spec.Type {
	case argv.TPath:
		return script.Path(v.Typed.(string))
	case argv.TBytes:
		return script.Bytes(v.Typed.(int64))
	case argv.TDuration:
		return script.Duration(v.Typed.(float64))
	case argv.TInt, argv.TPID:
		return starlark.MakeInt64(v.Typed.(int64))
	case argv.TFloat:
		return starlark.Float(v.Typed.(float64))
	case argv.TBool:
		return starlark.Bool(v.Typed.(bool))
	}
	return starlark.String(v.Text)
}

// overlay merges roots for load(): later roots win.
func overlay(roots []fs.FS) fs.FS { return overlayFS(roots) }

type overlayFS []fs.FS

func (o overlayFS) Open(name string) (fs.File, error) {
	for i := len(o) - 1; i >= 0; i-- {
		if o[i] == nil {
			continue
		}
		f, err := o[i].Open(name)
		if err == nil {
			return f, nil
		}
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// Quote is a helper for generated sources.
func Quote(s string) string { return strconv.Quote(s) }
