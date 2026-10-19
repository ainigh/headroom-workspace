package recipes

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ainigh/headroom-workspace/sysq"
	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/testenv"
)

func shipped(t *testing.T) (*Library, *engine.Engine) {
	t.Helper()
	e, err := testenv.Engine()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range e.Catalog.All() {
		if p.Network {
			e.Policy.NetworkPrimitives = append(e.Policy.NetworkPrimitives, p.Name)
		}
	}
	sub, _ := fs.Sub(sysq.Assets, "recipes")
	lib, err := Load(Options{Catalog: e.Catalog, Policy: e.Policy, Roots: []fs.FS{sub}})
	if err != nil {
		t.Fatalf("shipped library has errors: %v", err)
	}
	return lib, e
}

func TestShippedLibraryLoads(t *testing.T) {
	lib, _ := shipped(t)
	for _, n := range []string{"health.overview", "disk.hogs", "mem.headline", "attention.memory", "headroom.check", "item.profile"} {
		if _, ok := lib.Get(n); !ok {
			t.Errorf("missing %s", n)
		}
	}
	for _, r := range lib.All() {
		if r.Unavailable != "" {
			t.Errorf("%s unavailable: %s", r.Name, r.Unavailable)
		}
		if r.Cost == "" || r.Sensitivity == "" {
			t.Errorf("%s: facts not derived", r.Name)
		}
	}
	h, _ := lib.Get("disk.hogs")
	if h.Cost != "expensive" || h.Sensitivity != "personal" || strings.Join(h.Primitives, ",") != "disk.usage,fs.large" {
		t.Fatalf("disk.hogs facts: %+v", h)
	}
	ov, _ := lib.Get("attention.storage")
	if len(ov.Uses) != 1 || ov.Uses[0] != "disk.headline" {
		t.Fatalf("transitive uses: %v", ov.Uses)
	}
}

// Every shipped recipe runs against the fixtures with its default params.
func TestShippedRecipesRun(t *testing.T) {
	lib, e := shipped(t)
	for _, r := range lib.All() {
		r := r
		t.Run(r.Name, func(t *testing.T) {
			params := map[string]any{}
			for _, p := range r.Params {
				if p.Spec.Required {
					params[p.Name] = map[string]any{"cmd": "chrome", "id": "2210"}[p.Name]
				}
			}
			if r.Name == "history.trend" {
				t.Skip("needs sysqd history")
			}
			items, err := lib.Run(context.Background(), Env{Engine: e, SnapshotDir: t.TempDir()}, r.Name, params, engine.CallOpts{Ledger: &engine.Ledger{Budget: 5000}})
			if err != nil {
				t.Fatal(err)
			}
			if len(items) == 0 {
				t.Fatal("no output")
			}
		})
	}
}

func lib(t *testing.T, files map[string]string) (*Library, error) {
	e, _ := testenv.Engine()
	m := fstest.MapFS{}
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return Load(Options{Catalog: e.Catalog, Policy: e.Policy, Roots: []fs.FS{m}})
}

// Declaring cost or sensitivity is a load error (design §23.5).
func TestDeclaredFactsRefused(t *testing.T) {
	for _, field := range []string{`cost = "cheap"`, `sensitivity = "public"`} {
		_, err := lib(t, map[string]string{"x/y.star": "recipe(name = \"x.y\", doc = \"d\", " + field + ")\ndef run():\n  return sys.memory\n"})
		if err == nil || !strings.Contains(err.Error(), "derived") {
			t.Errorf("%s accepted: %v", field, err)
		}
	}
}

func TestCyclesRefused(t *testing.T) {
	l, err := lib(t, map[string]string{
		"a/a.star": "recipe(name = \"a.a\", doc = \"d\")\ndef run():\n  return b.b()\n",
		"b/b.star": "recipe(name = \"b.b\", doc = \"d\")\ndef run():\n  return a.a()\n",
		"c/c.star": "recipe(name = \"c.c\", doc = \"d\")\ndef run():\n  return sys.memory\n",
	})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle not reported: %v", err)
	}
	if _, ok := l.Get("a.a"); ok {
		t.Fatal("cyclic recipe callable")
	}
	if _, ok := l.Get("c.c"); !ok {
		t.Fatal("good recipe dropped")
	}
}

func TestProposedNeverCallable(t *testing.T) {
	l, _ := lib(t, map[string]string{
		"proposed/p.star": "recipe(name = \"x.p\", doc = \"d\")\ndef run():\n  return sys.memory\n",
	})
	if _, ok := l.Get("x.p"); ok {
		t.Fatal("proposed recipe is callable")
	}
	e, _ := testenv.Engine()
	if _, err := l.Run(context.Background(), Env{Engine: e}, "x.p", nil, engine.CallOpts{}); err == nil || !strings.Contains(err.Error(), "promote") {
		t.Fatalf("got %v", err)
	}
}

func TestProposeAndPromote(t *testing.T) {
	l, e := shipped(t)
	dir := t.TempDir()
	if _, err := l.Propose(dir, Proposal{Name: "proc.chrome", Doc: "Chrome processes", Script: `proc.list | filter name contains "Chrome"`, Extra: map[string]any{"cost": "cheap"}}); err == nil {
		t.Fatal("proposal declaring cost accepted")
	}
	path, err := l.Propose(dir, Proposal{Name: "proc.chrome", Doc: "Chrome processes", Script: `proc.list | filter name contains "Chrome"`})
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := fs.Sub(sysq.Assets, "recipes")
	l2, err := Load(Options{Catalog: e.Catalog, Policy: e.Policy, Roots: []fs.FS{sub, os.DirFS(dir)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l2.Get("proc.chrome"); ok {
		t.Fatal("proposal callable before promotion")
	}
	if _, ok := l2.Proposed("proc.chrome"); !ok {
		t.Fatalf("proposal not loaded from %s", path)
	}
	dst, err := l2.Promote(dir, "proc.chrome", nil)
	if err != nil {
		t.Fatal(err)
	}
	l3, _ := Load(Options{Catalog: e.Catalog, Policy: e.Policy, Roots: []fs.FS{sub, os.DirFS(dir)}})
	if _, ok := l3.Get("proc.chrome"); !ok {
		t.Fatalf("promoted recipe %s not callable", filepath.Base(dst))
	}
}

func TestAttention(t *testing.T) {
	l, e := shipped(t)
	rt := l.Options(Env{Engine: e, SnapshotDir: t.TempDir()}, engine.CallOpts{Ledger: &engine.Ledger{}})
	_ = rt
	tb, err := l.RunAttention(context.Background(), Env{Engine: e, SnapshotDir: t.TempDir()}, "", engine.CallOpts{Ledger: &engine.Ledger{}})
	if err != nil {
		t.Fatal(err)
	}
	// Fixture boot volume has 93 GB free (amber below 50 GB → ok); memory pressure 42% → ok.
	for _, r := range tb.Rows {
		if r[0] == "attention.memory" {
			t.Fatalf("memory flagged: %v", r)
		}
	}
}
