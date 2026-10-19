package expr

import (
	"strings"
	"testing"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

func procs() *table.Table {
	t := table.New([]table.Col{
		{Name: "pid", Type: table.PID}, {Name: "command", Type: table.Str},
		{Name: "cpu", Type: table.Float, Unit: "%"}, {Name: "mem", Type: table.Bytes},
		{Name: "started", Type: table.TS}, {Name: "user", Type: table.Str},
	})
	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	t.Append(int64(1), "launchd", 0.1, int64(30e6), base.Add(-48*time.Hour), "root")
	t.Append(int64(412), "postgres", 2.5, int64(600e6), base.Add(-3*time.Hour), "_postgres")
	t.Append(int64(2210), "Google Chrome", 45.0, int64(2.9e9), base.Add(-90*time.Minute), "me")
	t.Append(int64(3320), "photoanalysisd", 98.2, int64(400e6), base.Add(-15*time.Minute), "me")
	t.Append(int64(1502), "ollama", 0.2, int64(22e9), base.Add(-13*time.Hour), "me")
	return t
}

var env = &Env{Now: time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC), Home: "/Users/me"}

func mustFilter(t *testing.T, src string, b map[string]any) *table.Table {
	t.Helper()
	n, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	e := *env
	e.Bindings = b
	out, err := Filter(procs(), n, &e)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFilter(t *testing.T) {
	cases := map[string]int{
		"cpu > 30":                                 2,
		"cpu > 30 and mem > 1GB":                   1,
		`command contains "chrome"`:                1,
		`command matches "^p"`:                     2,
		`user in ["root", "_postgres"]`:            2,
		`not user in ["root", "_postgres"]`:        3,
		`user not in ["me"]`:                       2,
		"started > ago(2h)":                        2,
		"now() - started > 12h":                    2,
		`command startswith "o" or pid == 1`:       2,
		"mem >= 22GB":                              1,
		"cpu > threshold":                          2,
		"coalesce(nothing, 3) == 3 and pid > 1000": 3,
	}
	for src, want := range cases {
		got := mustFilter(t, src, map[string]any{"threshold": 30.0, "nothing": nil})
		if got.Len() != want {
			t.Errorf("%s: got %d rows, want %d", src, got.Len(), want)
		}
	}
}

func TestUnknownColumnIsAnError(t *testing.T) {
	n, _ := Parse("cpux > 3")
	if _, err := Filter(procs(), n, env); err == nil || !strings.Contains(err.Error(), "unknown column") {
		t.Fatalf("got %v", err)
	}
}

func TestSelectSortGroup(t *testing.T) {
	items, err := ParseList("command, mem / 1GB as gb, cpu * 2 as double", "select")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Select(procs(), items, env)
	if err != nil {
		t.Fatal(err)
	}
	if s.Schema[1].Name != "gb" || s.Schema[1].Type != table.Float || s.Schema[2].Type != table.Float {
		t.Fatalf("schema %+v", s.Schema)
	}
	keys, _ := ParseList("cpu desc", "sort")
	sorted, _ := Sort(procs(), keys, env)
	if sorted.Get(0, "command") != "photoanalysisd" {
		t.Fatal("sort desc")
	}
	gk, _ := ParseList("user", "group")
	ga, _ := ParseList("count() as n, sum(mem) as mem, avg(cpu) as cpu, list(command) as cmds, max(mem) / 1GB as biggest", "group")
	g, err := Group(procs(), gk, ga, env)
	if err != nil {
		t.Fatal(err)
	}
	if g.Len() != 3 || g.Get(2, "n") != int64(3) || g.Schema[2].Type != table.Bytes {
		t.Fatalf("group: %+v %v", g.Schema, g.Rows)
	}
	if sub, ok := g.Get(2, "cmds").(*table.Table); !ok || sub.Len() != 3 {
		t.Fatal("list() aggregate")
	}
	empty, _ := Group(procs().WithRows(nil), nil, ga[:2], env)
	if empty.Len() != 1 || empty.Get(0, "n") != int64(0) {
		t.Fatal("aggregate over nothing")
	}
}

func TestWindow(t *testing.T) {
	ts := table.New([]table.Col{{Name: "t", Type: table.TS}, {Name: "free", Type: table.Bytes}})
	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		ts.Append(base.Add(time.Duration(i)*2*time.Second), int64(100+i*10))
	}
	items, err := ParseList("avg(free) over (order by t rows 3) as avg3, rate(free) over (order by t) as r", "window")
	if err != nil {
		t.Fatal(err)
	}
	w, err := ApplyWindow(ts, items, env)
	if err != nil {
		t.Fatal(err)
	}
	if w.Get(5, "avg3") != int64(140) || w.Get(1, "r") != 5.0 || w.Get(0, "r") != nil {
		t.Fatalf("%v", w.Rows)
	}
}

