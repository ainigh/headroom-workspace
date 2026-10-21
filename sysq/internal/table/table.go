// Package table holds sysq's universal value: a typed table (design §12).
//
// Cell values are plain Go values whose meaning comes from the column type:
//
//	int, bytes, pid, port, uid  -> int64
//	float                       -> float64
//	duration                    -> float64 (seconds)
//	bool                        -> bool
//	str, path, enum             -> string
//	ts                          -> time.Time
//	json                        -> any (decoded JSON)
//	table                       -> *Table
//	missing                     -> nil
package table

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Type is a column type from design §12.1.
type Type string

const (
	Int      Type = "int"
	Float    Type = "float"
	Bool     Type = "bool"
	Str      Type = "str"
	TS       Type = "ts"
	Duration Type = "duration"
	Bytes    Type = "bytes"
	Path     Type = "path"
	PID      Type = "pid"
	Port     Type = "port"
	UID      Type = "uid"
	JSON     Type = "json"
	TableT   Type = "table"
	Enum     Type = "enum"
)

// KnownTypes lists every valid column type.
var KnownTypes = []Type{Int, Float, Bool, Str, TS, Duration, Bytes, Path, PID, Port, UID, JSON, TableT, Enum}

// IsKnown reports whether t is a valid column type.
func (t Type) IsKnown() bool {
	for _, k := range KnownTypes {
		if k == t {
			return true
		}
	}
	return false
}

// IsIntegral reports whether values of t are stored as int64.
func (t Type) IsIntegral() bool {
	switch t {
	case Int, Bytes, PID, Port, UID:
		return true
	}
	return false
}

// IsNumeric reports whether values of t are numbers.
func (t Type) IsNumeric() bool { return t.IsIntegral() || t == Float || t == Duration }

// Col describes one column.
type Col struct {
	Name   string   `json:"name" yaml:"name"`
	Type   Type     `json:"type" yaml:"type"`
	Unit   string   `json:"unit,omitempty" yaml:"unit,omitempty"`
	Values []string `json:"values,omitempty" yaml:"values,omitempty"` // enum vocabulary
	Schema []Col    `json:"schema,omitempty" yaml:"schema,omitempty"` // nested table schema
	Doc    string   `json:"doc,omitempty" yaml:"doc,omitempty"`
}

// Meta is attached to every table (design §12).
type Meta struct {
	Rows           int       `json:"rows"`
	Truncated      bool      `json:"truncated"`
	Source         []string  `json:"source,omitempty"`
	CostMS         int64     `json:"cost_ms"`
	Sensitivity    string    `json:"sensitivity,omitempty"`
	DroppedByLimit int       `json:"dropped_by_limit,omitempty"`
	SnapshotTS     time.Time `json:"snapshot_ts,omitzero"`
	Cached         bool      `json:"cached,omitempty"`
	Error          string    `json:"error,omitempty"`
	Unavailable    string    `json:"unavailable,omitempty"`
	Pushdown       []string  `json:"pushdown,omitempty"`
	Label          string    `json:"label,omitempty"`
	Fixture        bool      `json:"fixture,omitempty"`
	Redacted       int       `json:"redacted,omitempty"`
}

// Table is a schema, rows, and meta.
type Table struct {
	Schema []Col   `json:"schema"`
	Rows   [][]any `json:"rows"`
	Meta   Meta    `json:"meta"`
}

// New returns an empty table with the given schema.
func New(schema []Col) *Table {
	return &Table{Schema: append([]Col(nil), schema...), Rows: [][]any{}}
}

// Errorf returns an empty table carrying an error (errors are values, design §14.3).
func Errorf(format string, args ...any) *Table {
	t := New(nil)
	t.Meta.Error = fmt.Sprintf(format, args...)
	return t
}

// Scalar returns a 1×1 table.
func Scalar(name string, typ Type, v any) *Table {
	t := New([]Col{{Name: name, Type: typ}})
	t.Rows = append(t.Rows, []any{v})
	t.Meta.Rows = 1
	return t
}

