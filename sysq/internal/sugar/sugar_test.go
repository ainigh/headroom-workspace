package sugar

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

var update = flag.Bool("update", false, "rewrite lowered goldens")

// One golden per sugar form in design §18 (plus the sysq-script statement
// forms of §14.2). The right side is exactly what explain shows.
var forms = []struct{ name, in, want string }{
	{"pipeline", `a | filter cpu > 30 | limit 5`, `a.filter(expr("cpu > 30")).limit(5)`},
	{"filter_pred", `t | filter size > min and name contains "cache"`, `t.filter(expr("size > min and name contains \"cache\""))`},
	{"select", `t | select a, b as c, x*2 as y`, `t.select(expr("a, b as c, x*2 as y"))`},
	{"sort", `t | sort by f desc, g`, `t.sort(expr("f desc, g"))`},
	{"group", `t | group by f { count() as n }`, `t.group(expr("f"), expr("count() as n"))`},
	{"group_all", `t | group {} { sum(size) as bytes }`, `t.group(expr(""), expr("sum(size) as bytes"))`},
	{"filter_lambda", `t | filter(lambda r: r.cpu > 30)`, `t.filter(lambda r: scalar(r.cpu) > 30)`},
	{"durations", `[2s, 30m, 4h, 1d]`, `[Duration("2s"), Duration("30m"), Duration("4h"), Duration("1d")]`},
	{"bytes", `[1.5GB, 512MB, 4GiB]`, `[Bytes("1.5GB"), Bytes("512MB"), Bytes("4GiB")]`},
	{"path", `fs.list(~/Downloads)`, `fs.list(Path("~/Downloads"))`},
	{"path_root", `disk.usage(/Applications, depth: 1)`, `disk.usage(Path("/Applications"), depth=1)`},
	{"ago", `ago(2h)`, `(now() - Duration("2h"))`},
	{"column", `t.cpu`, `t.cpu`},
	{"column_scalar", `t.cpu[0] + t.cpu.max()`, `t.cpu[0] + t.cpu.max()`},
	{"fallback", `x = t ?? other`, `x = fallback(t, lambda: other)`},
	{"try", `x = try proc.info(pid)`, `x = try_(lambda: proc.info(pid))`},
	{"join", `a | join b on pid`, `a.join(b, on="pid")`},
	{"left_join", `a | left join b on pid`, `a.join(b, on="pid", how="left")`},
	{"anti_join", `a | anti join b on pid`, `a.join(b, on="pid", how="anti")`},
	{"join_trailing_kind", `a | join (b | limit 1) on pid left`, `a.join((b.limit(1)), on="pid", how="left")`},
	{"diff", `svc.launchd | diff snapshot("x") on label`, `svc.launchd.diff(snapshot("x"), on="label")`},
	{"fstring", `x = f"{n} processes over {th}%"`, `x = "{} processes over {}%".format(n, th)`},
	{"emit_label", `emit name: t | limit 3`, `emit(t.limit(3), label="name")`},
	{"assert", `assert count(t) > 0, "msg"`, `assert_(count(t) > 0, "msg")`},
	{"kwargs", `proc.top(by: "cpu", n: 20)`, `proc.top(by="cpu", n=20)`},
	{"keyword_kwargs", `watch(sys.memory, every: 2s, for: 30s)`, `watch(sys.memory, every=Duration("2s"), for_=Duration("30s"))`},
	{"set_ops", `a | union b | except c | intersect d | distinct pid`, `a.union(b).except_(c).intersect(d).distinct("pid")`},
	{"nest", `t | nest by user as procs | unnest procs`, `t.nest(by="user", as_="procs").unnest("procs")`},
	{"pivot", `t | pivot pid, name, value | unpivot cpu, mem`, `t.pivot("pid", "name", "value").unpivot("cpu", "mem")`},
	{"window", `t | window avg(free) over (order by t rows 5)`, `t.window(expr("avg(free) over (order by t rows 5)"))`},
	{"limit_offset", `t | limit 5 offset 10 | head 3 | tail 2 | sample 1`, `t.limit(5, offset=10).head(3).tail(2).sample(1)`},
	{"any_pred", `any(cpu, cpu > 80)`, `any(cpu, expr("cpu > 80"))`},
	{"scalar_compare", `if hot.level >= 2 { emit hot }`, "if scalar(hot.level) >= 2:\n  emit(hot)"},
	{"record", `emit { pid: p.pid, files: f | limit 10 }`, `emit({ "pid": p.pid, "files": f.limit(10) })`},
	{"binding", "let th = 50\nemit t | filter cpu > th", "th = 50\nemit(t.filter(expr(\"cpu > th\", th=th)))"},
	{"fn", "fn top(n) { proc.top(n: n) | limit n }\nemit top(3)", "def top(n):\n  return proc.top(n=n).limit(n)\nemit(top(3))"},
	{"else_if", "if a { emit 1 } else if b { emit 2 } else { emit 3 }", "if a:\n  emit(1)\nelif b:\n  emit(2)\nelse:\n  emit(3)"},
	{"for", "for p in t | limit 2 { emit p.pid }", "for p in t.limit(2):\n  emit(p.pid)"},
	{"continuation", "let x = a | union b\n  | distinct pid\nemit x", "x = a.union(b).distinct(\"pid\")\nemit(x)"},
	{"auto_emit", `proc.top(by: "cpu") | filter cpu > 30 | limit 5`, `proc.top(by="cpu").filter(expr("cpu > 30")).limit(5)`},
}

