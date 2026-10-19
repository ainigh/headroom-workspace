package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/render"
	"github.com/ainigh/headroom-workspace/sysq/internal/setup"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/pkg/audit"
)

var (
	flagFixtures string
	flagFormat   string
	flagDir      string
	flagFresh    bool
)

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "sysq",
		Short:         "Read-only queries over this Mac",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&flagFixtures, "fixtures", os.Getenv("SYSQ_FIXTURES"), `serve recorded output instead of running commands ("builtin" or a directory)`)
	root.PersistentFlags().StringVarP(&flagFormat, "format", "o", "table", "output: table, json, csv, md")
	root.PersistentFlags().StringVar(&flagDir, "dir", "", "sysq data directory (default ~/Library/Application Support/sysq)")
	root.PersistentFlags().BoolVar(&flagFresh, "fresh", false, "bypass the result cache")
	root.PersistentFlags().Bool("json", false, "same as --format json")
	root.PersistentFlags().Bool("csv", false, "same as --format csv")
	root.PersistentFlags().Bool("md", false, "same as --format md")
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		for _, f := range []string{"json", "csv", "md"} {
			if v, _ := cmd.Flags().GetBool(f); v {
				flagFormat = f
			}
		}
	}
	root.AddCommand(describeCmd(), runCmd(), fixtureCmd(), auditCmd())
	addExtraCommands(root)
	return root
}

// addExtraCommands is extended by later milestones (script, recipes).
var extraCommands []func(*cobra.Command)

func addExtraCommands(root *cobra.Command) {
	for _, f := range extraCommands {
		f(root)
	}
}

func openEnv() (*setup.Env, error) {
	env, err := setup.Open(setup.Options{Dir: flagDir, Fixtures: flagFixtures})
	if err != nil {
		return nil, err
	}
	if env.PolicyError != nil {
		fmt.Fprintln(os.Stderr, "sysq: policy.yaml ignored:", env.PolicyError)
	}
	if env.CatalogErr != nil {
		fmt.Fprintln(os.Stderr, "sysq: some catalog files were skipped:", env.CatalogErr)
	}
	return env, nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// parseKV turns ["a=1", "b=x"] into params (values stay strings; validation
// converts them).
func parseKV(args []string) (map[string]any, error) {
	out := map[string]any{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("expected name=value, got %q", a)
		}
		out[strings.TrimLeft(k, "-")] = v
	}
	return out, nil
}

func describeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe [name]",
		Short: "List the catalog, or show one primitive or recipe's schema",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			d := describer(env)
			if len(args) == 1 {
				v, err := d.One(args[0])
				if err != nil {
					return err
				}
				if flagFormat == "table" {
					b, _ := yaml.Marshal(v)
					_, err = os.Stdout.Write(b)
					return err
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(v)
			}
			return render.Write(os.Stdout, flagFormat, []render.Item{{Table: d.Index()}})
		},
	}
}

// Describer lists primitives (and, from M2, recipes) as one catalog.
type Describer interface {
	Index() *table.Table
	One(name string) (any, error)
}

var describer = func(env *setup.Env) Describer { return primDescriber{env} }

type primDescriber struct{ env *setup.Env }

func (d primDescriber) Index() *table.Table {
	t := table.New([]table.Col{
		{Name: "name", Type: table.Str}, {Name: "kind", Type: table.Str},
		{Name: "cost", Type: table.Str}, {Name: "sensitivity", Type: table.Str},
		{Name: "doc", Type: table.Str},
	})
	for _, p := range d.env.Catalog.All() {
		t.Append(p.Name, "primitive", p.Cost, p.Sensitivity, p.Doc)
	}
	return t
}

func (d primDescriber) One(name string) (any, error) {
	p, ok := d.env.Catalog.Get(name)
	if !ok {
		return nil, fmt.Errorf("no primitive or recipe named %q", name)
	}
	return DescribePrimitive(p), nil
}

