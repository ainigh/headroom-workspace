package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/expr"
	"github.com/ainigh/headroom-workspace/sysq/internal/sugar"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// FileOptions are the Starlark dialect sysq runs: top-level if/for for
// scripts, no while, no recursion (design §14.1).
var FileOptions = &syntax.FileOptions{TopLevelControl: true, GlobalReassign: true, Set: true}

// Recipes is how the runtime reaches the recipe library (implemented by the
// recipes package; nil when only primitives are available).
type Recipes interface {
	// Names lists callable recipe names.
	Names() []string
	// ParamOrder returns a recipe's parameter names in declaration order.
	ParamOrder(name string) []string
	// Call runs a recipe with params inside the caller's runtime.
	Call(rt *Runtime, name string, params map[string]any) (starlark.Value, error)
}

// HistoryFunc serves history(recipe, range) from sysqd's store.
type HistoryFunc func(ctx context.Context, recipe, column string, rangeSec float64) (*table.Table, error)

// Options configure one run.
type Options struct {
	Engine   *engine.Engine
	CallOpts engine.CallOpts
	Steps    uint64        // Starlark step budget (0 = policy default)
	Timeout  time.Duration // wall clock (0 = 60s)
	Recipes  Recipes
	History  HistoryFunc
	// RecipesFS is the only place load() may read from.
	RecipesFS fs.FS
	// SnapshotDir stores snapshot() tables (empty = snapshots unavailable).
	SnapshotDir string
	// MaxEmits caps emitted items.
	MaxEmits int
}

// Item is one emitted output.
type Item struct {
	Label string       `json:"label,omitempty"`
	Table *table.Table `json:"table"`
}

// Result is what a run produced. On error, Items holds the partial emits.
type Result struct {
	Items    []Item        `json:"items"`
	Lowered  string        `json:"lowered,omitempty"`
	Steps    uint64        `json:"steps"`
	Cost     int           `json:"cost"`
	Calls    int           `json:"calls"`
	Source   []string      `json:"source,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"-"`
}

// Runtime is the per-run state shared by builtins and table values.
type Runtime struct {
	Options
	ctx    context.Context
	thread *starlark.Thread
	items  []Item
	loads  map[string]*loadEntry
	depth  int
	pre    starlark.StringDict
	ext    map[any]any
}

// Ext returns per-run state stored by extensions (the recipe library).
func (rt *Runtime) Ext(key any) any { return rt.ext[key] }

// SetExt stores per-run extension state.
func (rt *Runtime) SetExt(key, v any) {
	if rt.ext == nil {
		rt.ext = map[any]any{}
	}
	rt.ext[key] = v
}

// Attention is implemented by recipe libraries that have rule recipes.
type Attention interface {
	Attention(rt *Runtime, severity string) (*table.Table, error)
}

type loadEntry struct {
	globals starlark.StringDict
	err     error
}

// Errors that try() never swallows.
var errStop = errors.New("run stopped")

// NewRuntime prepares a runtime (exported so recipes can run bodies in it).
func NewRuntime(ctx context.Context, o Options) *Runtime {
	if o.CallOpts.Ledger == nil {
		o.CallOpts.Ledger = &engine.Ledger{Budget: o.Engine.Policy.CostBudget}
	}
	if o.Steps == 0 {
		o.Steps = o.Engine.Policy.StepBudget
	}
	if o.MaxEmits == 0 {
		o.MaxEmits = 64
	}
	rt := &Runtime{Options: o, ctx: ctx, loads: map[string]*loadEntry{}}
	return rt
}

// Context returns the run's context.
func (rt *Runtime) Context() context.Context { return rt.ctx }

// Thread returns (creating if needed) the run's Starlark thread.
func (rt *Runtime) Thread() *starlark.Thread {
	if rt.thread == nil {
		rt.thread = &starlark.Thread{Name: "sysq", Load: rt.load}
		rt.thread.SetMaxExecutionSteps(rt.Steps)
		rt.thread.SetLocal("sysq.runtime", rt)
	}
	return rt.thread
}

func (rt *Runtime) tick() error {
	if err := rt.ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", errStop, err)
	}
	return nil
}

func (rt *Runtime) exprEnv() *expr.Env {
	return &expr.Env{Now: rt.now(), Home: rt.Engine.Home}
}

