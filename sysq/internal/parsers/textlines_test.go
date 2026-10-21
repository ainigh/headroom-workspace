package parsers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// log.file: a log whose last lines start with a blank line kept no rows at
// all; only an empty output means no lines.
func TestTextLinesLeadingBlankLine(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "log.file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Count(strings.TrimRight(string(fixture), "\n"), "\n") + 2
	rows, err := parseTextLines(Input{Outputs: map[string][]byte{"main": append([]byte("\n"), fixture...)}})
	if err != nil || len(rows) != want {
		t.Fatalf("got %d rows (%v), want %d", len(rows), err, want)
	}
	if rows[0]["text"] != "" || rows[0]["line"] != int64(1) || rows[1]["line"] != int64(2) {
		t.Fatalf("first rows %v %v", rows[0], rows[1])
	}
	if rows, _ := parseTextLines(Input{Outputs: map[string][]byte{"main": nil}}); len(rows) != 0 {
		t.Errorf("empty output: %d rows, want none", len(rows))
	}
	if rows, _ := parseTextLines(Input{Outputs: map[string][]byte{"main": []byte("\n")}}); len(rows) != 1 {
		t.Errorf("one blank line: %d rows, want 1", len(rows))
	}
}