// ColIndex returns the index of a column or -1.
func (t *Table) ColIndex(name string) int {
	for i, c := range t.Schema {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// Col returns a column description.
func (t *Table) Col(name string) (Col, bool) {
	if i := t.ColIndex(name); i >= 0 {
		return t.Schema[i], true
	}
	return Col{}, false
}

// Append adds a row; the row must match the schema width.
func (t *Table) Append(row ...any) {
	if len(row) != len(t.Schema) {
		panic(fmt.Sprintf("table: row has %d values, schema has %d", len(row), len(t.Schema)))
	}
	t.Rows = append(t.Rows, row)
	t.Meta.Rows = len(t.Rows)
}

// Get returns the value of column name in row r.
func (t *Table) Get(r int, name string) any {
	i := t.ColIndex(name)
	if i < 0 || r < 0 || r >= len(t.Rows) {
		return nil
	}
	return t.Rows[r][i]
}

// Len returns the number of rows.
func (t *Table) Len() int {
	if t == nil {
		return 0
	}
	return len(t.Rows)
}

// Clone returns a shallow copy (rows slice copied, row contents shared).
func (t *Table) Clone() *Table {
	c := &Table{Schema: append([]Col(nil), t.Schema...), Rows: append([][]any(nil), t.Rows...), Meta: t.Meta}
	c.Meta.Source = append([]string(nil), t.Meta.Source...)
	c.Meta.Pushdown = append([]string(nil), t.Meta.Pushdown...)
	return c
}

// WithRows returns a copy of t's schema and meta holding rows.
func (t *Table) WithRows(rows [][]any) *Table {
	c := &Table{Schema: append([]Col(nil), t.Schema...), Rows: rows, Meta: t.Meta}
	if c.Rows == nil {
		c.Rows = [][]any{}
	}
	c.Meta.Source = append([]string(nil), t.Meta.Source...)
	c.Meta.Rows = len(rows)
	return c
}

// Fix recomputes Meta.Rows.
func (t *Table) Fix() *Table {
	if t.Rows == nil {
		t.Rows = [][]any{}
	}
	t.Meta.Rows = len(t.Rows)
	return t
}

// MergeMeta folds provenance and flags from others into t.
func (t *Table) MergeMeta(others ...*Table) {
	for _, o := range others {
		if o == nil {
			continue
		}
		t.Meta.Source = mergeStrings(t.Meta.Source, o.Meta.Source)
		t.Meta.Pushdown = mergeStrings(t.Meta.Pushdown, o.Meta.Pushdown)
		t.Meta.Truncated = t.Meta.Truncated || o.Meta.Truncated
		t.Meta.CostMS += o.Meta.CostMS
		t.Meta.Sensitivity = MaxSensitivity(t.Meta.Sensitivity, o.Meta.Sensitivity)
		t.Meta.Fixture = t.Meta.Fixture || o.Meta.Fixture
		t.Meta.Redacted += o.Meta.Redacted
		if t.Meta.Error == "" {
			t.Meta.Error = o.Meta.Error
		}
		if t.Meta.SnapshotTS.IsZero() || (!o.Meta.SnapshotTS.IsZero() && o.Meta.SnapshotTS.Before(t.Meta.SnapshotTS)) {
			t.Meta.SnapshotTS = o.Meta.SnapshotTS
		}
	}
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			a = append(a, s)
			seen[s] = true
		}
	}
	return a
}

// Sensitivity ordering (design §6.3).
var sensitivityRank = map[string]int{"": 0, "public": 1, "personal": 2, "secret": 3}

// SensitivityRank returns the order of a sensitivity class (unknown = -1).
func SensitivityRank(s string) int {
	if r, ok := sensitivityRank[s]; ok {
		return r
	}
	return -1
}

