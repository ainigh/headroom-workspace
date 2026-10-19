package sampler

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultSchedulesParse(t *testing.T) {
	c := Default()
	if len(c.Schedules) == 0 {
		t.Fatal("no shipped schedules")
	}
	if _, err := Parse(DefaultYAML()); err != nil {
		t.Fatal(err)
	}
}

func TestParseDefaultsAndIntervals(t *testing.T) {
	c, err := Parse([]byte("version: 1\nschedules:\n  - recipe: health.overview\n  - recipe: health.overview\n    name: slow\n    every: 10s\n    battery_every: 2m\n    retention: 48h\n"))
	if err != nil {
		t.Fatal(err)
	}
	fast, slow := c.Schedules[0], c.Schedules[1]
	if fast.Name != "health.overview" {
		t.Errorf("name should default to the recipe, got %q", fast.Name)
	}
	if fast.Interval(true, false) != 5*time.Second || fast.Interval(false, false) != 30*time.Second {
		t.Errorf("defaults: view %v idle %v", fast.Interval(true, false), fast.Interval(false, false))
	}
	if fast.Interval(false, true) != 30*time.Second {
		t.Error("no battery_every: idle interval applies on battery")
	}
	if slow.Interval(false, true) != 2*time.Minute || slow.Interval(true, true) != 10*time.Second {
		t.Error("battery_every applies only with no view open")
	}
	if c.MaxRetention() != 48*time.Hour {
		t.Errorf("MaxRetention %v", c.MaxRetention())
	}
}

func TestParseRejectsBadSchedules(t *testing.T) {
	cases := map[string]string{
		"version: 1\nschedules:\n  - recipe: a.b\n  - recipe: a.b\n":        "duplicate name",
		"version: 1\nschedules:\n  - recipe: a.b\n    every: 500ms\n":       "at least 1s",
		"version: 1\nschedules:\n  - recipe: a.b\n    idle_every: 500ms\n":  "at least 1s",
		"version: 1\nschedules:\n  - recipe: a.b\n    battery_every: -1m\n": "battery_every",
		"version: 1\nschedules:\n  - recipe: a.b\n    retention: -1h\n":     "retention",
		"version: 1\nschedules:\n  - recipe: a.b\n    every: soon\n":        "every",
	}
	for text, want := range cases {
		_, err := Parse([]byte(text))
		if err == nil {
			t.Errorf("%q: accepted", text)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v should mention %q", text, err, want)
		}
	}
}
