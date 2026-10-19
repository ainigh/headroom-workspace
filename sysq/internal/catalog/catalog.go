// Package catalog loads primitive definitions (implementation.md §3.1). A
// primitive is data: a frozen argv template, a parser name, a schema, cost
// and sensitivity. Adding one means adding a YAML file (and, for a new output
// style, a parser).
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

//go:embed primitive.schema.json
var schemaJSON string

// Cost classes and their ledger units (design §17).
var CostUnits = map[string]int{"cheap": 1, "moderate": 10, "expensive": 100}

// CostRank orders cost classes.
func CostRank(c string) int {
	switch c {
	case "cheap":
		return 1
	case "moderate":
		return 2
	case "expensive":
		return 3
	}
	return 0
}

// Template is an argv template that unmarshals from a YAML list whose items
// are strings or lists of strings (optional groups).
type Template []argv.Element

// UnmarshalYAML implements yaml.Unmarshaler.
func (t *Template) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: argv must be a list", n.Line)
	}
	for _, it := range n.Content {
		switch it.Kind {
		case yaml.ScalarNode:
			*t = append(*t, argv.Element{Text: it.Value})
		case yaml.SequenceNode:
			var g []string
			if err := it.Decode(&g); err != nil {
				return err
			}
			if len(g) == 0 {
				return fmt.Errorf("line %d: empty optional group", it.Line)
			}
			*t = append(*t, argv.Element{Group: g})
		default:
			return fmt.Errorf("line %d: argv items are strings or lists of strings", it.Line)
		}
	}
	return nil
}

// MarshalJSON renders the template for describe/explain.
func (t Template) MarshalJSON() ([]byte, error) {
	out := make([]any, len(t))
	for i, e := range t {
		if e.Group != nil {
			out[i] = e.Group
		} else {
			out[i] = e.Text
		}
	}
	return json.Marshal(out)
}

// Command is one subprocess of a primitive.
type Command struct {
	ID          string   `yaml:"id" json:"id"`
	Argv        Template `yaml:"argv" json:"argv"`
	BinaryPaths []string `yaml:"binary_paths,omitempty" json:"binary_paths,omitempty"`
	Optional    bool     `yaml:"optional,omitempty" json:"optional,omitempty"`
	AllowExit   []int    `yaml:"allow_exit,omitempty" json:"allow_exit,omitempty"`
}

// Sudo describes a narrow sudo rule a primitive needs.
type Sudo struct {
	Binary string   `yaml:"binary" json:"binary"`
	Args   []string `yaml:"args" json:"args"`
}

// Pushdown says which operators the planner may push into params.
type Pushdown struct {
	Filter *FilterPushdown `yaml:"filter,omitempty" json:"filter,omitempty"`
	Limit  string          `yaml:"limit,omitempty" json:"limit,omitempty"` // param receiving limit n
}

// FilterPushdown maps predicate shapes into a param.
type FilterPushdown struct {
	// Param of type predicate that receives the pushed expression.
	Predicate string `yaml:"predicate,omitempty" json:"predicate,omitempty"`
	// Bounds: column comparisons pushed into scalar params, e.g.
	// size > X  ->  min_size = X;  modified > X -> since = X.
	Bounds []BoundPushdown `yaml:"bounds,omitempty" json:"bounds,omitempty"`
}

// BoundPushdown pushes `column op value` into a param.
type BoundPushdown struct {
	Column string   `yaml:"column" json:"column"`
	Ops    []string `yaml:"ops" json:"ops"`
	Param  string   `yaml:"param" json:"param"`
}