func (rt *Runtime) now() time.Time {
	if rt.Engine.Now != nil {
		return rt.Engine.Now()
	}
	return time.Now()
}

func (rt *Runtime) callPrimitive(name string, params map[string]any) (*table.Table, error) {
	if err := rt.tick(); err != nil {
		return nil, err
	}
	conv := map[string]any{}
	for k, v := range params {
		conv[k] = v
	}
	return rt.Engine.Primitive(rt.ctx, name, conv, rt.CallOpts)
}

func (rt *Runtime) callRecipe(name string, params map[string]any) (starlark.Value, error) {
	if rt.Recipes == nil {
		return nil, fmt.Errorf("no recipe library loaded")
	}
	if rt.depth > 8 {
		return nil, fmt.Errorf("recipes nested too deeply")
	}
	rt.depth++
	defer func() { rt.depth-- }()
	return rt.Recipes.Call(rt, name, params)
}

func (rt *Runtime) paramOrder(s *source) []string {
	if s.kind == "primitive" && s.prim != nil {
		return s.prim.ParamOrder
	}
	if s.kind == "recipe" && rt.Recipes != nil {
		return rt.Recipes.ParamOrder(s.name)
	}
	return nil
}

// ---- namespaces ------------------------------------------------------------------

// Namespace exposes primitives and recipes as dotted names (proc.top).
type Namespace struct {
	name    string
	entries map[string]starlark.Value
	call    func(*starlark.Thread, starlark.Tuple, []starlark.Tuple) (starlark.Value, error)
}

func (n *Namespace) String() string        { return "<namespace " + n.name + ">" }
func (n *Namespace) Type() string          { return "namespace" }
func (n *Namespace) Freeze()               {}
func (n *Namespace) Truth() starlark.Bool  { return true }
func (n *Namespace) Hash() (uint32, error) { return starlark.String(n.name).Hash() }
func (n *Namespace) Name() string          { return n.name }
func (n *Namespace) Attr(name string) (starlark.Value, error) {
	if v, ok := n.entries[name]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("%s has no %q (see describe())", n.name, name)
}
func (n *Namespace) AttrNames() []string {
	out := make([]string, 0, len(n.entries))
	for k := range n.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CallInternal lets history be both a function and a namespace.
func (n *Namespace) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if n.call == nil {
		return nil, fmt.Errorf("%s is a namespace, not a function", n.name)
	}
	return n.call(thread, args, kwargs)
}

func (rt *Runtime) insert(root map[string]*Namespace, full string, v starlark.Value) {
	parts := strings.Split(full, ".")
	ns, ok := root[parts[0]]
	if !ok {
		ns = &Namespace{name: parts[0], entries: map[string]starlark.Value{}}
		root[parts[0]] = ns
	}
	for i := 1; i < len(parts)-1; i++ {
		child, ok := ns.entries[parts[i]].(*Namespace)
		if !ok {
			child = &Namespace{name: strings.Join(parts[:i+1], "."), entries: map[string]starlark.Value{}}
			ns.entries[parts[i]] = child
		}
		ns = child
	}
	if _, exists := ns.entries[parts[len(parts)-1]]; !exists {
		ns.entries[parts[len(parts)-1]] = v
	}
}

// Predeclared returns the builtins visible to scripts and recipes (built
// once per run).
func (rt *Runtime) Predeclared() starlark.StringDict {
	if rt.pre != nil {
		return rt.pre
	}
	rt.pre = rt.predeclared()
	return rt.pre
}

// PredeclaredNames lists builtin names for a catalog and recipe set without a
// run (the recipe compiler needs them).
func PredeclaredNames(cat interface{ Namespaces() []string }, recipeNames []string) map[string]bool {
	out := map[string]bool{"recipes": true, "history": true, "attention": true}
	for _, n := range builtinNames {
		out[n] = true
	}
	for _, n := range cat.Namespaces() {
		out[n] = true
	}
	for _, n := range recipeNames {
		out[strings.SplitN(n, ".", 2)[0]] = true
	}
	return out
}

var builtinNames = []string{"emit", "expr", "now", "ago", "humanize", "try_", "fallback", "scalar", "num", "count",
	"any", "all", "assert_", "snapshot", "watch", "table", "record", "Duration", "Bytes", "Path"}

