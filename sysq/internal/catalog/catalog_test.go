package catalog

import (
	"strings"
	"testing"
)

const sudoBase = `name: x.power
doc: d
cost: cheap
sensitivity: public
requires_sudo: { binary: /usr/bin/powermetrics, args: ["--samplers", "thermal", "-n", "1"] }
sandbox: read-only-sudo
parser: p
schema: [{ name: a, type: int }]
`

func TestSudoOnlyExactRule(t *testing.T) {
	ok := sudoBase + `argv: ["/usr/bin/sudo", "-n", "/usr/bin/powermetrics", "--samplers", "thermal", "-n", "1"]` + "\n"
	if _, err := Parse([]byte(ok), "ok.yaml"); err != nil {
		t.Fatalf("exact sudo rule refused: %v", err)
	}
	bad := map[string]string{
		"extra flag":   `argv: ["/usr/bin/sudo", "-n", "/usr/bin/powermetrics", "--samplers", "thermal", "-n", "1", "-o", "/tmp/x"]`,
		"other binary": `argv: ["/usr/bin/sudo", "-n", "/bin/cat", "--samplers", "thermal", "-n", "1"]`,
		"no -n":        `argv: ["/usr/bin/sudo", "/usr/bin/powermetrics", "--samplers", "thermal", "-n", "1", "x"]`,
		"placeholder":  `argv: ["/usr/bin/sudo", "-n", "/usr/bin/powermetrics", "--samplers", "{s}", "-n", "1"]` + "\nparams: { s: { type: str } }",
	}
	for name, a := range bad {
		if _, err := Parse([]byte(sudoBase+a+"\n"), name+".yaml"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	noRule := strings.Replace(strings.Replace(sudoBase, "requires_sudo: { binary: /usr/bin/powermetrics, args: [\"--samplers\", \"thermal\", \"-n\", \"1\"] }\n", "", 1), "sandbox: read-only-sudo\n", "", 1)
	if _, err := Parse([]byte(noRule+`argv: ["/usr/bin/sudo", "-n", "/usr/bin/powermetrics", "--samplers", "thermal", "-n", "1"]`+"\n"), "norule.yaml"); err == nil {
		t.Error("sudo without requires_sudo accepted")
	}
}

func TestBinaryPathsVerbRules(t *testing.T) {
	src := `name: x.git
doc: d
cost: cheap
sensitivity: public
argv: ["{binary}", "fetch"]
binary_paths: ["/usr/bin/git"]
parser: p
schema: [{ name: a, type: int }]
`
	if _, err := Parse([]byte(src), "git.yaml"); err == nil {
		t.Fatal("git fetch through {binary} accepted")
	}
}

func TestNetworkProfiles(t *testing.T) {
	base := "name: x.net\ndoc: d\ncost: cheap\nsensitivity: public\nargv: [\"/usr/bin/true\"]\nparser: p\nschema: [{ name: a, type: int }]\n"
	if _, err := Parse([]byte(base+"network: true\nsandbox: read-only-loopback\n"), "a.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(base+"sandbox: read-only-loopback\n"), "b.yaml"); err == nil {
		t.Fatal("loopback profile without network: true accepted")
	}
	if _, err := Parse([]byte(base+"network: true\n"), "c.yaml"); err == nil {
		t.Fatal("network primitive on the default profile accepted")
	}
}
