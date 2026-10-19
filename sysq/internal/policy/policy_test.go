package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMissingFileIsDefault(t *testing.T) {
	p, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.AllowedRoots) != len(Default().AllowedRoots) || p.Caps != Default().Caps {
		t.Error("missing file did not yield the shipped policy")
	}
}

func TestLoadInvalidFileReturnsDefaultsAndError(t *testing.T) {
	cases := []string{
		"version: 2\n",
		"caps: {rows: 999999}\n",
		"enabled_sensitivity: [top-secret]\n",
		"allowed_roots: [relative/path]\n",
		"unknown_key: 1\n",
		"caps: [1, 2]\n",
	}
	for _, text := range cases {
		p, err := Load(write(t, text))
		if err == nil {
			t.Errorf("%q: accepted", text)
		}
		if p.Caps != Default().Caps || len(p.ExcludedPaths) != len(Default().ExcludedPaths) {
			t.Errorf("%q: an invalid file must leave the shipped policy intact", text)
		}
	}
}

func TestShippedExcludesAlwaysApply(t *testing.T) {
	p, err := Load(write(t, "excluded_paths: ['~/Secrets']\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range append(Default().ExcludedPaths, "~/Secrets") {
		found := false
		for _, x := range p.ExcludedPaths {
			if x == want {
				found = true
			}
		}
		if !found {
			t.Errorf("exclude %q dropped", want)
		}
	}
}

func TestCapsNeverExceedHardLimits(t *testing.T) {
	p, err := Load(write(t, "caps: {rows: 100, bytes: 4096, depth: 2}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Caps != (Caps{Rows: 100, Bytes: 4096, Depth: 2}) {
		t.Errorf("caps %+v", p.Caps)
	}
	// The schema rejects anything above the hard limits before Load sees it.
	if _, err := Load(write(t, "caps: {depth: 5}\n")); err == nil {
		t.Error("depth above the hard limit accepted")
	}
}

func TestSensitivityAndNetworkSwitches(t *testing.T) {
	p, err := Load(write(t, "enabled_sensitivity: [public]\nnetwork_primitives: [ai.ollama_ps]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.SensitivityEnabled("personal") || !p.SensitivityEnabled("public") {
		t.Error("enabled_sensitivity not applied")
	}
	if !p.NetworkEnabled("ai.ollama_ps") || p.NetworkEnabled("ai.lmstudio_ps") {
		t.Error("network_primitives not applied")
	}
	if Default().NetworkEnabled("ai.ollama_ps") {
		t.Error("network primitives must be off by default")
	}
	if Default().SensitivityEnabled("secret") {
		t.Error("secret must be off by default")
	}
}

func TestDirHonoursSYSQHome(t *testing.T) {
	t.Setenv("SYSQ_HOME", "/custom/sysq")
	if Dir() != "/custom/sysq" {
		t.Errorf("Dir() = %q", Dir())
	}
}
