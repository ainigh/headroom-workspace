package history

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// Series names a sampled column the narrative describes.
type Series struct {
	Recipe string  `yaml:"recipe" json:"recipe"`
	Column string  `yaml:"column" json:"column"`
	Label  string  `yaml:"label" json:"label"`
	Unit   string  `yaml:"unit" json:"unit"`   // "%", "bytes", "bytes/s", "" (plain)
	High   float64 `yaml:"high" json:"high"`   // "high" above this (0 = no high-water sentence)
	Low    float64 `yaml:"low" json:"low"`     // "low" below this (for free space; 0 = none)
	Trend  bool    `yaml:"trend" json:"trend"` // report net change (rose/fell)
}

// DefaultSeries describes the Headroom headline recipes.
var DefaultSeries = []Series{
	{Recipe: "mem.headline", Column: "pressure_pct", Label: "memory pressure", Unit: "%", High: 60},
	{Recipe: "mem.headline", Column: "swap_used", Label: "swap in use", Unit: "bytes", Trend: true},
	{Recipe: "cpu.headline", Column: "load_pct", Label: "CPU load", Unit: "%", High: 70},
	{Recipe: "gpu.headline", Column: "utilization", Label: "GPU utilization", Unit: "%", High: 80},
	{Recipe: "disk.headline", Column: "free", Label: "disk free", Unit: "bytes", Low: 50e9, Trend: true},
	{Recipe: "net.headline", Column: "throughput", Label: "network throughput", Unit: "bytes/s", High: 50e6},
	{Recipe: "thermal.headline", Column: "level", Label: "thermal level", High: 1},
	{Recipe: "ai.headline", Column: "memory_held", Label: "local model memory", Unit: "bytes", Trend: true},
}

func fmtVal(v float64, unit string) string {
	switch unit {
	case "%":
		return fmt.Sprintf("%.0f%%", v)
	case "bytes":
		return table.HumanBytes(int64(v))
	case "bytes/s":
		return table.HumanBytes(int64(v)) + "/s"
	}
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// Narrative turns recent history into a few plain sentences for model
// context (design §25): "memory pressure above 60% for 20 min; peaked at
// 85% at 14:12. disk free fell 12 GB (105 GB → 93 GB)."
func (s *Store) Narrative(ctx context.Context, rng time.Duration, series []Series, loc *time.Location) (*table.Table, error) {
	if len(series) == 0 {
		series = DefaultSeries
	}
	if loc == nil {
		loc = time.Local
	}
	out := table.New([]table.Col{{Name: "sentence", Type: table.Str}, {Name: "topic", Type: table.Str}})
	clock := func(t time.Time) string { return t.In(loc).Format("15:04") }
	for _, sr := range series {
		t, err := s.Query(ctx, sr.Recipe, sr.Column, rng, "")
		if err != nil {
			return nil, err
		}
		if t.Len() < 2 {
			continue
		}
		st, _ := s.Stats(ctx, sr.Recipe, sr.Column, rng)
		var parts []string
		if sr.High > 0 && st.Max >= sr.High {
			above := spanWhere(t, func(v float64) bool { return v >= sr.High })
			parts = append(parts, fmt.Sprintf("%s at or above %s for %s; peaked at %s at %s",
				sr.Label, fmtVal(sr.High, sr.Unit), table.HumanDuration(above.Seconds()), fmtVal(st.Max, sr.Unit), clock(st.MaxAt)))
		} else if sr.Low > 0 && st.Min <= sr.Low {
			below := spanWhere(t, func(v float64) bool { return v <= sr.Low })
			parts = append(parts, fmt.Sprintf("%s at or below %s for %s; lowest %s at %s",
				sr.Label, fmtVal(sr.Low, sr.Unit), table.HumanDuration(below.Seconds()), fmtVal(st.Min, sr.Unit), clock(st.MinAt)))
		}
		if sr.Trend {
			delta := st.Last - st.First
			rel := 0.0
			if st.First != 0 {
				rel = math.Abs(delta) / math.Abs(st.First)
			}
			if rel >= 0.05 && math.Abs(delta) > 0 {
				dir := "rose"
				if delta < 0 {
					dir = "fell"
				}
				parts = append(parts, fmt.Sprintf("%s %s %s (%s → %s)", sr.Label, dir, fmtVal(math.Abs(delta), sr.Unit), fmtVal(st.First, sr.Unit), fmtVal(st.Last, sr.Unit)))
			}
		}
		if len(parts) == 0 && sr.Unit == "%" {
			parts = append(parts, fmt.Sprintf("%s averaged %s (max %s)", sr.Label, fmtVal(st.Avg, sr.Unit), fmtVal(st.Max, sr.Unit)))
		}
		for _, p := range parts {
			out.Append(p, sr.Recipe)
		}
	}
	ev, err := s.Events(ctx, rng, "")
	if err != nil {
		return nil, err
	}
	n := ev.Len()
	start := 0
	if n > 8 {
		start = n - 8
	}
	for _, r := range ev.Rows[start:] {
		out.Append(fmt.Sprintf("%s %s", clock(r[0].(time.Time)), r[4]), "event")
	}
	if out.Len() == 0 {
		out.Append(fmt.Sprintf("no notable changes in the last %s", table.HumanDuration(rng.Seconds())), "summary")
	}
	out.Meta.Source = []string{"history.narrative"}
	out.Meta.Sensitivity = "personal"
	return out, nil
}

// spanWhere sums the time between consecutive samples where cond holds.
func spanWhere(t *table.Table, cond func(float64) bool) time.Duration {
	var total time.Duration
	for i := 1; i < t.Len(); i++ {
		v, ok := t.Rows[i-1][len(t.Rows[i-1])-1].(float64)
		if !ok || !cond(v) {
			continue
		}
		total += t.Rows[i][0].(time.Time).Sub(t.Rows[i-1][0].(time.Time))
	}
	return total
}

// Text joins a narrative table into one paragraph.
func Text(t *table.Table) string {
	var b []string
	for _, r := range t.Rows {
		b = append(b, table.Format(r[0]))
	}
	return strings.Join(b, ". ") + "."
}