// MaxSensitivity returns the more sensitive of a and b.
func MaxSensitivity(a, b string) string {
	if sensitivityRank[b] > sensitivityRank[a] {
		return b
	}
	return a
}

// ---- JSON -----------------------------------------------------------------

type jsonTable struct {
	Schema []Col   `json:"schema"`
	Rows   [][]any `json:"rows"`
	Meta   *Meta   `json:"meta,omitempty"`
}

// MarshalJSON renders cells per their column type.
func (t *Table) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.jsonValue(true))
}

func (t *Table) jsonValue(withMeta bool) jsonTable {
	rows := make([][]any, len(t.Rows))
	for i, r := range t.Rows {
		out := make([]any, len(r))
		for j, v := range r {
			var col Col
			if j < len(t.Schema) {
				col = t.Schema[j]
			}
			out[j] = EncodeCell(col, v)
		}
		rows[i] = out
	}
	jt := jsonTable{Schema: t.Schema, Rows: rows}
	if jt.Schema == nil {
		jt.Schema = []Col{}
	}
	if withMeta {
		m := t.Meta
		m.Rows = len(t.Rows)
		jt.Meta = &m
	}
	return jt
}

// EncodeCell converts a cell into its JSON form.
func EncodeCell(col Col, v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		return x.Format(time.RFC3339)
	case *Table:
		return x.jsonValue(false)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		return x
	}
	return v
}

