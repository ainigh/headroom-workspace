//go:build darwin

package exec

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The write-denial test (CLAUDE.md "safety guarantees get tests first"): a
// command run under every read-only profile cannot create a file in $HOME or
// /tmp, and cannot open a network connection.
func TestSandboxDeniesWrites(t *testing.T) {
	sb := testSandbox(t)
	home, _ := os.UserHomeDir()
	sb.Home = home
	scratch := t.TempDir()
	for _, profile := range []string{"read-only-default", "read-only-network", "read-only-sudo"} {
		for _, dir := range []string{home, "/tmp", filepath.Dir(scratch)} {
			target := filepath.Join(dir, "sysq-sandbox-probe-"+profile+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
			text, err := sb.Render(profile, []string{"/usr/bin/touch"}, scratch)
			if err != nil {
				t.Fatal(err)
			}
			r := &Runner{Sandbox: sb, Scratch: scratch}
			res, err := r.Run(t.Context(), Invocation{Argv: []string{"/usr/bin/touch", target}, ProfileText: text, Timeout: 5 * time.Second})
			if _, statErr := os.Stat(target); statErr == nil {
				os.Remove(target)
				t.Fatalf("%s: sandboxed touch created %s", profile, target)
			}
			if err == nil && res.ExitCode == 0 {
				t.Fatalf("%s: touch %s reported success", profile, target)
			}
		}
	}
}

func TestSandboxAllowsScratchOnly(t *testing.T) {
	sb := testSandbox(t)
	sb.Home, _ = os.UserHomeDir()
	scratch := t.TempDir()
	target := filepath.Join(scratch, "ok")
	text, err := sb.Render("read-only-default", []string{"/usr/bin/touch"}, scratch)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Sandbox: sb, Scratch: scratch}
	if _, err := r.Run(t.Context(), Invocation{Argv: []string{"/usr/bin/touch", target}, ProfileText: text, Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("scratch write refused: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("scratch write did not happen; profile is broken")
	}
}

func TestSandboxDeniesNetwork(t *testing.T) {
	sb := testSandbox(t)
	sb.Home, _ = os.UserHomeDir()
	scratch := t.TempDir()
	text, err := sb.Render("read-only-default", []string{"/usr/bin/nc"}, scratch)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Sandbox: sb, Scratch: scratch}
	res, err := r.Run(t.Context(), Invocation{Argv: []string{"/usr/bin/nc", "-z", "-G", "2", "1.1.1.1", "443"}, ProfileText: text, Timeout: 5 * time.Second})
	if err == nil && res.ExitCode == 0 {
		t.Fatal("sandboxed nc connected to the network")
	}
}

func TestSandboxDeniesExcludedReads(t *testing.T) {
	home, _ := os.UserHomeDir()
	ssh := filepath.Join(home, ".ssh")
	if _, err := os.Stat(ssh); err != nil {
		t.Skip("no ~/.ssh")
	}
	sb := testSandbox(t)
	sb.Home = home
	scratch := t.TempDir()
	text, err := sb.Render("read-only-default", []string{"/bin/ls"}, scratch)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Sandbox: sb, Scratch: scratch}
	res, err := r.Run(t.Context(), Invocation{Argv: []string{"/bin/ls", ssh}, ProfileText: text, Timeout: 5 * time.Second})
	if err == nil && res.ExitCode == 0 {
		t.Fatal("sandboxed ls listed ~/.ssh")
	}
}

func TestSandboxedCommandStillWorks(t *testing.T) {
	sb := testSandbox(t)
	sb.Home, _ = os.UserHomeDir()
	scratch := t.TempDir()
	text, err := sb.Render("read-only-default", []string{"/usr/bin/sw_vers"}, scratch)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Sandbox: sb, Scratch: scratch}
	res, err := r.Run(t.Context(), Invocation{Argv: []string{"/usr/bin/sw_vers"}, ProfileText: text, Timeout: 5 * time.Second})
	if err != nil || len(res.Stdout) == 0 {
		t.Fatalf("sw_vers failed under the sandbox: %v %s", err, res.Stderr)
	}
}
