package exec

import (
	"os"
	"strings"
	"testing"

	"github.com/ainigh/headroom-workspace/sysq"
	"io/fs"
)

func testSandbox(t *testing.T) *Sandbox {
	t.Helper()
	profiles, err := fs.Sub(sysq.Assets, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	return &Sandbox{Profiles: profiles, Home: "/Users/me", Excluded: []string{"~/.ssh", "~/Library/Keychains"}}
}

// Every shipped profile denies by default, denies network (except the
// network profile), and allows writes only to /dev/null and the scratch dir.
func TestProfilesDenyWritesAndNetwork(t *testing.T) {
	sb := testSandbox(t)
	entries, err := fs.ReadDir(sb.Profiles, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no profiles shipped")
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".sb")
		text, err := sb.Render(name, []string{"/bin/ps"}, "/private/tmp/sysq-scratch-x")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(text, "(deny default)") {
			t.Errorf("%s: missing (deny default)", name)
		}
		netProfile := name == "read-only-network" || name == "read-only-loopback"
		if !netProfile && !strings.Contains(text, "(deny network*)") {
			t.Errorf("%s: network not denied", name)
		}
		if netProfile && !strings.Contains(text, "(deny network-bind network-inbound)") {
			t.Errorf("%s: inbound not denied", name)
		}
		if name == "read-only-loopback" && (strings.Contains(text, "(allow network-outbound)") || !strings.Contains(text, `(allow network-outbound (remote ip "localhost:*"))`)) {
			t.Errorf("%s: outbound not limited to loopback", name)
		}
		if !strings.Contains(text, "(deny user-preference-write)") {
			t.Errorf("%s: preference writes not denied", name)
		}
		for _, line := range strings.Split(text, "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, ";") || !strings.Contains(l, "file-write") {
				continue
			}
			if !strings.HasPrefix(l, "(allow file-write* (literal \"/dev/null\") (subpath \"/private/tmp/sysq-scratch-x\")") {
				t.Errorf("%s: unexpected write rule %q", name, l)
			}
			if strings.Contains(l, "/private/var/db/sudo") && name != "read-only-sudo" {
				t.Errorf("%s: sudo dir writable", name)
			}
		}
		if !strings.Contains(text, `(deny file-read* (subpath "/Users/me/.ssh"))`) {
			t.Errorf("%s: excluded root not denied", name)
		}
		if !strings.Contains(text, `(allow process-exec (literal "/bin/ps"))`) {
			t.Errorf("%s: binary not allowed", name)
		}
		if strings.Contains(text, "process-exec*") || strings.Contains(text, "(allow process-exec)") {
			t.Errorf("%s: unrestricted exec", name)
		}
	}
}

func TestRenderRefusesBadInput(t *testing.T) {
	sb := testSandbox(t)
	if _, err := sb.Render("../etc/passwd", nil, "/tmp/x"); err == nil {
		t.Error("path traversal in profile name accepted")
	}
	if _, err := sb.Render("read-only-default", []string{"ps"}, "/tmp/x"); err == nil {
		t.Error("relative exec accepted")
	}
	if _, err := sb.Render("read-only-default", nil, ""); err == nil {
		t.Error("empty scratch accepted")
	}
	text, err := sb.Render("read-only-default", []string{`/tmp/a") (allow default) ("`}, "/tmp/x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "\n(allow default)") || strings.Contains(text, `") (allow default) ("`) {
		t.Error("SBPL injection through exec path")
	}
}

func TestWrapUsesSandboxExec(t *testing.T) {
	argv := Wrap("(version 1)", []string{"/bin/ps", "-ax"})
	if argv[0] != SandboxExec || argv[1] != "-p" || argv[3] != "/bin/ps" || argv[4] != "-ax" {
		t.Fatalf("bad wrap %v", argv)
	}
}

func TestNoSandboxMeansNoExecution(t *testing.T) {
	if Available() {
		t.Skip("sandbox-exec present")
	}
	r := &Runner{Sandbox: testSandbox(t), Scratch: os.TempDir()}
	_, err := r.Run(t.Context(), Invocation{Argv: []string{"/bin/echo", "hi"}, Profile: "read-only-default"})
	if err == nil {
		t.Fatal("ran a command without a sandbox")
	}
}
