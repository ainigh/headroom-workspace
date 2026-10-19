package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

func sample() *table.Table {
	t := table.New([]table.Col{
		{Name: "name", Type: table.Str},
		{Name: "size", Type: table.Bytes},
		{Name: "cpu", Type: table.Float, Unit: "%"},
	})
	t.Append("a|b", int64(1_500_000), 12.34)
	t.Append("zed", nil, 3.0)
	t.Meta.DroppedByLimit = 2
	t.Meta.Sensitivity = "personal"
	return t
}

func render(t *testing.T, format string, items ...Item) string {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, format, items); err != nil {
		t.Fatalf("%s: %v", format, err)
	}
	return b.String()
}

func TestTextAlignsAndHumanizes(t *testing.T) {
	out := render(t, "", Item{Table: sample()})
	want := strings.Join([]string{
		"name    size  cpu (%)",
		"a|b   1.5 MB     12.3",
		"zed        —      3.0",
		"(2 rows, 2 dropped by caps, personal)",
		"",
	}, "\n")
	if out != want {
		t.Errorf("text:\n%s\nwant:\n%s", out, want)
	}
}

func TestTextTruncatesLongCellsAndFlattensNewlines(t *testing.T) {
	tb := table.New([]table.Col{{Name: "s", Type: table.Str}})
	tb.Append("line1\nline2 " + strings.Repeat("x", 100))
	out := render(t, "table", Item{Table: tb})
	first := strings.SplitN(out, "\n", 3)[1]
	if strings.Contains(first, "\n") || !strings.HasSuffix(first, "…") || len([]rune(first)) != 80 {
		t.Errorf("cell not truncated to 80 runes: %q", first)
	}
}

func TestMarkdownEscapesPipesAndAlignsNumbers(t *testing.T) {
	out := render(t, "md", Item{Label: "Top", Table: sample()})
	for _, want := range []string{"### Top\n\n", "| name | size | cpu (%) |", "| --- | ---: | ---: |", `| a\|b | 1.5 MB | 12.3 |`} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q in:\n%s", want, out)
		}
	}
}

func TestCSVIsRaw(t *testing.T) {
	out := render(t, "csv", Item{Table: sample()})
	if !strings.HasPrefix(out, "name,size,cpu\na|b,1500000,12.34\nzed,,3\n") {
		t.Errorf("csv:\n%s", out)
	}
}

func TestJSONSingleVersusLabeled(t *testing.T) {
	single := render(t, "json", Item{Table: sample()})
	var tb map[string]any
	if err := json.Unmarshal([]byte(single), &tb); err != nil || tb["schema"] == nil {
		t.Errorf("single unlabeled item should be the bare table: %s", single)
	}
	multi := render(t, "json", Item{Label: "a", Table: sample()}, Item{Label: "b", Table: sample()})
	var items []map[string]any
	if err := json.Unmarshal([]byte(multi), &items); err != nil || len(items) != 2 || items[1]["label"] != "b" {
		t.Errorf("labeled items should be a list: %s", multi)
	}
}

func TestUnknownFormatAndNilTables(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, "xml", []Item{{Table: sample()}}); err == nil {
		t.Error("unknown format accepted")
	}
	for _, f := range []string{"table", "csv", "md"} {
		var b bytes.Buffer
		if err := Write(&b, f, []Item{{Table: nil}}); err != nil {
			t.Errorf("%s with nil table: %v", f, err)
		}
	}
}
