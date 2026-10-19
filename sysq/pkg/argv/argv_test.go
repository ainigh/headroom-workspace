package argv

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func f(v float64) *float64 { return &v }

func TestSubstitutionIsOneElement(t *testing.T) {
	specs := map[string]Spec{"q": {Type: TStr}}
	vals, err := Validate(specs, map[string]any{"q": "a b; rm -rf ~ $(x) `y` | z"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Build([]Element{{Text: "/bin/echo"}, {Text: "{q}"}}, vals)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, []string{"/bin/echo", "a b; rm -rf ~ $(x) `y` | z"}) {
		t.Fatalf("value was split or altered: %q", out)
	}
}

func TestEmbeddedPlaceholder(t *testing.T) {
	specs := map[string]Spec{"min": {Type: TBytes, Render: "find_k"}}
	vals, err := Validate(specs, map[string]any{"min": "1MiB"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Build([]Element{{Text: "-size"}, {Text: "{min}"}}, vals)
	if err != nil || out[1] != "+1024k" {
		t.Fatalf("%v %q", err, out)
	}
}

func TestOptionalGroups(t *testing.T) {
	specs := map[string]Spec{"glob": {Type: TStr}, "n": {Type: TInt, Default: 5}}
	tmpl := []Element{{Text: "/x"}, {Group: []string{"-name", "{glob}"}}, {Text: "-n{n}"}}
	vals, _ := Validate(specs, map[string]any{}, nil)
	out, err := Build(tmpl, vals)
	if err != nil || !reflect.DeepEqual(out, []string{"/x", "-n5"}) {
		t.Fatalf("%v %q", err, out)
	}
	vals, _ = Validate(specs, map[string]any{"glob": "*.log"}, nil)
	out, _ = Build(tmpl, vals)
	if !reflect.DeepEqual(out, []string{"/x", "-name", "*.log", "-n5"}) {
		t.Fatalf("%q", out)
	}
}

func TestArgumentInjectionRefused(t *testing.T) {
	specs := map[string]Spec{"model": {Type: TStr}}
	vals, err := Validate(specs, map[string]any{"model": "--delete"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build([]Element{{Text: "/bin/x"}, {Text: "{model}"}}, vals); !errors.Is(err, ErrInvalid) {
		t.Fatalf("leading dash accepted: %v", err)
	}
	specs["model"] = Spec{Type: TStr, AllowDash: true}
	vals, _ = Validate(specs, map[string]any{"model": "-x"}, nil)
	if _, err := Build([]Element{{Text: "{model}"}}, vals); err != nil {
		t.Fatal(err)
	}
}

func TestValidation(t *testing.T) {
	specs := map[string]Spec{
		"by":  {Type: TEnum, Values: []string{"cpu", "mem"}, Default: "cpu"},
		"n":   {Type: TInt, Default: 20, Max: f(200), Min: f(1)},
		"m":   {Type: TStr, Pattern: "^[a-z0-9:.-]+$"},
		"dur": {Type: TDuration},
	}
	cases := []map[string]any{
		{"by": "disk"},
		{"n": 201},
		{"n": 0},
		{"n": "12; ls"},
		{"m": "Gemma 27b"},
		{"m": "a\x00b"},
		{"zzz": 1},
		{"dur": "soon"},
	}
	for _, c := range cases {
		if _, err := Validate(specs, c, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v accepted", c)
		}
	}
	vals, err := Validate(specs, map[string]any{"dur": "2h", "n": "7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if vals["by"].Text != "cpu" || vals["n"].Text != "7" || vals["dur"].Text != "7200" {
		t.Fatalf("%+v", vals)
	}
}

func TestPredicateMustBeStructured(t *testing.T) {
	specs := map[string]Spec{"p": {Type: TPredicate, Dialect: "nspredicate"}}
	if _, err := Validate(specs, map[string]any{"p": `process == "x"`}, nil); err == nil {
		t.Fatal("raw predicate text accepted")
	}
}

func TestRootPolicy(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.MkdirAll(filepath.Join(home, "Downloads"), 0o700)
	os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "Downloads", "sneaky"))
	home, _ = filepath.EvalSymlinks(home)
	pol := RootPolicy{Home: home, Allowed: []string{"~", "/Applications"}, Excluded: []string{"~/.ssh"}}
	for _, bad := range []string{"~/.ssh", "~/.ssh/id_ed25519", "/etc/passwd", "relative", "~/Downloads/sneaky", "~/../../etc"} {
		if _, err := pol.CheckPath(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	got, err := pol.CheckPath("~/Downloads/../Downloads")
	if err != nil || got != filepath.Join(home, "Downloads") {
		t.Fatalf("%v %s", err, got)
	}
}

func FuzzValidateBuild(f *testing.F) {
	f.Add("hello", 3)
	f.Add("-rf", 0)
	f.Add("a\x00", -1)
	specs := map[string]Spec{"s": {Type: TStr}, "n": {Type: TInt, Max: func() *float64 { v := 100.0; return &v }()}}
	f.Fuzz(func(t *testing.T, s string, n int) {
		vals, err := Validate(specs, map[string]any{"s": s, "n": n}, nil)
		if err != nil {
			return
		}
		out, err := Build([]Element{{Text: "/bin/x"}, {Text: "{s}"}, {Text: "{n}"}}, vals)
		if err != nil {
			return
		}
		if len(out) != 3 || out[1] != s {
			t.Fatalf("value altered: %q", out)
		}
		if len(s) > 0 && s[0] == '-' {
			t.Fatal("leading dash passed")
		}
	})
}

func TestWithinRoots(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	if err := os.MkdirAll(filepath.Join(home, "Library", "Logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "Documents"), 0o700); err != nil {
		t.Fatal(err)
	}
	pol := RootPolicy{Home: home, Allowed: []string{"~"}}
	specs := map[string]Spec{"path": {Type: TPath, Within: []string{"~/Library/Logs", "/private/var/log"}}}
	for _, bad := range []string{"~/Library/Logs/../../Documents/x", "~/Documents/x", "~/Library/LogsX/y"} {
		if _, err := Validate(specs, map[string]any{"path": bad}, pol); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", bad, err)
		}
	}
	v, err := Validate(specs, map[string]any{"path": "~/Library/Logs/app.log"}, pol)
	if err != nil || v["path"].Text != filepath.Join(home, "Library/Logs/app.log") {
		t.Fatalf("%v %+v", err, v)
	}
}

func TestFindMtimeRender(t *testing.T) {
	v, err := Validate(map[string]Spec{"since": {Type: TDuration, Render: "find_mtime"}}, map[string]any{"since": "2h"}, nil)
	if err != nil || v["since"].Text != "-7200s" {
		t.Fatalf("%v %+v", err, v)
	}
}
