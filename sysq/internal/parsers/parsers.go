// Package parsers turns command output into tables. One parser per output
// style; each is driven by a captured fixture and a golden table
// (tests/fixtures, tests/golden).
package parsers

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// Input is what a parser receives.
type Input struct {
	// Outputs maps command id ("main" for single-command primitives) to stdout.
	Outputs map[string][]byte
	// Params are the validated parameter values (typed).
	Params map[string]any
	// Now is the sample time (fixed in tests).
	Now time.Time
	// Loc is the local time zone (fixed in tests).
	Loc *time.Location
	// Home is the user's home directory.
	Home string
}

// Main returns the single command's output.
func (in Input) Main() string { return string(in.Outputs["main"]) }

// Out returns one command's output.
func (in Input) Out(id string) string { return string(in.Outputs[id]) }

// Row is one parsed record keyed by column name.
type Row map[string]any

// Func parses command output into rows.
type Func func(in Input) ([]Row, error)

var registry = map[string]Func{}

// Register adds a parser. Called from init functions.
func Register(name string, f Func) {
	if _, dup := registry[name]; dup {
		panic("duplicate parser " + name)
	}
	registry[name] = f
}

// Get returns a parser.
func Get(name string) (Func, bool) {
	f, ok := registry[name]
	return f, ok
}

// Names lists registered parsers.
func Names() []string {
	var out []string
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Build converts rows into a table using the declared schema. A row key that
// is not in the schema is an error: the schema is the parser's contract.
func Build(schema []table.Col, rows []Row) (*table.Table, error) {
	t := table.New(schema)
	idx := map[string]int{}
	for i, c := range schema {
		idx[c.Name] = i
	}
	for _, r := range rows {
		vals := make([]any, len(schema))
		for k, v := range r {
			i, ok := idx[k]
			if !ok {
				return nil, fmt.Errorf("parser produced column %q not in schema", k)
			}
			vals[i] = table.Coerce(schema[i].Type, v)
		}
		t.Rows = append(t.Rows, vals)
	}
	return t.Fix(), nil
}

// ---- helpers used by parsers ------------------------------------------------

// lines splits output into non-empty trimmed-right lines.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		l = strings.TrimRight(l, " \t")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// kv parses "key: value" or "key = value" lines.
func kv(s, sep string) map[string]string {
	out := map[string]string{}
	for _, l := range lines(s) {
		k, v, ok := strings.Cut(l, sep)
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func atoi(s string) (int64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "."))
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func atof(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func optInt(s string) any {
	if n, ok := atoi(s); ok {
		return n
	}
	return nil
}

func optFloat(s string) any {
	if f, ok := atof(s); ok {
		return f
	}
	return nil
}

// suffixBytes parses sizes like "1234M", "12G+", "512K-", "3456B", "2048.00M".
func suffixBytes(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "+-")
	if s == "" {
		return 0, false
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'B', 'b':
		s = s[:len(s)-1]
	case 'K', 'k':
		mult, s = 1024, s[:len(s)-1]
	case 'M', 'm':
		mult, s = 1024*1024, s[:len(s)-1]
	case 'G', 'g':
		mult, s = 1024*1024*1024, s[:len(s)-1]
	case 'T', 't':
		mult, s = 1024*1024*1024*1024, s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return int64(f * mult), true
}

func basename(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func loc(in Input) *time.Location {
	if in.Loc != nil {
		return in.Loc
	}
	return time.Local
}
