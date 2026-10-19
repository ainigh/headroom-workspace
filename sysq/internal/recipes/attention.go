package recipes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/expr"
	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

var severityRank = map[string]int{"info": 0, "low": 1, "medium": 2, "high": 3, "critical": 4}

// FindingsSchema is the attention() table.
var FindingsSchema = []table.Col{
	{Name: "rule", Type: table.Str},
	{Name: "severity", Type: table.Enum, Values: []string{"info", "low", "medium", "high", "critical"}},
	{Name: "message", Type: table.Str},
	{Name: "subject", Type: table.Str},
	{Name: "detail", Type: table.TableT},
}

var placeholderRe = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)\}`)

// Message fills {column} placeholders from a row, humanized.
func Message(tmpl string, t *table.Table, row []any) string {
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := m[1 : len(m)-1]
		i := t.ColIndex(name)
		if i < 0 {
			return m
		}
		c := t.Schema[i]
		s := table.Humanize(table.Col{Type: c.Type}, row[i])
		return s
	})
}

// Attention runs every rule recipe at or above minSeverity and returns the
// matching rows as findings (design §7, §19.4).
func (lib *Library) Attention(rt *script.Runtime, minSeverity string) (*table.Table, error) {
	out := table.New(FindingsSchema)
	min := severityRank[minSeverity]
	for _, r := range lib.Rules() {
		if severityRank[r.Rule.Severity] < min || r.Unavailable != "" {
			continue
		}
		if rt.Engine.Allowed(r.Sensitivity, rt.CallOpts) != nil {
			continue
		}
		v, err := lib.Call(rt, r.Name, nil)
		if err != nil {
			if strings.Contains(err.Error(), "budget") || strings.Contains(err.Error(), "too many steps") {
				return nil, err
			}
			out.Append(r.Name, "info", "rule failed: "+err.Error(), "", nil)
			continue
		}
		items, err := rt.ToItems("", v)
		if err != nil || len(items) == 0 {
			continue
		}
		t := items[0].Table
		if r.Rule.Baseline != "" {
			before, err := loadSnapshot(rt, "baseline."+r.Rule.Baseline)
			if err != nil {
				continue // no baseline captured yet
			}
			key := []string{t.Schema[0].Name}
			d, err := t.Diff(before, key)
			if err != nil {
				continue
			}
			t = d
		}
		when := r.Rule.When
		if when == "" && r.Rule.Baseline != "" {
			when = `change == "added"`
		}
		rows := t.Rows
		if when != "" {
			n, err := expr.Parse(when)
			if err != nil {
				out.Append(r.Name, "info", "bad when: "+err.Error(), "", nil)
				continue
			}
			env := &expr.Env{Now: engineNow(rt), Home: rt.Engine.Home}
			f, err := expr.Filter(t, n, env)
			if err != nil {
				out.Append(r.Name, "info", "when failed: "+err.Error(), "", nil)
				continue
			}
			rows = f.Rows
		}
		for i, row := range rows {
			if i >= 20 {
				break
			}
			msg := r.Doc
			if r.Rule.Message != "" {
				msg = Message(r.Rule.Message, t, row)
			}
			subject := ""
			if len(row) > 0 {
				subject = table.Format(row[0])
			}
			out.Append(r.Name, r.Rule.Severity, msg, subject, t.WithRows([][]any{row}))
		}
		out.MergeMeta(t)
	}
	out.Meta.Error = ""
	out.SortStable(func(a, b []any) bool { return severityRank[a[1].(string)] > severityRank[b[1].(string)] })
	return out.Fix(), nil
}

func engineNow(rt *script.Runtime) time.Time {
	if rt.Engine.Now != nil {
		return rt.Engine.Now()
	}
	return time.Now()
}

func loadSnapshot(rt *script.Runtime, name string) (*table.Table, error) {
	if rt.SnapshotDir == "" {
		return nil, fmt.Errorf("no snapshots")
	}
	data, err := os.ReadFile(filepath.Join(rt.SnapshotDir, name+".json"))
	if err != nil {
		return nil, err
	}
	var t table.Table
	return &t, json.Unmarshal(data, &t)
}

// CaptureBaselines runs each baseline rule's recipe and stores its output as
// snapshot "baseline.<baseline>" (sysq baseline capture).
func (lib *Library) CaptureBaselines(rt *script.Runtime) ([]string, error) {
	var done []string
	for _, r := range lib.Rules() {
		if r.Rule.Baseline == "" || r.Unavailable != "" {
			continue
		}
		v, err := lib.Call(rt, r.Name, nil)
		if err != nil {
			return done, err
		}
		items, err := rt.ToItems("", v)
		if err != nil || len(items) == 0 {
			continue
		}
		data, err := json.Marshal(items[0].Table)
		if err != nil {
			return done, err
		}
		if err := os.MkdirAll(rt.SnapshotDir, 0o700); err != nil {
			return done, err
		}
		if err := os.WriteFile(filepath.Join(rt.SnapshotDir, "baseline."+r.Rule.Baseline+".json"), data, 0o600); err != nil {
			return done, err
		}
		done = append(done, r.Rule.Baseline)
	}
	return done, nil
}