func (rt *Runtime) predeclared() starlark.StringDict {
	d := starlark.StringDict{
		"emit":     starlark.NewBuiltin("emit", rt.emit),
		"expr":     starlark.NewBuiltin("expr", exprBuiltin),
		"now":      starlark.NewBuiltin("now", rt.nowBuiltin),
		"ago":      starlark.NewBuiltin("ago", rt.agoBuiltin),
		"humanize": starlark.NewBuiltin("humanize", humanize),
		"try_":     starlark.NewBuiltin("try_", tryBuiltin),
		"fallback": starlark.NewBuiltin("fallback", fallback),
		"scalar":   starlark.NewBuiltin("scalar", scalarBuiltin),
		"num":      starlark.NewBuiltin("num", numBuiltin),
		"count":    starlark.NewBuiltin("count", countBuiltin),
		"any":      starlark.NewBuiltin("any", rt.anyAll),
		"all":      starlark.NewBuiltin("all", rt.anyAll),
		"assert_":  starlark.NewBuiltin("assert_", assertBuiltin),
		"snapshot": starlark.NewBuiltin("snapshot", rt.snapshot),
		"watch":    starlark.NewBuiltin("watch", rt.watch),
		"table":    starlark.NewBuiltin("table", rt.tableBuiltin),
		"record":   starlark.NewBuiltin("record", rt.recordBuiltin),
		"Duration": starlark.NewBuiltin("Duration", durationCtor),
		"Bytes":    starlark.NewBuiltin("Bytes", bytesCtor),
		"Path":     starlark.NewBuiltin("Path", pathCtor),
	}
	root := map[string]*Namespace{}
	for _, p := range rt.Engine.Catalog.All() {
		rt.insert(root, p.Name, &Table{rt: rt, src: &source{kind: "primitive", name: p.Name, prim: p, ref: true}})
	}
	if rt.Recipes != nil {
		recipesNS := map[string]*Namespace{}
		for _, n := range rt.Recipes.Names() {
			ref := &Table{rt: rt, src: &source{kind: "recipe", name: n, ref: true}}
			rt.insert(recipesNS, n, ref)
			rt.insert(root, n, ref) // bare names too, when no primitive has them
		}
		top := &Namespace{name: "recipes", entries: map[string]starlark.Value{}}
		for k, v := range recipesNS {
			top.entries[k] = v
		}
		top.entries["search"] = starlark.NewBuiltin("recipes.search", rt.searchRecipes)
		d["recipes"] = top
	}
	if att, ok := rt.Recipes.(Attention); ok && rt.Recipes != nil {
		d["attention"] = starlark.NewBuiltin("attention", func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			sev := ""
			if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "severity?", &sev); err != nil {
				return nil, err
			}
			t, err := att.Attention(rt, sev)
			if err != nil {
				return nil, err
			}
			return rt.wrap(t), nil
		})
	}
	if rt.History != nil {
		h, ok := root["history"]
		if !ok {
			h = &Namespace{name: "history", entries: map[string]starlark.Value{}}
			root["history"] = h
		}
		h.call = rt.historyCall
	}
	for k, v := range root {
		if _, taken := d[k]; !taken {
			d[k] = v
		}
	}
	return d
}

// ---- builtins --------------------------------------------------------------------

func (rt *Runtime) emit(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	label := ""
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) != "label" {
			return nil, fmt.Errorf("emit: unexpected keyword %s", kv[0])
		}
		s, ok := starlark.AsString(kv[1])
		if !ok {
			return nil, fmt.Errorf("emit: label must be a string")
		}
		label = s
	}
	for _, a := range args {
		items, err := rt.toItems(label, a)
		if err != nil {
			return nil, fmt.Errorf("emit: %w", err)
		}
		for _, it := range items {
			if len(rt.items) >= rt.MaxEmits {
				return nil, fmt.Errorf("emit: more than %d outputs", rt.MaxEmits)
			}
			engine.ApplyCaps(it.Table, rt.Engine.Policy.Caps)
			rt.items = append(rt.items, it)
		}
	}
	return starlark.None, nil
}

