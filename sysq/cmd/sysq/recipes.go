package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/plan"
	"github.com/ainigh/headroom-workspace/sysq/internal/recipes"
	"github.com/ainigh/headroom-workspace/sysq/internal/render"
	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/setup"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

func init() {
	describer = func(env *setup.Env) Describer { return libDescriber{env} }
	runName = func(ctx context.Context, env *setup.Env, name string, params map[string]any, opts engine.CallOpts) ([]render.Item, error) {
		if env.Recipes != nil {
			if _, ok := env.Recipes.Get(name); ok {
				items, err := env.Recipes.Run(ctx, env.RecipeEnv(), name, params, opts)
				return toRender(items), err
			}
			if _, ok := env.Recipes.Proposed(name); ok {
				return nil, fmt.Errorf("%s is proposed, not promoted; review it with: sysq recipe review %s", name, name)
			}
		}
		t, err := env.Engine.Primitive(ctx, name, params, opts)
		if err != nil {
			return nil, err
		}
		return []render.Item{{Table: t}}, nil
	}
	scriptOptions = func(env *setup.Env, opts engine.CallOpts) script.Options {
		if env.Recipes == nil {
			return script.Options{Engine: env.Engine, CallOpts: opts, SnapshotDir: env.RecipeEnv().SnapshotDir}
		}
		return env.Recipes.Options(env.RecipeEnv(), opts)
	}
	resolver = func(env *setup.Env) plan.Resolver { return libResolver{env} }
	explainName = func(env *setup.Env, name string) error {
		r, ok := env.Recipes.Get(name)
		if !ok {
			r, ok = env.Recipes.Proposed(name)
		}
		if !ok {
			return fmt.Errorf("no recipe named %q", name)
		}
		p, err := plan.Analyze(r.Lowered, libResolver{env})
		if err != nil {
			return err
		}
		if s, err := env.Recipes.Schema(env.FixtureEngine, name); err == nil {
			p.Schema = s
		}
		return printPlan(p)
	}
	extraCommands = append(extraCommands, func(root *cobra.Command) {
		root.AddCommand(recipesCmd(), recipeCmd(), attentionCmd(), baselineCmd())
		addGenerated(root)
	})
}

func toRender(items []script.Item) []render.Item {
	out := make([]render.Item, len(items))
	for i, it := range items {
		out[i] = render.Item{Label: it.Label, Table: it.Table}
	}
	return out
}

type libResolver struct{ env *setup.Env }

func (r libResolver) Primitive(n string) (*catalog.Primitive, bool) { return r.env.Catalog.Get(n) }
func (r libResolver) Recipe(n string) (plan.RecipeFacts, bool) {
	if r.env.Recipes == nil {
		return plan.RecipeFacts{}, false
	}
	x, ok := r.env.Recipes.Get(n)
	if !ok {
		return plan.RecipeFacts{}, false
	}
	return plan.RecipeFacts{Cost: x.Cost, Sensitivity: x.Sensitivity, Primitives: x.Primitives, Units: x.Units, Network: x.Network}, true
}

type libDescriber struct{ env *setup.Env }

func (d libDescriber) Index() *table.Table {
	t := primDescriber(d).Index()
	if d.env.Recipes != nil {
		for _, r := range d.env.Recipes.All() {
			doc := r.Doc
			if r.Unavailable != "" {
				doc = "[unavailable: " + r.Unavailable + "] " + doc
			}
			t.Append(r.Name, "recipe", r.Cost, r.Sensitivity, doc)
		}
	}
	t.SortStable(func(a, b []any) bool { return a[0].(string) < b[0].(string) })
	return t
}

func (d libDescriber) One(name string) (any, error) {
	if d.env.Recipes != nil {
		r, ok := d.env.Recipes.Get(name)
		if !ok {
			r, ok = d.env.Recipes.Proposed(name)
		}
		if ok {
			v := r.Describe()
			if s, err := d.env.Recipes.Schema(d.env.FixtureEngine, name); err == nil {
				v["output"] = s
			}
			return v, nil
		}
	}
	return primDescriber(d).One(name)
}

func recipesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recipes [search]",
		Short: "List recipe groups, or search recipes by name, doc or tag",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				return render.Write(os.Stdout, flagFormat, []render.Item{{Table: env.Recipes.Search(args[0])}})
			}
			t := table.New([]table.Col{{Name: "group", Type: table.Str}, {Name: "recipes", Type: table.Int}, {Name: "names", Type: table.Str}})
			groups := env.Recipes.Groups()
			var keys []string
			for g := range groups {
				keys = append(keys, g)
			}
			sort.Strings(keys)
			for _, g := range keys {
				short := make([]string, len(groups[g]))
				for i, n := range groups[g] {
					short[i] = strings.TrimPrefix(n, g+".")
				}
				t.Append(g, int64(len(groups[g])), strings.Join(short, ", "))
			}
			if p := env.Recipes.ProposedNames(); len(p) > 0 {
				t.Append("proposed", int64(len(p)), strings.Join(p, ", "))
			}
			return render.Write(os.Stdout, flagFormat, []render.Item{{Table: t}})
		},
	}
}

func recipeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "recipe", Short: "Review, promote, propose and explain recipes"}
	review := &cobra.Command{
		Use:   "review [name]",
		Short: "Show a proposed recipe: lowered Starlark, derived cost/sensitivity, and a dry run",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				names = env.Recipes.ProposedNames()
				if len(names) == 0 {
					fmt.Println("no proposals waiting")
					return nil
				}
			}
			for _, n := range names {
				r, ok := env.Recipes.Proposed(n)
				if !ok {
					return fmt.Errorf("no proposal named %s", n)
				}
				fmt.Printf("══ %s (%s)\n%s\n", r.Name, r.File, r.Doc)
				b, _ := yaml.Marshal(map[string]any{"cost": r.Cost, "sensitivity": r.Sensitivity, "primitives": r.Primitives, "uses": r.Uses})
				fmt.Print(string(b))
				fmt.Println("── lowered")
				fmt.Print(r.Lowered)
				fmt.Println("── dry run")
				items, err := dryRunProposal(env, r)
				if err != nil {
					fmt.Println("error:", err)
				} else if err := render.Write(os.Stdout, flagFormat, toRender(items)); err != nil {
					return err
				}
				fmt.Printf("── promote with: sysq recipe promote %s\n\n", r.Name)
			}
			return nil
		},
	}
	promote := &cobra.Command{
		Use:   "promote <name>",
		Short: "Move a reviewed proposal into your recipe library (humans only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			r, ok := env.Recipes.Proposed(args[0])
			if !ok {
				return fmt.Errorf("no proposal named %s", args[0])
			}
			items, _ := dryRunProposal(env, r)
			dst, err := env.Recipes.Promote(env.UserRecipeDir(), args[0], items)
			if err != nil {
				return err
			}
			fmt.Println("promoted to", dst)
			return nil
		},
	}
	var doc, tags, file string
	propose := &cobra.Command{
		Use:   "propose <name> --doc '...' (-f script | script)",
		Short: "Save a script as a proposed recipe (not callable until promoted)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			src := ""
			if len(args) == 2 {
				src = args[1]
			}
			if file != "" {
				b, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				src = string(b)
			}
			p := recipes.Proposal{Name: args[0], Doc: doc, Script: src}
			if tags != "" {
				p.Tags = strings.Split(tags, ",")
			}
			out, err := env.Recipes.Propose(env.UserRecipeDir(), p)
			if err != nil {
				return err
			}
			fmt.Println("proposed:", out)
			return nil
		},
	}
	propose.Flags().StringVar(&doc, "doc", "", "one-line description")
	propose.Flags().StringVar(&tags, "tags", "", "comma-separated tags")
	propose.Flags().StringVarP(&file, "file", "f", "", "script file")
	explain := &cobra.Command{
		Use:   "explain <name>",
		Short: "Show a recipe's lowered code, plan and output schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			plan.SetNamespaces(env.Catalog.Namespaces())
			return explainName(env, args[0])
		},
	}
	cmd.AddCommand(review, promote, propose, explain)
	return cmd
}

// dryRunProposal runs a proposal's body (it is not callable through the
// library) in a fresh runtime.
func dryRunProposal(env *setup.Env, r *recipes.Recipe) ([]script.Item, error) {
	body := r.Lowered + "\nemit(run())\n"
	ctx, cancel := signalContext()
	defer cancel()
	opts := env.Recipes.Options(env.RecipeEnv(), engine.CallOpts{Ledger: &engine.Ledger{Budget: env.Policy.CostBudget}})
	opts.Timeout = 60 * time.Second
	res, err := script.RunLowered(ctx, body, opts)
	if res == nil {
		return nil, err
	}
	return res.Items, err
}

func attentionCmd() *cobra.Command {
	var sev string
	cmd := &cobra.Command{
		Use:   "attention",
		Short: "Run the attention rules and show findings",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			t, err := env.Recipes.RunAttention(ctx, env.RecipeEnv(), sev, engine.CallOpts{Ledger: &engine.Ledger{Budget: env.Policy.CostBudget * 4}})
			if err != nil {
				return err
			}
			view, _ := t.Project("severity", "rule", "message")
			if flagFormat == "json" {
				view = t
			}
			return render.Write(os.Stdout, flagFormat, []render.Item{{Table: view}})
		},
	}
	cmd.Flags().StringVar(&sev, "min", "", "minimum severity (info, low, medium, high, critical)")
	return cmd
}

func baselineCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "baseline", Short: "Baselines for drift rules"}
	cmd.AddCommand(&cobra.Command{
		Use:   "capture",
		Short: "Snapshot every baseline rule's current output (startup items, listeners, ...)",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			rt := script.NewRuntime(ctx, env.Recipes.Options(env.RecipeEnv(), engine.CallOpts{Ledger: &engine.Ledger{Budget: env.Policy.CostBudget}}))
			done, err := env.Recipes.CaptureBaselines(rt)
			if err != nil {
				return err
			}
			fmt.Println("captured:", strings.Join(done, ", "))
			return nil
		},
	})
	return cmd
}

// addGenerated adds `sysq <group> <name> --param value` for every recipe
// (design §19.2) and, below them, every primitive.
func addGenerated(root *cobra.Command) {
	env, err := setup.Open(setup.Options{Dir: dirFromArgs()})
	if err != nil || env.Recipes == nil {
		return
	}
	taken := map[string]bool{}
	for _, c := range root.Commands() {
		taken[c.Name()] = true
	}
	groups := map[string]*cobra.Command{}
	group := func(name string) *cobra.Command {
		if g, ok := groups[name]; ok {
			return g
		}
		g := &cobra.Command{Use: name, Short: name + " recipes and primitives"}
		groups[name] = g
		return g
	}
	add := func(full, doc string, params []recipes.Param, isPrimitive bool) {
		parts := strings.SplitN(full, ".", 2)
		if len(parts) != 2 || taken[parts[0]] {
			return
		}
		sub := strings.ReplaceAll(parts[1], ".", "-")
		g := group(parts[0])
		for _, c := range g.Commands() {
			if c.Name() == sub {
				return
			}
		}
		short := doc
		if isPrimitive {
			short = "(primitive) " + doc
		}
		c := &cobra.Command{Use: sub, Short: short, Args: cobra.NoArgs}
		if alias := strings.ReplaceAll(sub, "_", "-"); alias != sub {
			c.Aliases = []string{alias}
		}
		vals := map[string]*string{}
		bools := map[string]*bool{}
		for _, p := range params {
			flag := strings.ReplaceAll(p.Name, "_", "-")
			help := p.Spec.Doc
			if help == "" {
				help = p.Spec.Type
			}
			if len(p.Spec.Values) > 0 {
				help += " (" + strings.Join(p.Spec.Values, "|") + ")"
			}
			if p.Spec.Default != nil {
				help += fmt.Sprintf(" [default %v]", p.Spec.Default)
			}
			if p.Spec.Type == argv.TBool {
				bools[p.Name] = c.Flags().Bool(flag, false, help)
			} else if p.Spec.Type != argv.TPredicate {
				vals[p.Name] = c.Flags().String(flag, "", help)
			}
		}
		c.RunE = func(cmd *cobra.Command, args []string) error {
			params := map[string]any{}
			for n, v := range vals {
				if cmd.Flags().Changed(strings.ReplaceAll(n, "_", "-")) {
					params[n] = *v
				}
			}
			for n, v := range bools {
				if cmd.Flags().Changed(strings.ReplaceAll(n, "_", "-")) {
					params[n] = *v
				}
			}
			env, err := openEnv()
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			start := time.Now()
			ledger := &engine.Ledger{Budget: env.Policy.CostBudget}
			items, err := runName(ctx, env, full, params, engine.CallOpts{Ledger: ledger, Fresh: flagFresh})
			auditRun(env, "run", full, "", params, ledger, start, items, err)
			if err != nil {
				return err
			}
			return render.Write(os.Stdout, flagFormat, items)
		}
		g.AddCommand(c)
	}
	for _, r := range env.Recipes.All() {
		add(r.Name, r.Doc, r.Params, false)
	}
	for _, p := range env.Catalog.All() {
		var ps []recipes.Param
		for _, n := range p.ParamOrder {
			ps = append(ps, recipes.Param{Name: n, Spec: p.Params[n]})
		}
		add(p.Name, p.Doc, ps, true)
	}
	for _, g := range groups {
		root.AddCommand(g)
	}
}

func dirFromArgs() string {
	for i, a := range os.Args {
		if a == "--dir" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if strings.HasPrefix(a, "--dir=") {
			return strings.TrimPrefix(a, "--dir=")
		}
	}
	return ""
}