// UnmarshalJSON decodes a table produced by MarshalJSON.
func (t *Table) UnmarshalJSON(data []byte) error {
	var raw struct {
		Schema []Col               `json:"schema"`
		Rows   [][]json.RawMessage `json:"rows"`
		Meta   *Meta               `json:"meta"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.Schema = raw.Schema
	if raw.Meta != nil {
		t.Meta = *raw.Meta
	}
	t.Rows = make([][]any, 0, len(raw.Rows))
	for _, r := range raw.Rows {
		row := make([]any, len(t.Schema))
		for j := range t.Schema {
			if j >= len(r) {
				continue
			}
			v, err := DecodeCell(t.Schema[j], r[j])
			if err != nil {
				return fmt.Errorf("column %s: %w", t.Schema[j].Name, err)
			}
			row[j] = v
		}
		t.Rows = append(t.Rows, row)
	}
	t.Meta.Rows = len(t.Rows)
	return nil
}

// DecodeCell converts JSON back into a typed cell.
func DecodeCell(col Col, raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	switch {
	case col.Type.IsIntegral():
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		return int64(f), nil
	case col.Type == Float || col.Type == Duration:
		var f float64
		err := json.Unmarshal(raw, &f)
		return f, err
	case col.Type == Bool:
		var b bool
		err := json.Unmarshal(raw, &b)
		return b, err
	case col.Type == TS:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return time.Parse(time.RFC3339, s)
	case col.Type == TableT:
		var sub Table
		if err := sub.UnmarshalJSON(raw); err != nil {
			return nil, err
		}
		if sub.Schema == nil && col.Schema != nil {
			sub.Schema = col.Schema
		}
		return &sub, nil
	case col.Type == JSON:
		var v any
		err := json.Unmarshal(raw, &v)
		return v, err
	default:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return string(raw), nil
		}
		return s, nil
	}
}

// ---- comparison and conversion -------------------------------------------

// Compare orders two cells: nil sorts first; numbers numerically; times
// chronologically; everything else by string form.
func Compare(a, b any) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		default:
			return 1
		}
	}
	if fa, ok := ToFloat(a); ok {
		if fb, ok := ToFloat(b); ok {
			switch {
			case fa < fb:
				return -1
			case fa > fb:
				return 1
			}
			return 0
		}
	}
	if ta, ok := a.(time.Time); ok {
		if tb, ok := b.(time.Time); ok {
			return ta.Compare(tb)
		}
	}
	if ba, ok := a.(bool); ok {
		if bb, ok := b.(bool); ok {
			switch {
			case ba == bb:
				return 0
			case !ba:
				return -1
			}
			return 1
		}
	}
	return strings.Compare(Format(a), Format(b))
}

// Equal reports cell equality with numeric coercion.
func Equal(a, b any) bool { return Compare(a, b) == 0 && (a == nil) == (b == nil) }

// ToFloat converts numeric cells to float64.
func ToFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	}
	return 0, false
}

// ToInt converts numeric cells to int64.
func ToInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

// Truthy implements cell truthiness.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case *Table:
		return x.Len() > 0
	case time.Time:
		return !x.IsZero()
	}
	if f, ok := ToFloat(v); ok {
		return f != 0
	}
	return true
}

// Format renders a cell as plain text (no humanizing).
func Format(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case time.Time:
		return x.Format(time.RFC3339)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return fmt.Sprintf("%.0f", x)
		}
		return fmt.Sprintf("%.2f", x)
	case *Table:
		return fmt.Sprintf("<table %d rows>", x.Len())
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

// Humanize renders a cell for terminals: bytes and durations human-readable.
func Humanize(col Col, v any) string {
	if v == nil {
		return "—"
	}
	switch col.Type {
	case Bytes:
		if n, ok := ToInt(v); ok {
			return HumanBytes(n)
		}
	case Duration:
		if f, ok := ToFloat(v); ok {
			return HumanDuration(f)
		}
	case Float:
		if f, ok := ToFloat(v); ok {
			s := fmt.Sprintf("%.1f", f)
			if col.Unit != "" && col.Unit != "bytes" {
				s += " " + col.Unit
			}
			return s
		}
	case TS:
		if t, ok := v.(time.Time); ok {
			return t.Local().Format("2006-01-02 15:04:05")
		}
	}
	s := Format(v)
	if col.Unit != "" && col.Type.IsNumeric() && col.Type != Bytes {
		s += " " + col.Unit
	}
	return s
}

// HumanBytes renders bytes with decimal units, as macOS does.
func HumanBytes(n int64) string {
	f := float64(n)
	neg := f < 0
	if neg {
		f = -f
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for f >= 999.5 && i < len(units)-1 {
		f /= 1000
		i++
	}
	var s string
	switch {
	case i == 0:
		s = fmt.Sprintf("%d B", int64(f))
	case f >= 99.95:
		// "%.0f" rounds up at .5, so 99.95 would print "100.0" below; and
		// values that round to 1000 moved to the next unit above.
		s = fmt.Sprintf("%.0f %s", f, units[i])
	default:
		s = fmt.Sprintf("%.1f %s", f, units[i])
	}
	if neg {
		return "-" + s
	}
	return s
}

// HumanDuration renders seconds compactly ("2h 10m", "40s").
func HumanDuration(sec float64) string {
	if sec < 0 {
		return "-" + HumanDuration(-sec)
	}
	d := time.Duration(sec * float64(time.Second))
	if d >= time.Second {
		// Round to whole seconds before choosing the unit: 59.6 is "1m 0s",
		// not "60s".
		d = d.Round(time.Second)
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// SortStable sorts rows by a comparator.
func (t *Table) SortStable(less func(a, b []any) bool) {
	sort.SliceStable(t.Rows, func(i, j int) bool { return less(t.Rows[i], t.Rows[j]) })
}

// Coerce converts a raw Go value into the storage form for typ.
func Coerce(typ Type, v any) any {
	if v == nil {
		return nil
	}
	switch {
	case typ.IsIntegral():
		if n, ok := ToInt(v); ok {
			return n
		}
		if b, ok := v.(bool); ok {
			if b {
				return int64(1)
			}
			return int64(0)
		}
	case typ == Float || typ == Duration:
		if f, ok := ToFloat(v); ok {
			return f
		}
	case typ == Str || typ == Path || typ == Enum:
		if s, ok := v.(string); ok {
			return s
		}
		return Format(v)
	}
	return v
}