// ToItems converts a script value into emitted items (also used for recipe
// return values): a table; a scalar (1×1); a record whose values are all
// tables (one item per key); any other record (a one-row table).
func (rt *Runtime) toItems(label string, v starlark.Value) ([]Item, error) {
	switch x := v.(type) {
	case *Table:
		t, err := x.Materialize()
		if err != nil {
			return nil, err
		}
		return []Item{{Label: label, Table: t}}, nil
	case *starlark.Dict:
		allTables := x.Len() > 0
		for _, it := range x.Items() {
			if _, ok := it[1].(*Table); !ok {
				allTables = false
			}
		}
		if allTables {
			var out []Item
			for _, it := range x.Items() {
				k, _ := starlark.AsString(it[0])
				l := k
				if label != "" {
					l = label + "." + k
				}
				t, err := it[1].(*Table).Materialize()
				if err != nil {
					return nil, err
				}
				out = append(out, Item{Label: l, Table: t})
			}
			return out, nil
		}
		t, err := rt.recordTable([]*starlark.Dict{x})
		if err != nil {
			return nil, err
		}
		return []Item{{Label: label, Table: t}}, nil
	case *starlark.List:
		var dicts []*starlark.Dict
		for i := 0; i < x.Len(); i++ {
			d, ok := x.Index(i).(*starlark.Dict)
			if !ok {
				dicts = nil
				break
			}
			dicts = append(dicts, d)
		}
		if dicts != nil {
			t, err := rt.recordTable(dicts)
			if err != nil {
				return nil, err
			}
			return []Item{{Label: label, Table: t}}, nil
		}
		col, vals := table.Col{Name: "value", Type: table.Str}, make([]any, x.Len())
		for i := 0; i < x.Len(); i++ {
			c, gv := cellOf(x.Index(i))
			if i == 0 {
				col.Type = c.Type
			}
			vals[i] = gv
		}
		t := table.New([]table.Col{col})
		for _, v := range vals {
			t.Append(table.Coerce(col.Type, v))
		}
		return []Item{{Label: label, Table: t}}, nil
	case *Row:
		t := x.t.WithRows([][]any{x.row})
		return []Item{{Label: label, Table: t}}, nil
	case starlark.NoneType:
		return nil, nil
	}
	c, gv := cellOf(v)
	c.Name = "value"
	if _, ok := v.(starlark.String); ok {
		c.Name = "message"
	}
	return []Item{{Label: label, Table: table.Scalar(c.Name, c.Type, gv)}}, nil
}

// cellOf maps a Starlark value to a column type and cell.
func cellOf(v starlark.Value) (table.Col, any) {
	switch x := v.(type) {
	case starlark.NoneType:
		return table.Col{Type: table.Str}, nil
	case starlark.Bool:
		return table.Col{Type: table.Bool}, bool(x)
	case starlark.Int:
		n, _ := x.Int64()
		return table.Col{Type: table.Int}, n
	case starlark.Float:
		return table.Col{Type: table.Float}, float64(x)
	case starlark.String:
		return table.Col{Type: table.Str}, string(x)
	case Bytes:
		return table.Col{Type: table.Bytes}, int64(x)
	case Duration:
		return table.Col{Type: table.Duration}, float64(x)
	case Path:
		return table.Col{Type: table.Path}, string(x)
	case Timestamp:
		return table.Col{Type: table.TS}, x.T
	case *Table:
		t := x.soft()
		if t.Len() == 1 && len(t.Schema) == 1 {
			c := t.Schema[0]
			c.Name = ""
			return c, t.Rows[0][0]
		}
		return table.Col{Type: table.TableT, Schema: t.Schema}, t
	case *Row:
		return table.Col{Type: table.TableT, Schema: x.t.Schema}, x.t.WithRows([][]any{x.row})
	}
	b, _ := json.Marshal(goValue(v))
	var j any
	_ = json.Unmarshal(b, &j)
	return table.Col{Type: table.JSON}, j
}