func TestPushdownRendering(t *testing.T) {
	fields := map[string]string{"process": "process", "message": "eventMessage", "level": "messageType"}
	n, _ := Parse(`process == "kernel" and message contains "pressure" and level == "error" and len(message) > 3`)
	pushed, residual := Split(n, "nspredicate", fields, nil)
	got, err := Pushable{Node: pushed}.Render("nspredicate", fields)
	if err != nil {
		t.Fatal(err)
	}
	want := `((process == "kernel" AND eventMessage CONTAINS[c] "pressure") AND messageType == error)`
	if got != want {
		t.Fatalf("got %s", got)
	}
	if residual == nil || !strings.Contains(String(residual), "len(message)") {
		t.Fatal("residual lost")
	}
	inj, _ := Parse(`process == "x\" OR 1==1 OR process == \"y"`)
	s, _ := Pushable{Node: inj}.Render("nspredicate", fields)
	if !strings.HasPrefix(s, `process == "x\" OR`) {
		t.Fatalf("not escaped: %s", s)
	}
	if _, err := (Pushable{Node: mustParse(`level == "x OR 1"`)}).Render("nspredicate", fields); err == nil {
		t.Fatal("bad level pushed")
	}
}

func mustParse(s string) Node {
	n, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return n
}

func TestTableOps(t *testing.T) {
	p := procs()
	conns := table.New([]table.Col{{Name: "pid", Type: table.PID}, {Name: "conns", Type: table.Int}})
	conns.Append(int64(2210), int64(42))
	conns.Append(int64(412), int64(3))
	j, err := p.Join(conns, []string{"pid"}, "left")
	if err != nil || j.Len() != 5 || j.Get(2, "conns") != int64(42) || j.Get(0, "conns") != nil {
		t.Fatalf("left join: %v %v", err, j.Rows)
	}
	in, _ := p.Join(conns, []string{"pid"}, "inner")
	anti, _ := p.Join(conns, []string{"pid"}, "anti")
	if in.Len() != 2 || anti.Len() != 3 {
		t.Fatal("inner/anti")
	}
	u := p.Head(2).Union(p.Tail(2))
	if u.Len() != 4 {
		t.Fatal("union")
	}
	d, _ := u.Distinct("user")
	if d.Len() != 3 {
		t.Fatalf("distinct %d", d.Len())
	}
	before := p.Head(3)
	after := p.Tail(3).Clone()
	after.Rows[0] = append([]any(nil), after.Rows[0]...)
	after.Rows[0][2] = 50.0
	diff, err := after.Diff(before, []string{"pid"})
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]int{}
	for i := range diff.Rows {
		changes[diff.Get(i, "change").(string)]++
	}
	if changes["added"] != 2 || changes["removed"] != 2 || changes["changed"] != 1 {
		t.Fatalf("diff %v", changes)
	}
	n, _ := p.Nest([]string{"user"}, "procs")
	if n.Len() != 3 {
		t.Fatal("nest")
	}
	un, _ := n.Unnest("procs")
	if un.Len() != 5 {
		t.Fatal("unnest")
	}
	up, _ := p.Unpivot("cpu")
	if up.Len() != 5 || up.Schema[len(up.Schema)-1].Name != "value" {
		t.Fatal("unpivot")
	}
	pv, err := up.Pivot("pid", "name", "value")
	if err != nil || pv.Len() != 5 || pv.Schema[1].Name != "cpu" {
		t.Fatalf("pivot %v", err)
	}
	if p.Limit(2, 1).Get(0, "pid") != int64(412) || p.Limit(2, 1).Meta.DroppedByLimit != 2 {
		t.Fatal("limit offset")
	}
	if p.Sample(2, 1).Len() != 2 {
		t.Fatal("sample")
	}
	if _, err := p.Except(conns); err == nil {
		t.Fatal("except with mismatched schemas")
	}
	ex, _ := p.Except(p.Head(1))
	is, _ := p.Intersect(p.Head(1))
	if ex.Len() != 4 || is.Len() != 1 {
		t.Fatal("except/intersect")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"cpu > 30", `a contains "x" and not b in [1,2]`, "sum(size) / 1GB", "~/x", "a ?? b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := Parse(s)
		if err != nil {
			return
		}
		_ = String(n)
		_, _ = Eval(n, &Env{Table: procs(), Row: procs().Rows[0]})
	})
}
