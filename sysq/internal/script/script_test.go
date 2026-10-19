package script

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/testenv"
)

func opts(t *testing.T) Options {
	t.Helper()
	e, err := testenv.Engine()
	if err != nil {
		t.Fatal(err)
	}
	return Options{Engine: e, SnapshotDir: t.TempDir()}
}

func run(t *testing.T, src string) *Result {
	t.Helper()
	res, err := Run(context.Background(), src, opts(t))
	if err != nil {
		t.Fatalf("%v\nlowered:\n%s", err, res.Lowered)
	}
	return res
}

// design §16 runs end to end against fixtures (M1 "done when").
func TestDesign16(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(testenv.Root(), "tests", "golden", "sugar", "design_16.sysq"))
	if err != nil {
		t.Fatal(err)
	}
	res := run(t, string(src))
	labels := map[string]int{}
	for _, it := range res.Items {
		labels[it.Label] = it.Table.Len()
	}
	for _, l := range []string{"culprits", "memory", "thermal", "full_volumes"} {
		if _, ok := labels[l]; !ok {
			t.Fatalf("missing %s in %v", l, labels)
		}
	}
	if labels["culprits"] == 0 || labels["culprits"] > 8 {
		t.Fatalf("culprits rows %d", labels["culprits"])
	}
	cul := res.Items[0].Table
	if cul.ColIndex("conns") < 0 {
		t.Fatalf("join did not add conns: %v", cul.Schema)
	}
	if cul.Get(0, "command") != "photoanalysisd" {
		t.Fatalf("not sorted by cpu: %v", cul.Rows[0])
	}
	if labels["full_volumes"] == 0 {
		t.Fatal("91% full data volume should be listed")
	}
	if res.Cost == 0 || len(res.Source) < 4 {
		t.Fatalf("ledger not charged: %+v", res)
	}
}

func TestDesign14(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(testenv.Root(), "tests", "golden", "sugar", "design_14_2.sysq"))
	if err != nil {
		t.Fatal(err)
	}
	res := run(t, string(src))
	if len(res.Items) < 3 {
		t.Fatalf("items: %d", len(res.Items))
	}
	// Big hogs (cpu > 50) → first emit is the table; the for loop emits records with nested tables.
	if res.Items[0].Table.Len() != 1 {
		t.Fatalf("big: %v", res.Items[0].Table.Rows)
	}
	found := false
	for _, it := range res.Items {
		if it.Table.ColIndex("home_files") >= 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("record with nested table not emitted")
	}
}

func TestPushdown(t *testing.T) {
	res := run(t, `log.system(since: 1h) | filter process == "kernel" and message contains "pressure" | limit 10`)
	m := res.Items[0].Table.Meta
	if len(m.Pushdown) != 2 || !strings.Contains(m.Pushdown[0], `process == "kernel"`) {
		t.Fatalf("pushdown: %v", m.Pushdown)
	}
	if res.Items[0].Table.Len() != 1 {
		t.Fatalf("residual filter not applied: %d", res.Items[0].Table.Len())
	}
}

func TestFuelAndBudget(t *testing.T) {
	o := opts(t)
	o.Steps = 2000
	_, err := Run(context.Background(), "def f(n):\n  x = 0\n  for i in range(n):\n    x += i\n  return x\nemit(f(1000000))\n", o)
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("step limit: %v", err)
	}
	o = opts(t)
	o.CallOpts.Ledger = &engine.Ledger{Budget: 15}
	res, err := Run(context.Background(), "emit sys.memory\nemit proc.top(by: \"mem\")\nemit proc.top(by: \"cpu\")", o)
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("partial emits lost: %d", len(res.Items))
	}
	// try() never swallows budget exhaustion.
	o.CallOpts.Ledger = &engine.Ledger{Budget: 1}
	if _, err := Run(context.Background(), "emit sys.memory\nemit try proc.top", o); err == nil {
		t.Fatal("try swallowed budget")
	}
}

func TestNoRecursionNoWhile(t *testing.T) {
	for _, src := range []string{
		"def f(n):\n  return f(n)\nemit(f(1))\n",
		"x = 0\nwhile x < 3:\n  x += 1\n",
	} {
		if _, err := Run(context.Background(), src, opts(t)); err == nil {
			t.Errorf("accepted: %q", src)
		}
	}
}

func TestLoadRestricted(t *testing.T) {
	o := opts(t)
	o.RecipesFS = os.DirFS(t.TempDir())
	for _, m := range []string{"../x.star", "/etc/passwd", "x.txt", "../../tests/fixtures/sys.info.txt"} {
		if _, err := Run(context.Background(), `load("`+m+`", "x")`+"\nemit(1)\n", o); err == nil {
			t.Errorf("load %q allowed", m)
		}
	}
}

func TestTryAndFallback(t *testing.T) {
	res := run(t, "let a = try proc.info(pid: 99999999)\nlet b = proc.top | filter cpu > 1000\nemit b ?? proc.top | limit 2\nemit a")
	if res.Items[0].Table.Len() != 2 {
		t.Fatal("fallback")
	}
	_ = res
}

func TestSnapshotDiff(t *testing.T) {
	o := opts(t)
	if _, err := Run(context.Background(), `snapshot("svc", svc.launchd | limit 3)`, o); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), `svc.launchd | diff snapshot("svc") on label`, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Table.Len() != 4 {
		t.Fatalf("diff rows %d", res.Items[0].Table.Len())
	}
}

func TestWatchAndWindow(t *testing.T) {
	res := run(t, `watch(sys.memory, every: 2s, for: 10s) | select t, free | window avg(free) over (order by t rows 3) as smooth`)
	if res.Items[0].Table.Len() != 6 || res.Items[0].Table.ColIndex("smooth") < 0 {
		t.Fatalf("%v", res.Items[0].Table.Schema)
	}
}

func TestScalarsAndColumns(t *testing.T) {
	res := run(t, "let m = sys.memory\nemit m.free.max() > m.swap_used.max()\nemit m.total[0]\nemit scalar(m.pressure) == \"normal\"")
	if res.Items[0].Table.Rows[0][0] != true || res.Items[2].Table.Rows[0][0] != true {
		t.Fatalf("%v %v", res.Items[0].Table.Rows, res.Items[2].Table.Rows)
	}
}

func TestSensitivityAtPlanTime(t *testing.T) {
	o := opts(t)
	o.CallOpts.MaxSensitivity = "public"
	_, err := Run(context.Background(), "emit sys.memory\nemit proc.top", o)
	if err == nil || !strings.Contains(err.Error(), "sensitivity") {
		t.Fatalf("personal primitive ran for a public caller: %v", err)
	}
}