func (rt *Runtime) recordTable(dicts []*starlark.Dict) (*table.Table, error) {
	var schema []table.Col
	index := map[string]int{}
	var rows [][]any
	for _, d := range dicts {
		row := make([]any, len(schema))
		for _, it := range d.Items() {
			k, ok := starlark.AsString(it[0])
			if !ok {
				return nil, fmt.Errorf("record keys must be strings")
			}
			c, v := cellOf(it[1])
			i, seen := index[k]
			if !seen {
				c.Name = k
				schema = append(schema, c)
				i = len(schema) - 1
				index[k] = i
				row = append(row, nil)
				for r := range rows {
					rows[r] = append(rows[r], nil)
				}
			} else if schema[i].Type == table.Str && v != nil && c.Type != table.Str && rows != nil && allNil(rows, i) {
				schema[i].Type = c.Type
			}
			row[i] = v
		}
		rows = append(rows, row)
	}
	t := table.New(schema)
	for _, r := range rows {
		for len(r) < len(schema) {
			r = append(r, nil)
		}
		t.Append(r...)
	}
	return t, nil
}

func allNil(rows [][]any, i int) bool {
	for _, r := range rows {
		if i < len(r) && r[i] != nil {
			return false
		}
	}
	return true
}

func (rt *Runtime) tableBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var rows *starlark.List
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "rows", &rows); err != nil {
		return nil, err
	}
	var dicts []*starlark.Dict
	for i := 0; i < rows.Len(); i++ {
		d, ok := rows.Index(i).(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("table: rows must be dicts")
		}
		dicts = append(dicts, d)
	}
	t, err := rt.recordTable(dicts)
	if err != nil {
		return nil, err
	}
	return rt.wrap(t), nil
}

func (rt *Runtime) recordBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("record takes only keyword arguments")
	}
	d := starlark.NewDict(len(kwargs))
	for _, kv := range kwargs {
		_ = d.SetKey(kv[0], kv[1])
	}
	t, err := rt.recordTable([]*starlark.Dict{d})
	if err != nil {
		return nil, err
	}
	var srcs []*table.Table
	for _, kv := range kwargs {
		if tv, ok := kv[1].(*Table); ok {
			srcs = append(srcs, tv.soft())
		}
	}
	t.MergeMeta(srcs...)
	t.Meta.Error = ""
	return rt.wrap(t), nil
}

func exprBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("expr(text, name=value...)")
	}
	s, ok := starlark.AsString(args[0])
	if !ok {
		return nil, fmt.Errorf("expr: text must be a string")
	}
	if len(s) > 4096 {
		return nil, fmt.Errorf("expr: longer than 4096 characters")
	}
	e := &Expr{src: s, bindings: map[string]starlark.Value{}}
	for _, kv := range kwargs {
		e.bindings[string(kv[0].(starlark.String))] = kv[1]
	}
	return e, nil
}

func (rt *Runtime) nowBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return Timestamp{rt.now()}, starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 0)
}

func (rt *Runtime) agoBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var d starlark.Value
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &d); err != nil {
		return nil, err
	}
	dur, ok := d.(Duration)
	if !ok {
		return nil, fmt.Errorf("ago: need a duration like 2h")
	}
	return Timestamp{rt.now()}.Add(-dur), nil
}

func humanize(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	c, gv := cellOf(v)
	return starlark.String(table.Humanize(c, gv)), nil
}

func tryBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var f starlark.Callable
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &f); err != nil {
		return nil, err
	}
	v, err := starlark.Call(thread, f, nil, nil)
	if err != nil {
		if stopping(thread, err) {
			return nil, err
		}
		rt, _ := thread.Local("sysq.runtime").(*Runtime)
		t := table.New(nil)
		t.Meta.Error = cleanErr(err)
		if rt != nil {
			return rt.wrap(t), nil
		}
		return newTable(t), nil
	}
	if tv, ok := v.(*Table); ok {
		// Materialize now so a failure becomes a value here, not later.
		if _, err := tv.Materialize(); err != nil {
			if stopping(thread, err) {
				return nil, err
			}
			t := table.New(nil)
			t.Meta.Error = cleanErr(err)
			return &Table{rt: tv.rt, t: t}, nil
		}
	}
	return v, nil
}

// stopping: fuel, budget and cancellation are never caught by try().
func stopping(thread *starlark.Thread, err error) bool {
	if errors.Is(err, engine.ErrBudget) || errors.Is(err, errStop) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "too many steps") || strings.Contains(s, "cancelled") || strings.Contains(s, engine.ErrBudget.Error())
}

func cleanErr(err error) string {
	var ee *starlark.EvalError
	if errors.As(err, &ee) {
		return ee.Msg
	}
	return err.Error()
}

