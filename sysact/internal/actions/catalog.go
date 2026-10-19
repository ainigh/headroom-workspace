// Package actions loads sysact's action catalog (implementation.md §4): one
// YAML file per action with a frozen argv template, tier, effect statement
// and undo hint. One action per request; no scripting, no composition.
package actions

import (
	_ "embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

//go:embed action.schema.json
var schemaText string

// Kinds of execution.
const (
	KindArgv         = "argv"           // run argv directly
	KindTrash        = "trash"          // move target to the Trash via Finder (recoverable)
	KindQuitApp      = "quit_app"       // ask the app with bundle id target to quit
	KindQuitAppNamed = "quit_app_named" // ask the app named target to quit
)

// Template is an argv template (strings or optional groups).
type Template []argv.Element

// UnmarshalYAML accepts strings and lists of strings.
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
			*t = append(*t, argv.Element{Group: g})
		default:
			return fmt.Errorf("line %d: argv items are strings or lists", it.Line)
		}
	}
	return nil
}

// Action is one catalog entry.
type Action struct {
	Name          string               `yaml:"name" json:"name"`
	Doc           string               `yaml:"doc" json:"doc"`
	Tier          string               `yaml:"tier" json:"tier"` // safe | caution | risky
	RequiresAdmin bool                 `yaml:"requires_admin" json:"requires_admin"`
	Params        map[string]argv.Spec `yaml:"params" json:"params"`
	ParamOrder    []string             `yaml:"-" json:"param_order"`
	Kind          string               `yaml:"kind" json:"kind"`
	Argv          Template             `yaml:"argv" json:"-"`
	BinaryPaths   []string             `yaml:"binary_paths" json:"-"`
	Target        string               `yaml:"target" json:"-"`
	Within        []string             `yaml:"within" json:"within,omitempty"` // target must be inside one of these
	MustExist     bool                 `yaml:"must_exist" json:"-"`
	Lookup        string               `yaml:"lookup" json:"-"` // "process": resolve a pid param to its name and owner
	Effect        string               `yaml:"effect" json:"effect"`
	UndoHint      string               `yaml:"undo_hint" json:"undo_hint,omitempty"`
	TimeoutRaw    string               `yaml:"timeout" json:"-"`
	Timeout       time.Duration        `yaml:"-" json:"-"`
	File          string               `yaml:"-" json:"-"`
}

// PathParams lists params of type path (checked against protected paths).
func (a *Action) PathParams() []string {
	var out []string
	for n, s := range a.Params {
		if s.Type == argv.TPath {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Catalog is the loaded action set.
type Catalog struct{ byName map[string]*Action }

// Get returns an action.
func (c *Catalog) Get(name string) (*Action, bool) { a, ok := c.byName[name]; return a, ok }

// All returns actions sorted by name.
func (c *Catalog) All() []*Action {
	var names []string
	for n := range c.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*Action, len(names))
	for i, n := range names {
		out[i] = c.byName[n]
	}
	return out
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// Load reads *.yaml from each root (later roots win by name).
func Load(roots ...fs.FS) (*Catalog, error) {
	comp := jsonschema.NewCompiler()
	if err := comp.AddResource("action.schema.json", strings.NewReader(schemaText)); err != nil {
		return nil, err
	}
	sch, err := comp.Compile("action.schema.json")
	if err != nil {
		return nil, err
	}
	c := &Catalog{byName: map[string]*Action{}}
	var errs []string
	for _, root := range roots {
		if root == nil {
			continue
		}
		_ = fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
				return nil
			}
			data, err := fs.ReadFile(root, p)
			if err != nil {
				return nil
			}
			a, err := parse(data, p, sch)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", p, err))
				return nil
			}
			c.byName[a.Name] = a
			return nil
		})
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("actions: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

func parse(data []byte, file string, sch *jsonschema.Schema) (*Action, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if err := sch.Validate(jsonCompat(doc)); err != nil {
		if ve, ok := err.(*jsonschema.ValidationError); ok {
			leaf := ve
			for len(leaf.Causes) > 0 {
				leaf = leaf.Causes[0]
			}
			return nil, fmt.Errorf("%s: %s", leaf.InstanceLocation, leaf.Message)
		}
		return nil, err
	}
	var a Action
	if err := yaml.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	a.File = file
	var n yaml.Node
	_ = yaml.Unmarshal(data, &n)
	if len(n.Content) > 0 {
		m := n.Content[0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == "params" {
				for j := 0; j+1 < len(m.Content[i+1].Content); j += 2 {
					a.ParamOrder = append(a.ParamOrder, m.Content[i+1].Content[j].Value)
				}
			}
		}
	}
	return &a, a.check()
}

func (a *Action) check() error {
	if !nameRe.MatchString(a.Name) {
		return fmt.Errorf("bad name %q", a.Name)
	}
	if a.Kind == "" {
		a.Kind = KindArgv
	}
	a.Timeout = 60 * time.Second
	if a.TimeoutRaw != "" {
		d, err := time.ParseDuration(a.TimeoutRaw)
		if err != nil {
			return err
		}
		a.Timeout = d
	}
	names := func(s string) error {
		for _, n := range argv.Names(s) {
			if _, ok := a.Params[n]; !ok && n != "binary" {
				return fmt.Errorf("placeholder {%s} is not a declared param", n)
			}
		}
		return nil
	}
	switch a.Kind {
	case KindArgv:
		if len(a.Argv) == 0 {
			return fmt.Errorf("argv actions need argv")
		}
		first := a.Argv[0].Text
		if first == "{binary}" {
			if len(a.BinaryPaths) == 0 {
				return fmt.Errorf("{binary} needs binary_paths")
			}
		} else if !filepath.IsAbs(first) || len(argv.Names(first)) > 0 {
			return fmt.Errorf("argv[0] must be an absolute path literal")
		}
		base := filepath.Base(first)
		for _, bad := range []string{"sh", "bash", "zsh", "env", "osascript", "python3", "perl", "ruby", "xargs", "sudo", "rm"} {
			if base == bad {
				return fmt.Errorf("%s may not be an action binary (no shells, no rm: trash is the delete path)", base)
			}
		}
		for _, el := range a.Argv {
			for _, s := range append([]string{el.Text}, el.Group...) {
				if err := names(s); err != nil {
					return err
				}
			}
		}
	case KindTrash, KindQuitApp, KindQuitAppNamed:
		if a.Target == "" || len(a.Argv) > 0 {
			return fmt.Errorf("%s actions take target, not argv", a.Kind)
		}
		if err := names(a.Target); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown kind %q", a.Kind)
	}
	effect := a.Effect
	if a.Lookup == "process" {
		effect = strings.NewReplacer("{process}", "", "{owner}", "").Replace(effect)
	}
	if err := names(effect); err != nil {
		return fmt.Errorf("effect: %w", err)
	}
	return nil
}

func jsonCompat(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, vv := range x {
			out[k] = jsonCompat(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = jsonCompat(vv)
		}
		return out
	case int:
		return float64(x)
	}
	return v
}
