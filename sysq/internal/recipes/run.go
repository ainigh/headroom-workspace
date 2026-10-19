package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/sugar"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// Env is what running recipes needs besides the library.
type Env struct {
	Engine      *engine.Engine
	SnapshotDir string
	History     script.HistoryFunc
	Timeout     time.Duration
}

// Options builds script options for this library.
func (lib *Library) Options(env Env, opts engine.CallOpts) script.Options {
	return script.Options{
		Engine: env.Engine, CallOpts: opts, Recipes: lib, History: env.History,
		RecipesFS: lib.FS, SnapshotDir: env.SnapshotDir, Timeout: env.Timeout,
	}
}

// Run executes one recipe and returns its output as items: the return value
// (a table, or a record of tables) followed by anything it emitted.
func (lib *Library) Run(ctx context.Context, env Env, name string, params map[string]any, opts engine.CallOpts) ([]script.Item, error) {
	timeout := env.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rt := script.NewRuntime(ctx, lib.Options(env, opts))
	thread := rt.Thread()
	stop := context.AfterFunc(ctx, func() { thread.Cancel("deadline or stop") })
	defer stop()
	v, err := lib.Call(rt, name, params)
	if err != nil {
		return rt.Items(), err
	}
	items, err := rt.ToItems("", v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	for _, it := range items {
		engine.ApplyCaps(it.Table, env.Engine.Policy.Caps)
	}
	return append(items, rt.Items()...), nil
}

// RunAttention runs attention() as a call of its own.
func (lib *Library) RunAttention(ctx context.Context, env Env, minSeverity string, opts engine.CallOpts) (*table.Table, error) {
	rt := script.NewRuntime(ctx, lib.Options(env, opts))
	return lib.Attention(rt, minSeverity)
}

// ---- derived output schema ------------------------------------------------------

var schemaCache sync.Map

// Schema derives a recipe's output schema by a dry run against the shipped
// fixtures (no live command runs). The result is a list of labeled schemas.
func (lib *Library) Schema(fixtures *engine.Engine, name string) ([]map[string]any, error) {
	if v, ok := schemaCache.Load(name); ok {
		return v.([]map[string]any), nil
	}
	if fixtures == nil || fixtures.Fixtures == nil {
		return nil, fmt.Errorf("schema derivation needs a fixture engine")
	}
	items, err := lib.Run(context.Background(), Env{Engine: fixtures, Timeout: 20 * time.Second}, name, nil, engine.CallOpts{Ledger: &engine.Ledger{}})
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, it := range items {
		out = append(out, map[string]any{"label": it.Label, "schema": it.Table.Schema})
	}
	schemaCache.Store(name, out)
	return out, nil
}

// ---- proposals --------------------------------------------------------------------

var (
	tagRe    = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	reserved = map[string]bool{"cost": true, "sensitivity": true}
)

// Proposal is what a model submits (design §19.5).
type Proposal struct {
	Name    string   `json:"name"`
	Doc     string   `json:"doc"`
	Tags    []string `json:"tags,omitempty"`
	Example string   `json:"example,omitempty"`
	Script  string   `json:"script"`
	// Anything else (cost, sensitivity, ...) is refused.
	Extra map[string]any `json:"-"`
}

// Propose writes a proposal into dir/proposed/<name>.star. It never touches
// the callable library: promotion is a human command.
func (lib *Library) Propose(dir string, p Proposal) (string, error) {
	for k := range p.Extra {
		if reserved[k] {
			return "", fmt.Errorf("proposals may not declare %s: it is derived by analysis", k)
		}
		return "", fmt.Errorf("unknown proposal field %s", k)
	}
	if !nameRe.MatchString(p.Name) {
		return "", fmt.Errorf("bad recipe name %q", p.Name)
	}
	if _, exists := lib.recipes[p.Name]; exists {
		return "", fmt.Errorf("%s already exists in the library", p.Name)
	}
	if _, isPrim := lib.Catalog.Get(p.Name); isPrim {
		return "", fmt.Errorf("%s is a primitive", p.Name)
	}
	if strings.TrimSpace(p.Doc) == "" || len(p.Doc) > 300 {
		return "", fmt.Errorf("doc is required (≤300 chars)")
	}
	for _, t := range p.Tags {
		if !tagRe.MatchString(t) {
			return "", fmt.Errorf("bad tag %q", t)
		}
	}
	if len(p.Script) > 20000 {
		return "", fmt.Errorf("script too long")
	}
	lowered, err := sugar.Lower(p.Script, sugar.Detect(p.Script))
	if err != nil {
		return "", fmt.Errorf("script: %w", err)
	}
	if strings.Contains(lowered, "recipe(") || strings.Contains(lowered, "load(") {
		return "", fmt.Errorf("a proposal is a script body; it may not declare recipes or load modules")
	}
	var b strings.Builder
	b.WriteString("# Proposed by a model. Not callable until a human runs: sysq recipe promote " + p.Name + "\n")
	b.WriteString("recipe(\n  name = " + Quote(p.Name) + ",\n  doc = " + Quote(p.Doc) + ",\n")
	if len(p.Tags) > 0 {
		q := make([]string, len(p.Tags))
		for i, t := range p.Tags {
			q[i] = Quote(t)
		}
		b.WriteString("  tags = [" + strings.Join(q, ", ") + "],\n")
	}
	if p.Example != "" {
		b.WriteString("  example = " + Quote(p.Example) + ",\n")
	}
	b.WriteString(")\n\ndef run():\n")
	for _, line := range strings.Split(strings.TrimRight(lowered, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("  return None\n")
	src := b.String()
	// It must load cleanly as a recipe.
	f := &file{path: "proposed/" + p.Name + ".star", src: src, proposed: true}
	if err := f.parse(); err != nil {
		return "", err
	}
	predeclared := scriptNames(lib)
	if _, err := lib.compile(f, predeclared); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "proposed", p.Name+".star")
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return "", err
	}
	if _, err := os.Stat(out); err == nil {
		return "", fmt.Errorf("a proposal named %s is already waiting for review", p.Name)
	}
	return out, os.WriteFile(out, []byte(src), 0o600)
}

func scriptNames(lib *Library) map[string]bool {
	m := script.PredeclaredNames(lib.Catalog, lib.Names())
	m["recipe"], m["param"] = true, true
	return m
}

// Promote moves a reviewed proposal into the user's recipe library and
// writes its dry-run output as a golden next to it. Humans only (CLI).
func (lib *Library) Promote(dir, name string, golden []script.Item) (string, error) {
	r, ok := lib.proposed[name]
	if !ok {
		return "", fmt.Errorf("no proposal named %s", name)
	}
	group := strings.SplitN(name, ".", 2)[0]
	dst := filepath.Join(dir, group, strings.ReplaceAll(strings.TrimPrefix(name, group+"."), ".", "_")+".star")
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("%s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	src := strings.Replace(r.Source, "# Proposed by a model. Not callable until a human runs: sysq recipe promote "+name+"\n", "# Proposed by a model; promoted by a human on "+time.Now().Format("2006-01-02")+".\n", 1)
	if err := os.WriteFile(dst, []byte(src), 0o644); err != nil {
		return "", err
	}
	if golden != nil {
		data, _ := json.MarshalIndent(golden, "", "  ")
		_ = os.WriteFile(strings.TrimSuffix(dst, ".star")+".golden.json", data, 0o644)
	}
	_ = os.Remove(filepath.Join(dir, r.File))
	return dst, nil
}

// Describe returns the describe() view of a recipe.
func (r *Recipe) Describe() map[string]any {
	params := map[string]any{}
	var order []string
	for _, p := range r.Params {
		params[p.Name] = p.Spec
		order = append(order, p.Name)
	}
	out := map[string]any{
		"name": r.Name, "kind": "recipe", "doc": r.Doc, "params": params, "param_order": order,
		"cost": r.Cost, "sensitivity": r.Sensitivity, "primitives": r.Primitives,
	}
	if len(r.Uses) > 0 {
		out["uses"] = r.Uses
	}
	if len(r.Tags) > 0 {
		out["tags"] = r.Tags
	}
	if r.Example != "" {
		out["example"] = r.Example
	}
	if r.Rule != nil {
		out["rule"] = r.Rule
	}
	if r.Unavailable != "" {
		out["unavailable"] = r.Unavailable
	}
	if r.Proposed {
		out["proposed"] = true
	}
	return out
}

// Groups returns recipe groups and their recipes.
func (lib *Library) Groups() map[string][]string {
	out := map[string][]string{}
	for _, n := range lib.Names() {
		g := strings.SplitN(n, ".", 2)[0]
		out[g] = append(out[g], n)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}