func fallback(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var first starlark.Value
	var other starlark.Callable
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 2, &first, &other); err != nil {
		return nil, err
	}
	if count := lenOf(first); count > 0 {
		return first, nil
	}
	return starlark.Call(thread, other, nil, nil)
}

func lenOf(v starlark.Value) int {
	switch x := v.(type) {
	case *Table:
		return x.Len()
	case starlark.NoneType:
		return 0
	case starlark.Sequence:
		return x.Len()
	}
	if v.Truth() {
		return 1
	}
	return 0
}

func scalarBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	t, ok := v.(*Table)
	if !ok {
		return v, nil
	}
	r, err := t.Materialize()
	if err != nil {
		return nil, err
	}
	switch {
	case r.Len() == 0:
		return starlark.None, nil
	case len(r.Schema) == 1:
		return toStarlark(r.Schema[0], r.Rows[0][0]), nil
	}
	return nil, fmt.Errorf("scalar: table has %d columns; pick one (t.col)", len(r.Schema))
}

// num(x) turns Bytes, Duration, a 1×1 table or a number into a float
// (None stays None), for arithmetic and comparisons with plain numbers.
func numBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	if t, ok := v.(*Table); ok {
		s, err := scalarBuiltin(thread, fn, starlark.Tuple{t}, nil)
		if err != nil {
			return nil, err
		}
		v = s
	}
	switch x := v.(type) {
	case starlark.NoneType:
		return starlark.None, nil
	case Bytes:
		return starlark.Float(x), nil
	case Duration:
		return starlark.Float(x), nil
	case starlark.Int, starlark.Float:
		f, _ := starlark.AsFloat(x)
		return starlark.Float(f), nil
	case starlark.Bool:
		if x {
			return starlark.Float(1), nil
		}
		return starlark.Float(0), nil
	case starlark.String:
		return nil, fmt.Errorf("num: %q is not a number", string(x))
	}
	return nil, fmt.Errorf("num: cannot convert %s", v.Type())
}

func countBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	return starlark.MakeInt(lenOf(v)), nil
}

// any(t, pred) / all(t, pred); with one argument, Starlark's meaning.
func (rt *Runtime) anyAll(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	isAny := fn.Name() == "any"
	if len(args) == 2 {
		t, ok := args[0].(*Table)
		if !ok {
			return nil, fmt.Errorf("%s(table, predicate)", fn.Name())
		}
		r, err := t.Materialize()
		if err != nil {
			return nil, err
		}
		var pred expr.Node
		var b map[string]any
		switch p := args[1].(type) {
		case *Expr:
			pred, b, err = p.node(rt)
		case starlark.String:
			pred, err = expr.Parse(string(p))
		default:
			return nil, fmt.Errorf("%s: predicate must be expr(...)", fn.Name())
		}
		if err != nil {
			return nil, err
		}
		env := rt.exprEnv()
		env.Bindings = b
		for _, row := range r.Rows {
			e := *env
			e.Table, e.Row = r, row
			v, err := expr.Eval(pred, &e)
			if err != nil {
				return nil, err
			}
			if table.Truthy(v.V) == isAny {
				return starlark.Bool(isAny), nil
			}
		}
		return starlark.Bool(!isAny), nil
	}
	var it starlark.Iterable
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &it); err != nil {
		return nil, err
	}
	iter := it.Iterate()
	defer iter.Done()
	var x starlark.Value
	for iter.Next(&x) {
		if bool(x.Truth()) == isAny {
			return starlark.Bool(isAny), nil
		}
	}
	return starlark.Bool(!isAny), nil
}

func assertBuiltin(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var cond starlark.Value
	msg := "assertion failed"
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &cond, &msg); err != nil {
		return nil, err
	}
	if !cond.Truth() {
		return nil, fmt.Errorf("%s", msg)
	}
	return starlark.None, nil
}

