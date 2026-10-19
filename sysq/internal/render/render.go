// Package render prints tables for terminals: aligned text with humanized
// bytes and durations, JSON, CSV, or Markdown.
package render

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// Item is one labeled output (emits and record returns render as several).
type Item struct {
	Label string
	Table *table.Table
}

// Write renders items in a format: table (default), json, csv, md.
func Write(w io.Writer, format string, items []Item) error {
	switch format {
	case "json":
		if len(items) == 1 && items[0].Label == "" {
			return writeJSON(w, items[0].Table)
		}
		out := make([]map[string]any, len(items))
		for i, it := range items {
			out[i] = map[string]any{"label": it.Label, "table": it.Table}
		}
		return writeJSON(w, out)
	case "csv":
		for i, it := range items {
			if i > 0 {
				fmt.Fprintln(w)
			}
			if it.Label != "" {
				fmt.Fprintf(w, "# %s\n", it.Label)
			}
			if err := CSV(w, it.Table); err != nil {
				return err
			}
		}
		return nil
	case "md":
		for i, it := range items {
			if i > 0 {
				fmt.Fprintln(w)
			}
			if it.Label != "" {
				fmt.Fprintf(w, "### %s\n\n", it.Label)
			}
			Markdown(w, it.Table)
		}
		return nil
	case "", "table":
		for i, it := range items {
			if i > 0 {
				fmt.Fprintln(w)
			}
			if it.Label != "" {
				fmt.Fprintf(w, "── %s\n", it.Label)
			}
			Text(w, it.Table)
		}
		return nil
	}
	return fmt.Errorf("unknown format %q (table, json, csv, md)", format)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cells(t *table.Table) ([]string, [][]string) {
	head := make([]string, len(t.Schema))
	for i, c := range t.Schema {
		head[i] = c.Name
		if c.Unit != "" && c.Type != table.Bytes {
			head[i] += " (" + c.Unit + ")"
		}
	}
	rows := make([][]string, len(t.Rows))
	for i, r := range t.Rows {
		out := make([]string, len(t.Schema))
		for j := range t.Schema {
			var v any
			if j < len(r) {
				v = r[j]
			}
			col := t.Schema[j]
			col.Unit = ""
			s := table.Humanize(col, v)
			s = strings.ReplaceAll(s, "\n", " ")
			if utf8.RuneCountInString(s) > 80 {
				s = string([]rune(s)[:79]) + "…"
			}
			out[j] = s
		}
		rows[i] = out
	}
	return head, rows
}

// Text renders an aligned table and a one-line footer from meta.
func Text(w io.Writer, t *table.Table) {
	if t == nil {
		return
	}
	head, rows := cells(t)
	width := make([]int, len(head))
	for i, h := range head {
		width[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > width[i] {
				width[i] = n
			}
		}
	}
	numeric := make([]bool, len(head))
	for i, c := range t.Schema {
		numeric[i] = c.Type.IsNumeric()
	}
	line := func(cs []string) {
		var b strings.Builder
		for i, c := range cs {
			pad := width[i] - utf8.RuneCountInString(c)
			if numeric[i] {
				b.WriteString(strings.Repeat(" ", pad) + c)
			} else {
				b.WriteString(c + strings.Repeat(" ", pad))
			}
			if i < len(cs)-1 {
				b.WriteString("  ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	if len(head) > 0 {
		line(head)
	}
	for _, r := range rows {
		line(r)
	}
	var notes []string
	notes = append(notes, fmt.Sprintf("%d rows", len(t.Rows)))
	if t.Meta.Truncated {
		notes = append(notes, "truncated")
	}
	if t.Meta.DroppedByLimit > 0 {
		notes = append(notes, fmt.Sprintf("%d dropped by caps", t.Meta.DroppedByLimit))
	}
	if t.Meta.Cached {
		notes = append(notes, "cached")
	}
	if t.Meta.Fixture {
		notes = append(notes, "fixture")
	}
	if t.Meta.Redacted > 0 {
		notes = append(notes, fmt.Sprintf("%d redacted", t.Meta.Redacted))
	}
	if t.Meta.Sensitivity != "" {
		notes = append(notes, t.Meta.Sensitivity)
	}
	if t.Meta.Error != "" {
		notes = append(notes, "error: "+t.Meta.Error)
	}
	if t.Meta.Unavailable != "" {
		notes = append(notes, "unavailable: "+t.Meta.Unavailable)
	}
	fmt.Fprintf(w, "(%s)\n", strings.Join(notes, ", "))
}

// CSV renders raw (unhumanized) values.
func CSV(w io.Writer, t *table.Table) error {
	cw := csv.NewWriter(w)
	head := make([]string, len(t.Schema))
	for i, c := range t.Schema {
		head[i] = c.Name
	}
	if err := cw.Write(head); err != nil {
		return err
	}
	for _, r := range t.Rows {
		rec := make([]string, len(t.Schema))
		for j := range t.Schema {
			if j < len(r) {
				rec[j] = table.Format(r[j])
			}
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Markdown renders a GitHub-style table.
func Markdown(w io.Writer, t *table.Table) {
	head, rows := cells(t)
	if len(head) == 0 {
		fmt.Fprintln(w, "_(empty)_")
		return
	}
	esc := func(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
	fmt.Fprintf(w, "| %s |\n", strings.Join(mapStr(head, esc), " | "))
	seps := make([]string, len(head))
	for i, c := range t.Schema {
		if c.Type.IsNumeric() {
			seps[i] = "---:"
		} else {
			seps[i] = "---"
		}
	}
	fmt.Fprintf(w, "| %s |\n", strings.Join(seps, " | "))
	for _, r := range rows {
		fmt.Fprintf(w, "| %s |\n", strings.Join(mapStr(r, esc), " | "))
	}
}

func mapStr(xs []string, f func(string) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return out
}
