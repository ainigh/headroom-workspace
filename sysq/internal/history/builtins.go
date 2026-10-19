package history

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

// Register installs the history.* builtin primitives on an engine and
// returns the history(recipe, range) function for scripts.
func Register(e *engine.Engine, s *Store, series func() []Series) func(ctx context.Context, recipe, column string, rangeSec float64) (*table.Table, error) {
	loc := e.Loc
	e.RegisterBuiltin("history_events", func(ctx context.Context, p *catalog.Primitive, params map[string]any) (*table.Table, error) {
		rng := dur(params["range"], 2*time.Hour)
		kind, _ := params["kind"].(string)
		return s.Events(ctx, rng, kind)
	})
	e.RegisterBuiltin("history_narrative", func(ctx context.Context, p *catalog.Primitive, params map[string]any) (*table.Table, error) {
		var sr []Series
		if series != nil {
			sr = series()
		}
		return s.Narrative(ctx, dur(params["range"], 2*time.Hour), sr, loc)
	})
	e.RegisterBuiltin("history_consumers_at", func(ctx context.Context, p *catalog.Primitive, params map[string]any) (*table.Table, error) {
		at, err := ParseAt(params["at"].(string), s.now(), loc)
		if err != nil {
			return nil, err
		}
		return s.TableAt(ctx, params["recipe"].(string), at, dur(params["window"], 2*time.Minute))
	})
	return func(ctx context.Context, recipe, column string, rangeSec float64) (*table.Table, error) {
		return s.Query(ctx, recipe, column, time.Duration(rangeSec*float64(time.Second)), "")
	}
}

func dur(v any, def time.Duration) time.Duration {
	if f, ok := v.(float64); ok && f > 0 {
		return time.Duration(f * float64(time.Second))
	}
	return def
}

var clockRe = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)

// ParseAt reads "14:12" (today, or yesterday if in the future), RFC 3339, or a
// duration ago ("20m").
func ParseAt(s string, now time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.Local
	}
	if m := clockRe.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		n := now.In(loc)
		t := time.Date(n.Year(), n.Month(), n.Day(), h, mi, 0, 0, loc)
		if t.After(now) {
			t = t.Add(-24 * time.Hour)
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if strings.IndexAny(s, "smhd") > 0 {
		sec, err := argv.ParseDuration(s)
		if err == nil {
			return now.Add(-time.Duration(sec * float64(time.Second))), nil
		}
	}
	return time.Time{}, fmt.Errorf("at: use HH:MM, an RFC 3339 time, or a duration ago like 20m")
}