var snapName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// snapshot(name) loads a saved table; snapshot(name, table) saves one.
// Snapshots are sysq's own state, written under its data directory.
func (rt *Runtime) snapshot(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	var tv starlark.Value = starlark.None
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &name, &tv); err != nil {
		return nil, err
	}
	if !snapName.MatchString(name) {
		return nil, fmt.Errorf("snapshot: bad name %q", name)
	}
	if rt.SnapshotDir == "" {
		return nil, fmt.Errorf("snapshot: unavailable in this mode")
	}
	file := filepath.Join(rt.SnapshotDir, name+".json")
	if tv == starlark.None {
		data, err := os.ReadFile(file)
		if err != nil {
			t := table.New(nil)
			t.Meta.Error = "no snapshot named " + name
			return rt.wrap(t), nil
		}
		var t table.Table
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("snapshot %s: %v", name, err)
		}
		return rt.wrap(&t), nil
	}
	t, ok := tv.(*Table)
	if !ok {
		return nil, fmt.Errorf("snapshot: second argument must be a table")
	}
	r, err := t.Materialize()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(rt.SnapshotDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return nil, err
	}
	return t, nil
}

// watch(target, every=2s, for_=30s) samples a primitive or recipe repeatedly
// (bounded: ≥1s apart, ≤60 samples, ≤2 minutes) into one table with a t column.
func (rt *Runtime) watch(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var target starlark.Value
	every, forDur := Duration(2), Duration(20)
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "target", &target, "every?", &every, "for_?", &forDur); err != nil {
		return nil, err
	}
	tv, ok := target.(*Table)
	if !ok || tv.src == nil {
		return nil, fmt.Errorf("watch: target must be a primitive or recipe, e.g. watch(sys.memory)")
	}
	if every < 1 {
		return nil, fmt.Errorf("watch: every must be at least 1s")
	}
	if forDur > 120 {
		return nil, fmt.Errorf("watch: for_ is at most 2m")
	}
	n := int(float64(forDur)/float64(every)) + 1
	if n > 60 {
		return nil, fmt.Errorf("watch: at most 60 samples")
	}
	var out *table.Table
	start := rt.now()
	for i := 0; i < n; i++ {
		if i > 0 && rt.Engine.Fixtures == nil {
			select {
			case <-rt.ctx.Done():
				return nil, fmt.Errorf("%w: %v", errStop, rt.ctx.Err())
			case <-time.After(time.Duration(float64(every) * float64(time.Second))):
			}
		}
		sample := &Table{rt: rt, src: &source{kind: tv.src.kind, name: tv.src.name, prim: tv.src.prim, params: copyParams(tv.src.params)}}
		saved := rt.CallOpts.Fresh
		rt.CallOpts.Fresh = true
		r, err := sample.Materialize()
		rt.CallOpts.Fresh = saved
		if err != nil {
			return nil, err
		}
		ts := start.Add(time.Duration(float64(i) * float64(every) * float64(time.Second)))
		if rt.Engine.Fixtures == nil {
			ts = rt.now()
		}
		schema := append([]table.Col{{Name: "t", Type: table.TS}}, r.Schema...)
		if out == nil {
			out = table.New(schema)
			out.Meta = r.Meta
		}
		for _, row := range r.Rows {
			out.Rows = append(out.Rows, append([]any{ts}, row...))
		}
		out.MergeMeta(r)
	}
	out.Fix()
	out.Meta.Cached = false
	return rt.wrap(out), nil
}

func (rt *Runtime) historyCall(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var recipe string
	column := ""
	rng := Duration(3600)
	if err := starlark.UnpackArgs("history", args, kwargs, "recipe", &recipe, "range?", &rng, "column?", &column); err != nil {
		return nil, err
	}
	if err := rt.CallOpts.Ledger.Charge("history", "cheap"); err != nil {
		return nil, err
	}
	t, err := rt.History(rt.ctx, recipe, column, float64(rng))
	if err != nil {
		return nil, err
	}
	return rt.wrap(t), nil
}

func (rt *Runtime) searchRecipes(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var q string
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &q); err != nil {
		return nil, err
	}
	searcher, ok := rt.Recipes.(interface {
		Search(q string) *table.Table
	})
	if !ok {
		return nil, fmt.Errorf("search unavailable")
	}
	return rt.wrap(searcher.Search(q)), nil
}

// ---- load() ----------------------------------------------------------------------

