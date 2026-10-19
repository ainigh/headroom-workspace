package protect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShippedRules(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	os.MkdirAll(filepath.Join(home, "Downloads"), 0o755)
	os.Symlink("/usr/lib", filepath.Join(home, "Downloads", "sys"))
	r, err := Load("", home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range r.Exact {
		if e == "" {
			t.Fatal("an exact rule parsed as empty (YAML ~ is null: quote it)")
		}
	}
	for _, bad := range []string{"/", "~", "~/Documents", "~/Library", "/Applications", "/System/Library/Fonts", "/usr/local/bin/x",
		"~/.ssh/id_ed25519", "~/Downloads/sys", "~/Downloads/sys/Fonts", "~/Library/Application Support/sysact/host.key", "relative/path"} {
		if _, err := r.Check(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	for _, ok := range []string{"~/Downloads/big.zip", "~/Library/Caches/com.example.app", "~/Library/Developer/Xcode/DerivedData", "/Applications/Some.app"} {
		if _, err := r.Check(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	user := filepath.Join(home, "extra.yaml")
	os.WriteFile(user, []byte("version: 1\ntrees: [\"~/Projects\"]\n"), 0o644)
	r2, _ := Load(user, home)
	if _, err := r2.Check("~/Projects/x"); err == nil {
		t.Fatal("user rule ignored")
	}
	if _, err := r2.Check("/"); err == nil {
		t.Fatal("user file removed a shipped rule")
	}
}