// Primitive is one catalog entry.
type Primitive struct {
	Name          string               `yaml:"name" json:"name"`
	Doc           string               `yaml:"doc" json:"doc"`
	Params        map[string]argv.Spec `yaml:"params,omitempty" json:"params,omitempty"`
	ParamOrder    []string             `yaml:"-" json:"param_order,omitempty"`
	Cost          string               `yaml:"cost" json:"cost"`
	Sensitivity   string               `yaml:"sensitivity" json:"sensitivity"`
	Network       bool                 `yaml:"network,omitempty" json:"network,omitempty"`
	Argv          Template             `yaml:"argv,omitempty" json:"argv,omitempty"`
	BinaryPaths   []string             `yaml:"binary_paths,omitempty" json:"binary_paths,omitempty"`
	AllowExit     []int                `yaml:"allow_exit,omitempty" json:"allow_exit,omitempty"`
	Commands      []Command            `yaml:"commands,omitempty" json:"commands,omitempty"`
	Builtin       string               `yaml:"builtin,omitempty" json:"builtin,omitempty"`
	Parser        string               `yaml:"parser,omitempty" json:"parser,omitempty"`
	TimeoutRaw    string               `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	PartialOK     bool                 `yaml:"partial_ok,omitempty" json:"partial_ok,omitempty"`
	MaxBytes      int64                `yaml:"max_bytes,omitempty" json:"max_bytes,omitempty"`
	Schema        []table.Col          `yaml:"schema" json:"schema"`
	Sandbox       string               `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	SandboxExec   []string             `yaml:"sandbox_exec,omitempty" json:"sandbox_exec,omitempty"`
	CacheTTLRaw   string               `yaml:"cache_ttl,omitempty" json:"cache_ttl,omitempty"`
	RequiresSudo  *Sudo                `yaml:"requires_sudo,omitempty" json:"requires_sudo,omitempty"`
	Redact        bool                 `yaml:"redact,omitempty" json:"redact,omitempty"`
	Pushdown      *Pushdown            `yaml:"pushdown,omitempty" json:"pushdown,omitempty"`
	Tags          []string             `yaml:"tags,omitempty" json:"tags,omitempty"`
	FixtureParams map[string]any       `yaml:"fixture_params,omitempty" json:"fixture_params,omitempty"`

	Timeout  time.Duration `yaml:"-" json:"-"`
	CacheTTL time.Duration `yaml:"-" json:"-"`
	File     string        `yaml:"-" json:"-"`
}

// Kind distinguishes primitives from recipes in describe output.
func (p *Primitive) Kind() string { return "primitive" }

// AllCommands returns the primitive's commands (top-level argv as "main").
func (p *Primitive) AllCommands() []Command {
	if len(p.Argv) > 0 {
		return []Command{{ID: "main", Argv: p.Argv, BinaryPaths: p.BinaryPaths, AllowExit: p.AllowExit}}
	}
	return p.Commands
}

// Catalog is the loaded set of primitives.
type Catalog struct {
	byName map[string]*Primitive
}

// Get returns a primitive by name.
func (c *Catalog) Get(name string) (*Primitive, bool) {
	p, ok := c.byName[name]
	return p, ok
}

