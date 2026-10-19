package parsers_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/internal/testenv"
)

var update = flag.Bool("update", false, "rewrite golden tables from fixtures")

// Every primitive has a fixture and a golden parsed table
// (implementation.md §3.1 MUST).
func TestGoldenTables(t *testing.T) {
	e, err := testenv.Engine()
	if err != nil {
		t.Fatal(err)
	}
	// Network primitives are off by policy; enable them for fixture parsing.
	for _, p := range e.Catalog.All() {
		if p.Network {
			e.Policy.NetworkPrimitives = append(e.Policy.NetworkPrimitives, p.Name)
		}
	}
	golden := filepath.Join(testenv.Root(), "tests", "golden")
	for _, p := range e.Catalog.All() {
		if p.Builtin != "" {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			tb, err := e.Primitive(t.Context(), p.Name, p.FixtureParams, engine.CallOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if tb.Meta.Unavailable != "" || tb.Meta.Error != "" {
				t.Fatalf("fixture run failed: %s%s", tb.Meta.Unavailable, tb.Meta.Error)
			}
			if tb.Len() == 0 {
				t.Fatal("parser produced no rows from its fixture")
			}
			tb.Meta.CostMS = 0
			tb.Meta.SnapshotTS = time.Time{}
			got, _ := json.MarshalIndent(tb, "", "  ")
			got = append(got, '\n')
			file := filepath.Join(golden, p.Name+".json")
			if *update {
				if err := os.WriteFile(file, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("missing golden %s (run go test ./internal/parsers -update)", file)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("golden mismatch for %s; run with -update after checking the diff\n%s", p.Name, got)
			}
			// Round trip: the golden decodes back into an equal table.
			var back table.Table
			if err := json.Unmarshal(want, &back); err != nil {
				t.Fatal(err)
			}
			if back.Len() != tb.Len() {
				t.Fatal("round trip lost rows")
			}
		})
	}
}
