package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/testenv"
)

// At privacy "consumers" paths are reduced to their last element, including
// inside nested tables (list() columns), which used to pass through whole.
func TestFilterPrivacyNestedTables(t *testing.T) {
	e, err := testenv.Engine()
	if err != nil {
		t.Fatal(err)
	}
	res, err := script.Run(t.Context(), `fs.large(path: "~") | group by owner { list(path) as paths, list(name) as names }`,
		script.Options{Engine: e, SnapshotDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(res.Items)
	if !strings.Contains(string(before), "/Users/me/Downloads/Xcode_26.dmg") {
		t.Fatalf("fixture paths missing from the nested table: %s", before)
	}
	out := filterPrivacy(&Client{Privacy: "consumers"}, res.Items)
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "/Users/me") {
		t.Fatalf("path leaked through a nested table: %s", data)
	}
	if !strings.Contains(string(data), "…/Xcode_26.dmg") {
		t.Fatalf("nested path not reduced to its last element: %s", data)
	}
	// The input is not modified.
	if again, _ := json.Marshal(res.Items); string(again) != string(before) {
		t.Fatal("filterPrivacy modified its input")
	}
}
