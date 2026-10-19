package recipes

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/testenv"
)

type binding struct {
	Recipe string         `yaml:"recipe"`
	Column string         `yaml:"column"`
	Value  string         `yaml:"value"`
	Params map[string]any `yaml:"params"`
}

// M4 done-when: every group Headroom ships (headroom-spec §4) is backed by
// recipes that run and produce the columns groups.yaml names. Skipped when
// sysq is built without the Headroom checkout next to it.
func TestHeadroomGroupBindings(t *testing.T) {
	cfg := filepath.Join(testenv.Root(), "..", "headroom", "config")
	data, err := os.ReadFile(filepath.Join(cfg, "groups.yaml"))
	if err != nil {
		t.Skip("no headroom/config next to sysq")
	}
	var groups struct {
		Overview    string         `yaml:"overview"`
		ItemProfile string         `yaml:"item_profile"`
		Explorer    map[string]any `yaml:"explorer"`
		Groups      []struct {
			ID        string  `yaml:"id"`
			Headline  binding `yaml:"headline"`
			Detail    binding `yaml:"detail"`
			Consumers binding `yaml:"consumers"`
			Sparkline binding `yaml:"sparkline"`
			Attention string  `yaml:"attention"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(data, &groups); err != nil {
		t.Fatal(err)
	}
	lib, e := shipped(t)
	run := func(t *testing.T, b binding, col string) {
		t.Helper()
		items, err := runAny(lib, e, t.TempDir(), b.Recipe, b.Params)
		if err != nil {
			t.Fatalf("%s: %v", b.Recipe, err)
		}
		if len(items) == 0 {
			t.Fatalf("%s: no output", b.Recipe)
		}
		if col != "" && items[0].Table.ColIndex(col) < 0 {
			t.Fatalf("%s has no column %q (has %v)", b.Recipe, col, items[0].Table.Schema)
		}
	}
	if len(groups.Groups) < 8 {
		t.Fatalf("expected the 8 spec groups, got %d", len(groups.Groups))
	}
	for _, g := range groups.Groups {
		g := g
		t.Run(g.ID, func(t *testing.T) {
			run(t, g.Headline, g.Headline.Column)
			run(t, g.Detail, "")
			run(t, g.Consumers, g.Consumers.Value)
			run(t, binding{Recipe: g.Sparkline.Recipe}, g.Sparkline.Column)
			run(t, binding{Recipe: g.Attention}, "status")
		})
	}
	t.Run("overview", func(t *testing.T) { run(t, binding{Recipe: groups.Overview}, "") })
	t.Run("item_profile", func(t *testing.T) {
		run(t, binding{Recipe: groups.ItemProfile, Params: map[string]any{"kind": "process", "id": "1"}}, "")
	})
	// Storage Explorer bindings: each recipe runs with its fixed params.
	for key, v := range groups.Explorer {
		var list []any
		switch x := v.(type) {
		case map[string]any:
			list = []any{x}
		case []any:
			if key != "suggestions" {
				continue
			}
			list = x
		default:
			continue
		}
		for _, item := range list {
			m, _ := item.(map[string]any)
			name, _ := m["recipe"].(string)
			params, _ := m["params"].(map[string]any)
			t.Run("explorer/"+key+"/"+name, func(t *testing.T) { run(t, binding{Recipe: name, Params: params}, "") })
		}
	}
	adata, err := os.ReadFile(filepath.Join(cfg, "assistant.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var asst struct {
		Preflight string `yaml:"preflight_recipe"`
	}
	if err := yaml.Unmarshal(adata, &asst); err != nil {
		t.Fatal(err)
	}
	t.Run("preflight", func(t *testing.T) {
		run(t, binding{Recipe: asst.Preflight, Params: map[string]any{"memory": float64(5e9)}}, "fits")
	})
	mdata, err := os.ReadFile(filepath.Join(cfg, "modes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var modes struct {
		Modes map[string]struct {
			Groups map[string]struct {
				Recipes []string `yaml:"recipes"`
			} `yaml:"groups"`
		} `yaml:"modes"`
	}
	if err := yaml.Unmarshal(mdata, &modes); err != nil {
		t.Fatal(err)
	}
	for mode, m := range modes.Modes {
		for gid, g := range m.Groups {
			for _, r := range g.Recipes {
				_, isRecipe := lib.Get(r)
				_, isPrim := e.Catalog.Get(r)
				if !isRecipe && !isPrim {
					t.Errorf("modes.yaml %s/%s names %s, which does not exist", mode, gid, r)
				}
			}
		}
	}
}

func runAny(lib *Library, e *engine.Engine, snap, name string, params map[string]any) ([]script.Item, error) {
	if _, ok := lib.Get(name); ok {
		return lib.Run(context.Background(), Env{Engine: e, SnapshotDir: snap}, name, params, engine.CallOpts{Ledger: &engine.Ledger{Budget: 5000}})
	}
	t, err := e.Primitive(context.Background(), name, params, engine.CallOpts{})
	if err != nil {
		return nil, err
	}
	return []script.Item{{Table: t}}, nil
}