// load is restricted to the recipe directory (design §17, implementation §3.4).
func (rt *Runtime) load(thread *starlark.Thread, module string) (starlark.StringDict, error) {
	if rt.RecipesFS == nil {
		return nil, fmt.Errorf("load: no recipe library")
	}
	m := strings.TrimPrefix(module, "//")
	m = strings.TrimPrefix(m, "recipes/")
	clean := path.Clean(m)
	if path.IsAbs(m) || strings.HasPrefix(clean, "..") || !strings.HasSuffix(clean, ".star") || strings.Contains(m, "\\") {
		return nil, fmt.Errorf("load: %q is outside the recipe library", module)
	}
	if e, ok := rt.loads[clean]; ok {
		if e == nil {
			return nil, fmt.Errorf("load: cycle through %s", clean)
		}
		return e.globals, e.err
	}
	rt.loads[clean] = nil
	data, err := fs.ReadFile(rt.RecipesFS, clean)
	if err != nil {
		rt.loads[clean] = &loadEntry{err: fmt.Errorf("load: %s not found", clean)}
		return nil, rt.loads[clean].err
	}
	lowered, err := sugar.Lower(string(data), sugar.Star)
	if err != nil {
		rt.loads[clean] = &loadEntry{err: fmt.Errorf("load %s: %w", clean, err)}
		return nil, rt.loads[clean].err
	}
	pre := starlark.StringDict{}
	for k, v := range rt.Predeclared() {
		pre[k] = v
	}
	noop := starlark.NewBuiltin("noop", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		return starlark.None, nil
	})
	pre["recipe"], pre["param"] = noop, noop
	g, err := starlark.ExecFileOptions(FileOptions, thread, clean, lowered, pre)
	rt.loads[clean] = &loadEntry{globals: g, err: err}
	return g, err
}

// ---- running ---------------------------------------------------------------------

// Lower lowers a script, detecting its syntax.
func Lower(src string) (string, error) { return sugar.Lower(src, sugar.Detect(src)) }

// Run lowers and executes an ad-hoc script.
func Run(ctx context.Context, src string, o Options) (*Result, error) {
	if o.Timeout == 0 {
		o.Timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	lowered, err := Lower(src)
	if err != nil {
		return &Result{Lowered: lowered, Error: err.Error()}, err
	}
	return runLowered(ctx, lowered, o)
}

// RunLowered executes Starlark that has already been lowered.
func RunLowered(ctx context.Context, lowered string, o Options) (*Result, error) {
	if o.Timeout == 0 {
		o.Timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	return runLowered(ctx, lowered, o)
}

func runLowered(ctx context.Context, lowered string, o Options) (*Result, error) {
	start := time.Now()
	res := &Result{Lowered: lowered}
	rt := NewRuntime(ctx, o)
	thread := rt.Thread()
	stop := context.AfterFunc(ctx, func() { thread.Cancel("deadline or stop") })
	defer stop()
	_, err := starlark.ExecFileOptions(FileOptions, thread, "script.star", lowered, rt.withRecipeBuiltins())
	res.Items = rt.items
	res.Steps = thread.ExecutionSteps()
	res.Cost, res.Calls, res.Source = rt.CallOpts.Ledger.Snapshot()
	res.Duration = time.Since(start)
	if err != nil {
		res.Error = cleanErr(err)
		return res, fmt.Errorf("%s", res.Error)
	}
	return res, nil
}

// withRecipeBuiltins adds no-op recipe()/param() so a recipe file's body can
// run as a script (dry runs of proposals).
func (rt *Runtime) withRecipeBuiltins() starlark.StringDict {
	pre := starlark.StringDict{}
	for k, v := range rt.Predeclared() {
		pre[k] = v
	}
	noop := starlark.NewBuiltin("noop", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		return starlark.None, nil
	})
	pre["recipe"], pre["param"] = noop, noop
	return pre
}

// Items returns what has been emitted so far.
func (rt *Runtime) Items() []Item { return rt.items }

// ToItems is exported for the recipes package (return values become items).
func (rt *Runtime) ToItems(label string, v starlark.Value) ([]Item, error) {
	return rt.toItems(label, v)
}

// Wrap exposes table wrapping to the recipes package.
func (rt *Runtime) Wrap(t *table.Table) starlark.Value { return rt.wrap(t) }

// CatalogPrimitive returns a primitive definition.
func (rt *Runtime) CatalogPrimitive(name string) (*catalog.Primitive, bool) {
	return rt.Engine.Catalog.Get(name)
}
