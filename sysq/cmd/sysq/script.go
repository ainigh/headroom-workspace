package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/plan"
	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/setup"
)

func init() {
	extraCommands = append(extraCommands, func(root *cobra.Command) { root.AddCommand(explainCmd()) })
}

// scriptOptions builds runtime options from the environment (recipes join in M2).
var scriptOptions = func(env *setup.Env, opts engine.CallOpts) script.Options {
	return script.Options{
		Engine:      env.Engine,
		CallOpts:    opts,
		SnapshotDir: filepath.Join(env.Dir, "snapshots"),
	}
}

// resolver returns the explain resolver (recipes join in M2).
var resolver = func(env *setup.Env) plan.Resolver { return plan.CatalogResolver{Catalog: env.Catalog} }

func runScript(ctx context.Context, env *setup.Env, src string, opts engine.CallOpts) (*script.Result, error) {
	return script.Run(ctx, src, scriptOptions(env, opts))
}

func explainCmd() *cobra.Command {
	var scriptText, scriptFile string
	cmd := &cobra.Command{
		Use:   "explain [-e '<script>' | -f file | <recipe>]",
		Short: "Show the lowered Starlark, primitives, derived cost and sensitivity, and pushdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			plan.SetNamespaces(env.Catalog.Namespaces())
			if scriptFile != "" {
				b, err := os.ReadFile(scriptFile)
				if err != nil {
					return err
				}
				scriptText = string(b)
			}
			if scriptText == "" {
				if len(args) != 1 {
					return fmt.Errorf("pass -e '<script>', -f file, or a recipe name")
				}
				return explainName(env, args[0])
			}
			p, err := plan.Explain(scriptText, resolver(env))
			if err != nil {
				return err
			}
			return printPlan(p)
		},
	}
	cmd.Flags().StringVarP(&scriptText, "script", "e", "", "script to explain")
	cmd.Flags().StringVarP(&scriptFile, "file", "f", "", "read the script from a file")
	return cmd
}

// explainName is replaced in M2 to explain recipes.
var explainName = func(env *setup.Env, name string) error {
	return fmt.Errorf("no recipe named %q", name)
}

func printPlan(p *plan.Plan) error {
	if flagFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}
	fmt.Println("── lowered")
	fmt.Print(p.Lowered)
	fmt.Println("── plan")
	view := map[string]any{
		"primitives": p.Primitives, "cost": p.Cost, "cost_units": p.Units, "sensitivity": p.Sensitivity,
	}
	if len(p.Recipes) > 0 {
		view["recipes"] = p.Recipes
	}
	if len(p.Pushdown) > 0 {
		view["pushdown"] = p.Pushdown
	}
	if p.Network {
		view["network"] = true
	}
	if len(p.Unknown) > 0 {
		view["unknown"] = p.Unknown
	}
	if p.Schema != nil {
		view["schema"] = p.Schema
	}
	b, _ := yaml.Marshal(view)
	_, err := os.Stdout.Write(b)
	return err
}
