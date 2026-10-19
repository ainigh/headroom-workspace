package history

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "history.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func scalar(v float64) *table.Table {
	tb := table.New([]table.Col{{Name: "load_pct", Type: table.Float}})
	tb.Append(v)
	return tb
}

func TestDownsampleKeepsBucketAveragesOncePartlyExpired(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	// One minute of raw samples: 0 for the first half, 100 for the second.
	for i := range 12 {
		v := 0.0
		if i >= 6 {
			v = 100
		}
		if err := s.Record(ctx, Sample{Recipe: "cpu.headline", TS: t0.Add(time.Duration(i) * 5 * time.Second), Table: scalar(v)}); err != nil {
			t.Fatal(err)
		}
	}
	now := t0.Add(5 * time.Minute) // the sampler rolls up every few minutes
	s.SetClock(func() time.Time { return now })

	avg := func() float64 {
		t.Helper()
		tb, err := s.Query(ctx, "cpu.headline", "load_pct", 3*time.Hour, "")
		if err != nil {
			t.Fatal(err)
		}
		if tb.Len() != 1 {
			t.Fatalf("want one 1-minute point, got %d rows", tb.Len())
		}
		return tb.Rows[0][1].(float64)
	}
	if err := s.Downsample(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := avg(); got != 50 {
		t.Fatalf("first roll-up: avg %v, want 50", got)
	}
	// Raw retention now ends mid-bucket: the next pass deletes the first
	// half of the raw points and must not rewrite the bucket from the
	// surviving half (nor on the pass after, when they are all gone).
	now = t0.Add(2 * time.Hour).Add(30 * time.Second)
	for pass := range 2 {
		if err := s.Downsample(ctx, 0); err != nil {
			t.Fatal(err)
		}
		if got := avg(); got != 50 {
			t.Errorf("pass %d rewrote the bucket from partial data: avg %v, want 50", pass+2, got)
		}
	}
}

func TestDownsampleDropsExpiredTiers(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	if err := s.Record(ctx, Sample{Recipe: "cpu.headline", TS: t0, Table: scalar(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEvent(ctx, Event{TS: t0, Kind: "power", Message: "x"}); err != nil {
		t.Fatal(err)
	}
	s.SetClock(func() time.Time { return t0.Add(31 * 24 * time.Hour) })
	if err := s.Downsample(ctx, 0); err != nil {
		t.Fatal(err)
	}
	for _, rng := range []time.Duration{time.Hour, 24 * time.Hour, 40 * 24 * time.Hour} {
		tb, err := s.Query(ctx, "cpu.headline", "load_pct", rng, "")
		if err != nil {
			t.Fatal(err)
		}
		if tb.Len() != 0 {
			t.Errorf("points older than every retention survive at range %v", rng)
		}
	}
	ev, _ := s.Events(ctx, 40*24*time.Hour, "")
	if ev.Len() != 0 {
		t.Error("expired event survives")
	}
}
