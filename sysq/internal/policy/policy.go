// Package policy holds the human-owned limits sysq enforces (separation-of-
// concerns §4: policy.yaml). The model can't change any of it.
package policy

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/ainigh/headroom-workspace/sysq/internal/yamlcfg"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

//go:embed policy.schema.json
var schemaText string

var schema = yamlcfg.MustCompile("policy.schema.json", schemaText)

// Policy is policy.yaml.
type Policy struct {
	Version            int      `yaml:"version"`
	AllowedRoots       []string `yaml:"allowed_roots"`
	ExcludedPaths      []string `yaml:"excluded_paths"`
	EnabledSensitivity []string `yaml:"enabled_sensitivity"`
	NetworkPrimitives  []string `yaml:"network_primitives"`
	Caps               Caps     `yaml:"caps"`
	CostBudget         int      `yaml:"cost_budget"`
	StepBudget         uint64   `yaml:"step_budget"`
}

// Caps are hard output limits.
type Caps struct {
	Rows  int `yaml:"rows"`
	Bytes int `yaml:"bytes"`
	Depth int `yaml:"depth"`
}

// Default is the shipped policy.
func Default() Policy {
	return Policy{
		Version:      1,
		AllowedRoots: []string{"~", "/Applications", "/Library", "/System/Applications", "/usr/local", "/opt/homebrew", "/Volumes", "/private/var/log", "/tmp", "/private/tmp"},
		ExcludedPaths: []string{
			"~/.ssh", "~/.gnupg", "~/.aws", "~/.config/gh", "~/.netrc", "~/.docker/config.json",
			"~/Library/Keychains", "~/Library/Group Containers/2BUA8C4S2C.com.1password",
			"~/Library/Containers/com.1password.1password", "~/Library/Application Support/1Password",
			"~/Library/Cookies", "~/Library/Messages", "~/Library/Mail",
		},
		EnabledSensitivity: []string{"public", "personal"},
		Caps:               Caps{Rows: 5000, Bytes: 2 << 20, Depth: 4},
		CostBudget:         1000,
		StepBudget:         2_000_000,
	}
}

// Load reads policy.yaml, falling back to defaults for missing fields. A
// missing file is not an error.
func Load(path string) (Policy, error) {
	p := Default()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	var f Policy
	if err := schema.Decode(data, &f); err != nil {
		return p, err
	}
	if f.AllowedRoots != nil {
		p.AllowedRoots = f.AllowedRoots
	}
	if f.ExcludedPaths != nil {
		// Excludes only ever grow: shipped excludes always apply.
		p.ExcludedPaths = append(p.ExcludedPaths, f.ExcludedPaths...)
	}
	if f.EnabledSensitivity != nil {
		p.EnabledSensitivity = f.EnabledSensitivity
	}
	p.NetworkPrimitives = f.NetworkPrimitives
	if f.Caps.Rows > 0 && f.Caps.Rows <= 5000 {
		p.Caps.Rows = f.Caps.Rows
	}
	if f.Caps.Bytes > 0 && f.Caps.Bytes <= 2<<20 {
		p.Caps.Bytes = f.Caps.Bytes
	}
	if f.Caps.Depth > 0 && f.Caps.Depth <= 4 {
		p.Caps.Depth = f.Caps.Depth
	}
	if f.CostBudget > 0 {
		p.CostBudget = f.CostBudget
	}
	if f.StepBudget > 0 {
		p.StepBudget = f.StepBudget
	}
	return p, nil
}

// SensitivityEnabled reports whether a class is enabled.
func (p Policy) SensitivityEnabled(s string) bool {
	for _, x := range p.EnabledSensitivity {
		if x == s {
			return true
		}
	}
	return false
}

// NetworkEnabled reports whether a network primitive was enabled by name.
func (p Policy) NetworkEnabled(name string) bool {
	for _, x := range p.NetworkPrimitives {
		if x == name {
			return true
		}
	}
	return false
}

// Paths returns the path policy for parameter validation.
func (p Policy) Paths(home string) argv.RootPolicy {
	return argv.RootPolicy{Home: home, Allowed: p.AllowedRoots, Excluded: p.ExcludedPaths}
}

// Dir is sysq's Application Support directory.
func Dir() string {
	if d := os.Getenv("SYSQ_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "sysq")
}
