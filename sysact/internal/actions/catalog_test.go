package actions

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ainigh/headroom-workspace/sysact"
)

func TestShippedCatalog(t *testing.T) {
	sub, _ := fs.Sub(sysact.Actions, "actions")
	c, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ai.ollama_stop", "ai.ollama_unload", "app.force_quit", "app.quit", "cache.clear", "disk.verify", "fs.trash", "mem.purge", "spotlight.rebuild", "tm.start_backup"}
	var got []string
	for _, a := range c.All() {
		got = append(got, a.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("catalog %v", got)
	}
	tr, _ := c.Get("fs.trash")
	if tr.Kind != KindTrash {
		t.Fatal("fs.trash must go through Finder")
	}
	for _, n := range []string{"mem.purge", "spotlight.rebuild"} {
		if a, _ := c.Get(n); !a.RequiresAdmin {
			t.Errorf("%s must require admin", n)
		}
	}
}

func TestRefusesShellsAndRm(t *testing.T) {
	for _, bin := range []string{"/bin/sh", "/bin/rm", "/usr/bin/osascript", "/usr/bin/env", "/usr/bin/sudo"} {
		m := fstest.MapFS{"x.yaml": {Data: []byte("name: x.y\ndoc: d\ntier: caution\neffect: e\nargv: [\"" + bin + "\", \"-c\", \"x\"]\n")}}
		if _, err := Load(m); err == nil {
			t.Errorf("%s accepted as an action binary", bin)
		}
	}
	m := fstest.MapFS{"x.yaml": {Data: []byte("name: x.y\ndoc: d\ntier: caution\neffect: e {nope}\nargv: [\"/usr/bin/true\"]\n")}}
	if _, err := Load(m); err == nil {
		t.Error("undeclared placeholder accepted")
	}
}
