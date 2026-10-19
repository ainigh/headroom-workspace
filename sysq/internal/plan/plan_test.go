package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ainigh/headroom-workspace/sysq/internal/testenv"
)

func TestExplainDesign16(t *testing.T) {
	cat, err := testenv.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	SetNamespaces(cat.Namespaces())
	src, _ := os.ReadFile(filepath.Join(testenv.Root(), "tests", "golden", "sugar", "design_16.sysq"))
	p, err := Explain(string(src), CatalogResolver{cat})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"disk.volumes", "net.connections", "proc.top", "sys.memory", "sys.thermal"}
	if strings.Join(p.Primitives, ",") != strings.Join(want, ",") {
		t.Fatalf("primitives %v", p.Primitives)
	}
	if p.Cost != "moderate" || p.Sensitivity != "personal" || !strings.Contains(p.Lowered, ".join(") {
		t.Fatalf("%+v", p)
	}
}

func TestExplainPushdownAndUnknown(t *testing.T) {
	cat, _ := testenv.Catalog()
	SetNamespaces(cat.Namespaces())
	p, err := Explain(`log.system(since: 1h) | filter process == "kernel" and len(message) > 3 | limit 5`, CatalogResolver{cat})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Pushdown) != 2 || !strings.Contains(strings.Join(p.Pushdown, "|"), `--nspredicate process == "kernel"`) {
		t.Fatalf("pushdown %v", p.Pushdown)
	}
	if p.Cost != "expensive" {
		t.Fatal("cost not derived")
	}
	p, _ = Explain(`proc.tpo | limit 3`, CatalogResolver{cat})
	if len(p.Unknown) != 1 {
		t.Fatalf("unknown %v", p.Unknown)
	}
	p, _ = Explain("for x in proc.list { emit proc.info(pid: x.pid) }", CatalogResolver{cat})
	if p.Units != 1+10*10 || p.Loops != 1 {
		t.Fatalf("loop units %d", p.Units)
	}
}