func TestForms(t *testing.T) {
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			got, err := Lower(f.in, Script)
			if err != nil {
				t.Fatal(err)
			}
			got = strings.TrimSpace(got)
			want := f.want
			if !strings.Contains(f.in, "\n") && !strings.HasPrefix(f.in, "emit") && !strings.HasPrefix(f.in, "x =") && !strings.HasPrefix(f.in, "if") && !strings.HasPrefix(f.in, "for") && !strings.HasPrefix(f.in, "assert") {
				want = "emit(" + want + ")"
			}
			if got != want {
				t.Fatalf("\n in:   %s\n got:  %s\n want: %s", f.in, got, want)
			}
			if _, err := parseStarlark(got); err != nil {
				t.Fatalf("lowered form is not valid Starlark: %v\n%s", err, got)
			}
		})
	}
}

func parseStarlark(src string) (*syntax.File, error) {
	opts := &syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}
	return opts.Parse("lowered.star", src, 0)
}

// Whole-file goldens: design §14.2, §16, §19 and the shipped recipes.
func TestFileGoldens(t *testing.T) {
	dir := filepath.Join("..", "..", "tests", "golden", "sugar")
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	n := 0
	for _, in := range files {
		ext := filepath.Ext(in)
		if ext != ".sysq" && ext != ".star" {
			continue
		}
		n++
		t.Run(filepath.Base(in), func(t *testing.T) {
			src, _ := os.ReadFile(in)
			mode := Script
			if ext == ".star" {
				mode = Star
			}
			got, err := Lower(string(src), mode)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseStarlark(got); err != nil {
				t.Fatalf("not valid Starlark: %v\n%s", err, got)
			}
			golden := strings.TrimSuffix(in, ext) + ".lowered"
			if *update {
				os.WriteFile(golden, []byte(got), 0o644)
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing %s (go test ./internal/sugar -update)", golden)
			}
			if !bytes.Equal([]byte(got), want) {
				t.Fatalf("lowering changed:\n%s", got)
			}
		})
	}
	if n < 3 {
		t.Fatal("expected the design-doc goldens")
	}
}

func TestErrors(t *testing.T) {
	for _, bad := range []string{
		`t | filter`,
		`t | filter cpu >`,
		`t | sort cpu`,
		`t | group by f`,
		`t | group by f { cpu }`,
		`t | join b`,
		`t | limit`,
		`x = 5parsecs`,
		`t | filter nosuchfn(cpu)`,
		`if x { emit 1`,
	} {
		if _, err := Lower(bad, Script); err == nil {
			t.Errorf("%q lowered without error", bad)
		}
	}
}

func TestDetect(t *testing.T) {
	if Detect("def run():\n  return 1\n") != Star || Detect("let x = 1\nemit x") != Script || Detect(`proc.top | limit 3`) != Script {
		t.Fatal("detect")
	}
}

func FuzzLower(f *testing.F) {
	for _, s := range forms {
		f.Add(s.in)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = Lower(s, Script)
		_, _ = Lower(s, Star)
	})
}
