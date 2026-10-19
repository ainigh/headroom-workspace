package server

import (
	"context"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/ainigh/headroom-workspace/sysact"
	"github.com/ainigh/headroom-workspace/sysact/internal/actions"
	"github.com/ainigh/headroom-workspace/sysact/internal/protect"
	"github.com/ainigh/headroom-workspace/sysact/internal/run"

	"github.com/ainigh/headroom-workspace/sysact/internal/testhome"
)

type fakeRunner struct{ n *int }

func (f fakeRunner) Run(ctx context.Context, c run.Command) (run.Result, error) {
	*f.n++
	return run.Result{OK: true, Output: "ok"}, nil
}

var testHome string

func testServer(t *testing.T, dry bool) (*Server, *int) {
	t.Helper()
	home := testhome.New(t)
	testHome = home
	for _, d := range []string{"Library/Caches/com.example.app", "Downloads", ".ssh", "Library/Keychains", "Documents"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(home, "Downloads", "big.zip"), []byte("x"), 0o644)
	sub, _ := fs.Sub(sysact.Actions, "actions")
	cat, err := actions.Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := protect.Load("", home)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	s := New(Options{Catalog: cat, Protect: rules, Key: key, DryRun: dry, Home: home})
	n := 0
	s.runner = fakeRunner{&n}
	s.exists = func(string) bool { return true }
	me, _ := user.Current()
	s.lookup = func(ctx context.Context, pid int64) (ProcessInfo, error) {
		return ProcessInfo{Name: "Example", User: me.Username}, nil
	}
	return s, &n
}

func sampleParams(a *actions.Action) map[string]any {
	out := map[string]any{}
	for name, spec := range a.Params {
		switch {
		case spec.Type == "pid":
			out[name] = 4242
		case spec.Type == "path":
			out[name] = filepath.Join(testHome, "Downloads", "big.zip")
		case name == "model":
			out[name] = "gemma-27b"
		case name == "bundle_id":
			out[name] = "com.example.app"
		case name == "app":
			out[name] = "com.example.app"
		}
	}
	return out
}

func copyParams(p map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range p {
		out[k] = v
	}
	return out
}
