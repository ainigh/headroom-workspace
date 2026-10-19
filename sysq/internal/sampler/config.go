// Package sampler runs scheduled recipes into the history store and fans
// results out to live subscribers (design §25). Schedules are YAML
// (schedules.yaml); a host app such as Headroom writes them through.
package sampler

import (
	_ "embed"
	"fmt"
	"os"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/history"
	"github.com/ainigh/headroom-workspace/sysq/internal/yamlcfg"
)

//go:embed schedules.schema.json
var schemaText string

//go:embed schedules.default.yaml
var defaultYAML []byte

var schema = yamlcfg.MustCompile("schedules.schema.json", schemaText)

// Schedule samples one recipe (or primitive).
type Schedule struct {
	Name         string         `yaml:"name"` // history series name; default recipe
	Recipe       string         `yaml:"recipe"`
	Params       map[string]any `yaml:"params"`
	Every        string         `yaml:"every"`         // while a view is open
	IdleEvery    string         `yaml:"idle_every"`    // no view open
	BatteryEvery string         `yaml:"battery_every"` // no view open, on battery
	KeepTable    bool           `yaml:"keep_table"`    // store whole tables (consumers)
	Columns      []string       `yaml:"columns"`
	Key          string         `yaml:"key"` // split series by this column
	Retention    string         `yaml:"retention"`
	Disabled     bool           `yaml:"disabled"`

	every, idle, battery, retention time.Duration
}

// EventRule records an event when a column's value changes.
type EventRule struct {
	Name     string `yaml:"name"` // schedule name
	Column   string `yaml:"column"`
	Kind     string `yaml:"kind"`
	Message  string `yaml:"message"` // {from} {to} {name}
	Severity string `yaml:"severity"`
}

// Config is schedules.yaml.
type Config struct {
	Version   int              `yaml:"version"`
	Schedules []Schedule       `yaml:"schedules"`
	Narrative []history.Series `yaml:"narrative"`
	Events    []EventRule      `yaml:"events"`
}

// Interval returns the sampling interval for the current state.
func (s *Schedule) Interval(viewOpen, onBattery bool) time.Duration {
	if viewOpen {
		return s.every
	}
	if onBattery && s.battery > 0 {
		return s.battery
	}
	return s.idle
}

// MaxRetention is the longest raw retention any schedule asks for.
func (c *Config) MaxRetention() time.Duration {
	var m time.Duration
	for _, s := range c.Schedules {
		if s.retention > m {
			m = s.retention
		}
	}
	return m
}

// Parse validates and normalizes schedules.yaml.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := schema.Decode(data, &c); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range c.Schedules {
		s := &c.Schedules[i]
		if s.Name == "" {
			s.Name = s.Recipe
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("schedules: duplicate name %s (give param variants a name)", s.Name)
		}
		seen[s.Name] = true
		var err error
		if s.every, err = parseDur(s.Every, 5*time.Second); err != nil {
			return nil, fmt.Errorf("%s: every: %w", s.Name, err)
		}
		if s.idle, err = parseDur(s.IdleEvery, 6*s.every); err != nil {
			return nil, fmt.Errorf("%s: idle_every: %w", s.Name, err)
		}
		if s.battery, err = parseDur(s.BatteryEvery, 0); err != nil {
			return nil, fmt.Errorf("%s: battery_every: %w", s.Name, err)
		}
		if s.retention, err = parseDur(s.Retention, 0); err != nil {
			return nil, fmt.Errorf("%s: retention: %w", s.Name, err)
		}
		if s.every < time.Second || s.idle < time.Second {
			return nil, fmt.Errorf("%s: every and idle_every must be at least 1s", s.Name)
		}
	}
	return &c, nil
}

func parseDur(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}

// Default returns the shipped schedules.
func Default() *Config {
	c, err := Parse(defaultYAML)
	if err != nil {
		panic(err)
	}
	return c
}

// DefaultYAML is the shipped file (written on first run).
func DefaultYAML() []byte { return defaultYAML }

// Load reads a schedules file; a missing file means the defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
}