// Names returns all primitive names, sorted.
func (c *Catalog) Names() []string {
	out := make([]string, 0, len(c.byName))
	for n := range c.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// All returns every primitive, sorted by name.
func (c *Catalog) All() []*Primitive {
	var out []*Primitive
	for _, n := range c.Names() {
		out = append(out, c.byName[n])
	}
	return out
}

// Namespaces returns the distinct first name segments.
func (c *Catalog) Namespaces() []string {
	seen := map[string]bool{}
	for n := range c.byName {
		seen[strings.SplitN(n, ".", 2)[0]] = true
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Add inserts a primitive (used for builtins and tests).
func (c *Catalog) Add(p *Primitive) error {
	if c.byName == nil {
		c.byName = map[string]*Primitive{}
	}
	if _, dup := c.byName[p.Name]; dup {
		return fmt.Errorf("duplicate primitive %s", p.Name)
	}
	c.byName[p.Name] = p
	return nil
}

var compiled *jsonschema.Schema

func schema() (*jsonschema.Schema, error) {
	if compiled != nil {
		return compiled, nil
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("primitive.schema.json", strings.NewReader(schemaJSON)); err != nil {
		return nil, err
	}
	s, err := c.Compile("primitive.schema.json")
	if err != nil {
		return nil, err
	}
	compiled = s
	return s, nil
}

// Load reads every *.yaml under each fs root (later roots override earlier
// ones by name). Directories named "examples" are skipped.
func Load(roots ...fs.FS) (*Catalog, error) {
	c := &Catalog{byName: map[string]*Primitive{}}
	var errs []string
	for _, root := range roots {
		if root == nil {
			continue
		}
		err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return fs.SkipDir
				}
				return err
			}
			if d.IsDir() && d.Name() == "examples" {
				return fs.SkipDir
			}
			if d.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml")) {
				return nil
			}
			data, err := fs.ReadFile(root, path)
			if err != nil {
				return err
			}
			p, err := Parse(data, path)
			if err != nil {
				errs = append(errs, err.Error())
				return nil
			}
			c.byName[p.Name] = p
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return c, fmt.Errorf("catalog: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// Parse decodes and validates one primitive file.
func Parse(data []byte, file string) (*Primitive, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	s, err := schema()
	if err != nil {
		return nil, err
	}
	if err := s.Validate(toJSONCompatible(doc)); err != nil {
		return nil, fmt.Errorf("%s: %s", file, schemaError(err))
	}
	var p Primitive
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	p.File = file
	p.ParamOrder = paramOrder(data)
	if err := p.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &p, nil
}

func schemaError(err error) string {
	if ve, ok := err.(*jsonschema.ValidationError); ok {
		leaf := ve
		for len(leaf.Causes) > 0 {
			leaf = leaf.Causes[0]
		}
		return fmt.Sprintf("%s: %s", leaf.InstanceLocation, leaf.Message)
	}
	return err.Error()
}

// paramOrder recovers declaration order of params from the YAML node tree.
func paramOrder(data []byte) []string {
	var n yaml.Node
	if yaml.Unmarshal(data, &n) != nil || len(n.Content) == 0 {
		return nil
	}
	m := n.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "params" && m.Content[i+1].Kind == yaml.MappingNode {
			var out []string
			pm := m.Content[i+1]
			for j := 0; j+1 < len(pm.Content); j += 2 {
				out = append(out, pm.Content[j].Value)
			}
			return out
		}
	}
	return nil
}

func (p *Primitive) check() error {
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("bad name %q", p.Name)
	}
	if p.TimeoutRaw != "" {
		d, err := time.ParseDuration(p.TimeoutRaw)
		if err != nil {
			return fmt.Errorf("timeout: %w", err)
		}
		p.Timeout = d
	} else {
		p.Timeout = 10 * time.Second
	}
	if p.CacheTTLRaw != "" {
		d, err := time.ParseDuration(p.CacheTTLRaw)
		if err != nil {
			return fmt.Errorf("cache_ttl: %w", err)
		}
		p.CacheTTL = d
	}
	if p.Sandbox == "" {
		p.Sandbox = "read-only-default"
	}
	if p.Network && p.Sandbox != "read-only-network" && p.Sandbox != "read-only-loopback" {
		return fmt.Errorf("network primitives must use the read-only-network or read-only-loopback profile")
	}
	if !p.Network && (p.Sandbox == "read-only-network" || p.Sandbox == "read-only-loopback") {
		return fmt.Errorf("%s profile requires network: true", p.Sandbox)
	}
	if p.RequiresSudo != nil && p.Sandbox != "read-only-sudo" {
		return fmt.Errorf("sudo primitives must use the read-only-sudo profile")
	}
	n := 0
	if len(p.Argv) > 0 {
		n++
	}
	if len(p.Commands) > 0 {
		n++
	}
	if p.Builtin != "" {
		n++
	}
	if n != 1 {
		return fmt.Errorf("exactly one of argv, commands, builtin is required")
	}
	if p.Builtin == "" && p.Parser == "" {
		return fmt.Errorf("parser is required")
	}
	for name, s := range p.Params {
		if !contains(argv.KnownTypes, s.Type) {
			return fmt.Errorf("param %s: unknown type %q", name, s.Type)
		}
		if s.Pattern != "" {
			if _, err := regexp.Compile(s.Pattern); err != nil {
				return fmt.Errorf("param %s: %w", name, err)
			}
		}
		if s.Type == argv.TEnum && len(s.Values) == 0 {
			return fmt.Errorf("param %s: enum needs values", name)
		}
	}
	ids := map[string]bool{}
	for _, c := range p.AllCommands() {
		if ids[c.ID] {
			return fmt.Errorf("duplicate command id %s", c.ID)
		}
		ids[c.ID] = true
		if len(c.Argv) == 0 {
			return fmt.Errorf("command %s: empty argv", c.ID)
		}
		first := c.Argv[0]
		if first.Group != nil {
			return fmt.Errorf("command %s: argv[0] cannot be optional", c.ID)
		}
		if first.Text == "{binary}" {
			if len(c.BinaryPaths) == 0 {
				return fmt.Errorf("command %s: {binary} needs binary_paths", c.ID)
			}
		} else if !filepath.IsAbs(first.Text) || len(argv.Names(first.Text)) > 0 {
			return fmt.Errorf("command %s: argv[0] must be an absolute path literal", c.ID)
		}
		for _, b := range c.BinaryPaths {
			if !filepath.IsAbs(b) {
				return fmt.Errorf("command %s: binary path %q not absolute", c.ID, b)
			}
		}
		if base := filepath.Base(first.Text); base == "sh" || base == "bash" || base == "zsh" || base == "env" || base == "osascript" || base == "xargs" {
			return fmt.Errorf("command %s: %s is not allowed as a primitive binary", c.ID, base)
		}
		for _, el := range c.Argv[1:] {
			texts := el.Group
			if texts == nil {
				texts = []string{el.Text}
			}
			for _, s := range texts {
				for _, nm := range argv.Names(s) {
					if nm == "binary" {
						continue
					}
					if _, ok := p.Params[nm]; !ok {
						return fmt.Errorf("command %s: placeholder {%s} is not a declared param", c.ID, nm)
					}
				}
			}
		}
		if filepath.Base(first.Text) == "sudo" {
			if err := checkSudo(p, c); err != nil {
				return fmt.Errorf("command %s: %w", c.ID, err)
			}
		} else if err := deniedFlags(first.Text, c.Argv); err != nil {
			return fmt.Errorf("command %s: %w", c.ID, err)
		}
		// {binary}: the verb rules of every candidate binary apply too.
		for _, b := range c.BinaryPaths {
			if base := filepath.Base(b); base == "sh" || base == "bash" || base == "zsh" || base == "env" || base == "osascript" || base == "xargs" || base == "sudo" {
				return fmt.Errorf("command %s: %s is not allowed as a primitive binary", c.ID, base)
			}
			if err := deniedFlags(b, c.Argv); err != nil {
				return fmt.Errorf("command %s: %w", c.ID, err)
			}
		}
	}
	for _, col := range p.Schema {
		if !col.Type.IsKnown() {
			return fmt.Errorf("schema column %s: unknown type %q", col.Name, col.Type)
		}
	}
	if p.Pushdown != nil && p.Pushdown.Filter != nil && p.Pushdown.Filter.Predicate != "" {
		if s, ok := p.Params[p.Pushdown.Filter.Predicate]; !ok || s.Type != argv.TPredicate {
			return fmt.Errorf("pushdown predicate param must be a declared predicate param")
		}
	}
	return nil
}

// checkSudo allows /usr/bin/sudo only as `sudo -n <binary> <args...>`
// exactly matching the primitive's requires_sudo rule, with no placeholders
// (the sudoers rule names that exact binary and flags).
func checkSudo(p *Primitive, c Command) error {
	if p.RequiresSudo == nil {
		return fmt.Errorf("sudo is only allowed for primitives with requires_sudo")
	}
	if c.Argv[0].Text != "/usr/bin/sudo" {
		return fmt.Errorf("sudo must be /usr/bin/sudo")
	}
	want := append([]string{"-n", p.RequiresSudo.Binary}, p.RequiresSudo.Args...)
	got := c.Argv[1:]
	if len(got) != len(want) {
		return fmt.Errorf("sudo argv must be exactly: sudo %s", strings.Join(want, " "))
	}
	for i, el := range got {
		if el.Group != nil || el.Text != want[i] || len(argv.Names(el.Text)) > 0 {
			return fmt.Errorf("sudo argv must be exactly: sudo %s", strings.Join(want, " "))
		}
	}
	inner := append(Template{{Text: p.RequiresSudo.Binary}}, got[2:]...)
	return deniedFlags(p.RequiresSudo.Binary, inner)
}

// deniedFlags refuses mutating or phoning-home verbs (design §6.1).
func deniedFlags(bin string, t Template) error {
	var words []string
	for _, el := range t {
		if el.Group != nil {
			words = append(words, el.Group...)
		} else {
			words = append(words, el.Text)
		}
	}
	joined := " " + strings.Join(words, " ") + " "
	base := filepath.Base(bin)
	if base == "{binary}" {
		base = ""
	}
	bad := []string{" -exec ", " -execdir ", " -delete ", " -ok ", " --output ", " -fprint ", " -fls "}
	for _, b := range bad {
		if strings.Contains(joined, b) {
			return fmt.Errorf("flag %q is not allowed", strings.TrimSpace(b))
		}
	}
	verbs := map[string][]string{
		"git":            {"fetch", "pull", "push", "commit", "checkout", "reset", "clean", "gc", "remote"},
		"brew":           {"update", "upgrade", "install", "uninstall", "cleanup", "tap"},
		"softwareupdate": {"--install", "-i", "--download", "-d", "--list", "-l"},
		"tmutil":         {"startbackup", "stopbackup", "delete", "deletelocalsnapshots", "thinlocalsnapshots", "enable", "disable"},
		"mdutil":         {"-E", "-i", "-a"},
		"defaults":       {"write", "delete", "import", "rename"},
		"launchctl":      {"load", "unload", "bootstrap", "bootout", "kickstart", "kill", "start", "stop", "enable", "disable", "remove", "submit"},
		"diskutil":       {"erase", "eraseDisk", "eraseVolume", "partitionDisk", "unmount", "unmountDisk", "mount", "repairVolume", "repairDisk", "apfs", "resizeVolume", "rename"},
		"pmset":          {"-a", "-b", "-c", "-u", "sleepnow", "displaysleepnow", "schedule", "repeat"},
		"sqlite3":        {".import", ".save", ".restore", ".output"},
	}
	for _, v := range verbs[base] {
		for _, w := range words[1:] {
			if w == v {
				return fmt.Errorf("%s %s is not read-only", base, v)
			}
		}
	}
	if base == "sudo" || base == "killall" || base == "kill" || base == "rm" || base == "mv" || base == "cp" || base == "dd" || base == "curl" || base == "wget" || base == "open" {
		return fmt.Errorf("%s is not a read-only binary", base)
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// toJSONCompatible converts YAML-decoded maps for the JSON Schema validator.
func toJSONCompatible(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, vv := range x {
			out[k] = toJSONCompatible(vv)
		}
		return out
	case map[any]any:
		out := map[string]any{}
		for k, vv := range x {
			out[fmt.Sprint(k)] = toJSONCompatible(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = toJSONCompatible(vv)
		}
		return out
	case int:
		return json.Number(fmt.Sprint(x))
	case int64:
		return json.Number(fmt.Sprint(x))
	case float64:
		return json.Number(fmt.Sprint(x))
	case time.Time:
		return x.Format(time.RFC3339)
	}
	return v
}

// ToJSONCompatible is exported for other YAML loaders in sysq.
func ToJSONCompatible(v any) any { return toJSONCompatible(v) }