// DescribePrimitive is the describe() view of a primitive.
func DescribePrimitive(p *catalog.Primitive) map[string]any {
	params := map[string]any{}
	for k, s := range p.Params {
		params[k] = s
	}
	out := map[string]any{
		"name": p.Name, "kind": "primitive", "doc": p.Doc, "cost": p.Cost,
		"sensitivity": p.Sensitivity, "schema": p.Schema, "params": params,
	}
	if len(p.ParamOrder) > 0 {
		out["param_order"] = p.ParamOrder
	}
	if p.Network {
		out["network"] = true
	}
	if p.RequiresSudo != nil {
		out["requires_sudo"] = p.RequiresSudo
	}
	return out
}

// runner executes a name (primitive; recipes from M2) for `sysq run`.
var runName = func(ctx context.Context, env *setup.Env, name string, params map[string]any, opts engine.CallOpts) ([]render.Item, error) {
	t, err := env.Engine.Primitive(ctx, name, params, opts)
	if err != nil {
		return nil, err
	}
	return []render.Item{{Table: t}}, nil
}

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <name> [param=value ...]",
		Short: "Run a primitive or recipe",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			params, err := parseKV(args[1:])
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			start := time.Now()
			ledger := &engine.Ledger{Budget: env.Policy.CostBudget}
			items, err := runName(ctx, env, args[0], params, engine.CallOpts{Ledger: ledger, Fresh: flagFresh})
			auditRun(env, "run", args[0], "", params, ledger, start, items, err)
			if err != nil {
				return err
			}
			return render.Write(os.Stdout, flagFormat, items)
		},
	}
	return cmd
}

func auditRun(env *setup.Env, kind, name, script string, params map[string]any, l *engine.Ledger, start time.Time, items []render.Item, runErr error) {
	_, _, prims := l.Snapshot()
	rec := audit.Record{
		Component: "sysq", Caller: "cli", Kind: kind, Name: name, Script: script, Params: params,
		Primitives: prims, DurationMS: time.Since(start).Milliseconds(),
	}
	for _, it := range items {
		rec.Rows += it.Table.Len()
		b, _ := json.Marshal(it.Table)
		rec.Bytes += len(b)
		rec.Sensitivity = table.MaxSensitivity(rec.Sensitivity, it.Table.Meta.Sensitivity)
		rec.Truncated = rec.Truncated || it.Table.Meta.Truncated
	}
	if runErr != nil {
		rec.Error = runErr.Error()
	}
	_ = env.Audit.Write(rec)
}

func auditCmd() *cobra.Command {
	var last time.Duration
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show recent audit records",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			recs, err := env.Audit.Read(time.Now().Add(-last))
			if err != nil {
				return err
			}
			t := table.New([]table.Col{
				{Name: "ts", Type: table.TS}, {Name: "caller", Type: table.Str}, {Name: "kind", Type: table.Str},
				{Name: "name", Type: table.Str}, {Name: "primitives", Type: table.Str},
				{Name: "duration", Type: table.Duration}, {Name: "rows", Type: table.Int},
				{Name: "sensitivity", Type: table.Str}, {Name: "error", Type: table.Str},
			})
			for _, r := range recs {
				t.Append(r.TS, r.Caller, r.Kind, r.Name, strings.Join(r.Primitives, ","), float64(r.DurationMS)/1000, int64(r.Rows), r.Sensitivity, r.Error)
			}
			return render.Write(os.Stdout, flagFormat, []render.Item{{Table: t}})
		},
	}
	cmd.Flags().DurationVar(&last, "last", time.Hour, "how far back")
	return cmd
}

func fixtureCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "fixture", Short: "Capture and inspect primitive fixtures"}
	var all bool
	var out string
	capture := &cobra.Command{
		Use:   "capture [primitive ...] [--all]",
		Short: "Run primitives (sandboxed) and record their raw output as fixtures",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := setup.Open(setup.Options{Dir: flagDir})
			if err != nil {
				return err
			}
			names := args
			if all {
				names = nil
				for _, p := range env.Catalog.All() {
					if p.Builtin == "" && (!p.Network || env.Policy.NetworkEnabled(p.Name)) {
						names = append(names, p.Name)
					}
				}
			}
			if len(names) == 0 {
				return fmt.Errorf("name primitives or pass --all")
			}
			if out == "" {
				out = defaultFixtureDir()
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			manifest := loadManifest(out)
			failed := 0
			for _, n := range names {
				p, ok := env.Catalog.Get(n)
				if !ok {
					return fmt.Errorf("unknown primitive %q", n)
				}
				outs, err := env.Engine.Capture(ctx, n, p.FixtureParams)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  %-22s %v\n", n, err)
					failed++
					continue
				}
				for id, data := range outs {
					file := n + "." + id + ".txt"
					if id == "main" {
						file = n + ".txt"
					}
					if err := os.WriteFile(filepath.Join(out, file), data, 0o644); err != nil {
						return err
					}
				}
				manifest.Fixtures[n] = "captured"
				fmt.Printf("  %-22s captured (%d command(s))\n", n, len(outs))
			}
			home, _ := os.UserHomeDir()
			manifest.Home = home
			manifest.CapturedAt = time.Now().Format(time.RFC3339)
			if tz := os.Getenv("TZ"); tz != "" {
				manifest.Timezone = tz
			} else if l := time.Now().Location().String(); l != "Local" {
				manifest.Timezone = l
			}
			b, _ := yaml.Marshal(manifest)
			header := "# Provenance of each fixture: \"captured\" = real output from this Mac,\n# \"synthetic\" = written from the command's documented format.\n# Recapture: sysq fixture capture --all, then go test ./internal/parsers -update\n"
			if err := os.WriteFile(filepath.Join(out, "MANIFEST.yaml"), append([]byte(header), b...), 0o644); err != nil {
				return err
			}
			fmt.Printf("wrote %s; now run: (cd sysq && go test ./internal/parsers -update) and review the golden diff\n", out)
			if failed > 0 {
				return fmt.Errorf("%d primitive(s) could not be captured", failed)
			}
			return nil
		},
	}
	capture.Flags().BoolVar(&all, "all", false, "capture every primitive")
	capture.Flags().StringVar(&out, "out", "", "fixture directory (default sysq/tests/fixtures when run in the repo)")
	status := &cobra.Command{
		Use:   "status",
		Short: "Show which fixtures are captured from this Mac and which are synthetic",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := out
			if dir == "" {
				dir = defaultFixtureDir()
			}
			m := loadManifest(dir)
			names := make([]string, 0, len(m.Fixtures))
			for n := range m.Fixtures {
				names = append(names, n)
			}
			sort.Strings(names)
			t := table.New([]table.Col{{Name: "primitive", Type: table.Str}, {Name: "source", Type: table.Str}})
			for _, n := range names {
				t.Append(n, m.Fixtures[n])
			}
			return render.Write(os.Stdout, flagFormat, []render.Item{{Label: dir, Table: t}})
		},
	}
	status.Flags().StringVar(&out, "dir", "", "fixture directory")
	cmd.AddCommand(capture, status)
	return cmd
}

func defaultFixtureDir() string {
	for _, c := range []string{"tests/fixtures", "sysq/tests/fixtures"} {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return filepath.Join(flagDirOrDefault(), "fixtures")
}

func flagDirOrDefault() string {
	if flagDir != "" {
		return flagDir
	}
	env, err := setup.Open(setup.Options{})
	if err != nil {
		return "."
	}
	return env.Dir
}

func loadManifest(dir string) setup.Manifest {
	m := setup.Manifest{Fixtures: map[string]string{}}
	if data, err := os.ReadFile(filepath.Join(dir, "MANIFEST.yaml")); err == nil {
		_ = yaml.Unmarshal(data, &m)
	}
	if m.Fixtures == nil {
		m.Fixtures = map[string]string{}
	}
	return m
}
