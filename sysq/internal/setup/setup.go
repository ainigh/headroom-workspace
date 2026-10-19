// Package setup assembles sysq's runtime (policy, catalog overlay, sandbox,
// engine, audit) for the CLI and the daemon.
package setup

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ainigh/headroom-workspace/sysq"
	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/exec"
	"github.com/ainigh/headroom-workspace/sysq/internal/policy"
	"github.com/ainigh/headroom-workspace/sysq/internal/recipes"
	"github.com/ainigh/headroom-workspace/sysq/pkg/audit"
)

// Options select the data directory and fixture mode.
type Options struct {
	Dir      string // default: ~/Library/Application Support/sysq (or $SYSQ_HOME)
	Fixtures string // "" live; "builtin" = shipped fixtures; else a directory
}

// Env is an assembled runtime.
type Env struct {
	Dir         string
	Policy      policy.Policy
	PolicyError error
	Catalog     *catalog.Catalog
	CatalogErr  error
	Engine      *engine.Engine
	Audit       *audit.Log
	FixtureDir  string
	Recipes     *recipes.Library
	RecipesErr  error
	// FixtureEngine serves the shipped fixtures; used to derive recipe output
	// schemas by dry run without executing anything live.
	FixtureEngine *engine.Engine
}

// RecipeEnv returns what running recipes needs.
func (e *Env) RecipeEnv() recipes.Env {
	return recipes.Env{Engine: e.Engine, SnapshotDir: filepath.Join(e.Dir, "snapshots")}
}

// UserRecipeDir is where promoted and proposed recipes live.
func (e *Env) UserRecipeDir() string { return filepath.Join(e.Dir, "recipes") }

// Manifest describes a fixture directory.
type Manifest struct {
	CapturedAt string            `yaml:"captured_at"`
	Timezone   string            `yaml:"timezone"`
	Home       string            `yaml:"home"`
	Fixtures   map[string]string `yaml:"fixtures"`
}

// Open builds the environment. A broken user policy or catalog overlay is
// reported but never fatal: shipped defaults keep working.
func Open(o Options) (*Env, error) {
	dir := o.Dir
	if dir == "" {
		dir = policy.Dir()
	}
	env := &Env{Dir: dir, Audit: &audit.Log{Dir: filepath.Join(dir, "audit")}}
	env.Policy, env.PolicyError = policy.Load(filepath.Join(dir, "policy.yaml"))

	shipped, err := fs.Sub(sysq.Assets, "catalog")
	if err != nil {
		return nil, err
	}
	roots := []fs.FS{shipped}
	if st, err := os.Stat(filepath.Join(dir, "catalog")); err == nil && st.IsDir() {
		roots = append(roots, os.DirFS(filepath.Join(dir, "catalog")))
	}
	env.Catalog, env.CatalogErr = catalog.Load(roots...)
	if env.Catalog == nil {
		return nil, env.CatalogErr
	}
	profiles, err := fs.Sub(sysq.Assets, "sandbox")
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	scratch := filepath.Join(os.TempDir(), fmt.Sprintf("sysq-%d", os.Getuid()))
	e := &engine.Engine{
		Catalog: env.Catalog,
		Policy:  env.Policy,
		Home:    home,
		Runner:  &exec.Runner{Sandbox: &exec.Sandbox{Profiles: profiles, Home: home, Excluded: env.Policy.ExcludedPaths}, Scratch: scratch},
	}
	if o.Fixtures != "" {
		var fx fs.FS
		if o.Fixtures == "builtin" {
			fx, err = fs.Sub(sysq.Assets, "tests/fixtures")
			if err != nil {
				return nil, err
			}
		} else {
			fx = os.DirFS(o.Fixtures)
			env.FixtureDir = o.Fixtures
		}
		e.Fixtures = fx
		if data, err := fs.ReadFile(fx, "MANIFEST.yaml"); err == nil {
			var m Manifest
			if yaml.Unmarshal(data, &m) == nil {
				if m.Home != "" {
					e.Home = m.Home
				}
				if m.Timezone != "" {
					if l, err := time.LoadLocation(m.Timezone); err == nil {
						e.Loc = l
					}
				}
			}
		}
	}
	env.Engine = e

	fx, err := fs.Sub(sysq.Assets, "tests/fixtures")
	if err != nil {
		return nil, err
	}
	env.FixtureEngine = &engine.Engine{Catalog: env.Catalog, Policy: env.Policy, Home: "/Users/me", Fixtures: fx}
	shippedRecipes, err := fs.Sub(sysq.Assets, "recipes")
	if err != nil {
		return nil, err
	}
	rroots := []fs.FS{shippedRecipes}
	if st, err := os.Stat(env.UserRecipeDir()); err == nil && st.IsDir() {
		rroots = append(rroots, os.DirFS(env.UserRecipeDir()))
	}
	env.Recipes, env.RecipesErr = recipes.Load(recipes.Options{Catalog: env.Catalog, Policy: env.Policy, Roots: rroots})
	return env, nil
}
