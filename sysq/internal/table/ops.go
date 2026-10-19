package table

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

// Operators from design §13 that need no expressions. Each is pure: it
// returns a new table and never mutates its inputs.

// Limit keeps n rows after skipping offset.
func (t *Table) Limit(n, offset int) *Table {
	if offset < 0 {
		offset = 0
	}
	if n < 0 {
		n = 0
	}
	rows := t.Rows
	if offset > len(rows) {
		offset = len(rows)
	}
	rows = rows[offset:]
	dropped := 0
	if n < len(rows) {
		dropped = len(rows) - n
		rows = rows[:n]
	}
	out := t.WithRows(append([][]any(nil), rows...))
	out.Meta.DroppedByLimit += dropped
	return out
}

// Head is Limit(n, 0).
func (t *Table) Head(n int) *Table { return t.Limit(n, 0) }

// Tail keeps the last n rows.
func (t *Table) Tail(n int) *Table {
	if n >= len(t.Rows) {
		return t.WithRows(append([][]any(nil), t.Rows...))
	}
	if n < 0 {
		n = 0
	}
	out := t.WithRows(append([][]any(nil), t.Rows[len(t.Rows)-n:]...))
	out.Meta.DroppedByLimit += len(t.Rows) - n
	return out
}

// Sample keeps n pseudo-random rows (deterministic for a seed), in order.
func (t *Table) Sample(n int, seed int64) *Table {
	if n >= len(t.Rows) {
		return t.WithRows(append([][]any(nil), t.Rows...))
	}
	r := rand.New(rand.NewSource(seed))
	idx := r.Perm(len(t.Rows))[:n]
	sort.Ints(idx)
	rows := make([][]any, n)
	for i, j := range idx {
		rows[i] = t.Rows[j]
	}
	out := t.WithRows(rows)
	out.Meta.DroppedByLimit += len(t.Rows) - n
	return out
}

// Project keeps the named columns, in order.
func (t *Table) Project(names ...string) (*Table, error) {
	idx := make([]int, len(names))
	schema := make([]Col, len(names))
	for i, n := range names {
		j := t.ColIndex(n)
		if j < 0 {
			return nil, fmt.Errorf("no column %q", n)
		}
		idx[i], schema[i] = j, t.Schema[j]
	}
	out := &Table{Schema: schema, Meta: t.Meta}
	out.Meta.Source = append([]string(nil), t.Meta.Source...)
	for _, r := range t.Rows {
		nr := make([]any, len(idx))
		for i, j := range idx {
			nr[i] = r[j]
		}
		out.Rows = append(out.Rows, nr)
	}
	return out.Fix(), nil
}

// Column extracts one column as an N×1 table.
func (t *Table) Column(name string) (*Table, error) { return t.Project(name) }

func rowKey(r []any, idx []int) string {
	var b strings.Builder
	for _, i := range idx {
		b.WriteString(Format(r[i]))
		b.WriteByte(0)
	}
	return b.String()
}

func (t *Table) indexes(names []string) ([]int, error) {
	if len(names) == 0 {
		idx := make([]int, len(t.Schema))
		for i := range idx {
			idx[i] = i
		}
		return idx, nil
	}
	idx := make([]int, len(names))
	for i, n := range names {
		j := t.ColIndex(n)
		if j < 0 {
			return nil, fmt.Errorf("no column %q", n)
		}
		idx[i] = j
	}
	return idx, nil
}

// Distinct keeps the first row for each distinct value of fields (all
// columns when none are given).
func (t *Table) Distinct(fields ...string) (*Table, error) {
	idx, err := t.indexes(fields)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var rows [][]any
	for _, r := range t.Rows {
		k := rowKey(r, idx)
		if !seen[k] {
			seen[k] = true
			rows = append(rows, r)
		}
	}
	return t.WithRows(rows), nil
}

// Join combines t with o on equal key columns. how: inner, left, anti.
// Right-side columns that clash with left names get an "_<n>" suffix; the
// key column appears once.
func (t *Table) Join(o *Table, on []string, how string) (*Table, error) {
	if len(on) == 0 {
		return nil, fmt.Errorf("join needs on=")
	}
	li, err := t.indexes(on)
	if err != nil {
		return nil, fmt.Errorf("join left: %w", err)
	}
	ri, err := o.indexes(on)
	if err != nil {
		return nil, fmt.Errorf("join right: %w", err)
	}
	for k := range on {
		a, b := t.Schema[li[k]].Type, o.Schema[ri[k]].Type
		if a != b && !(a.IsIntegral() && b.IsIntegral() && (a == Int || b == Int)) && !(isStringy(a) && isStringy(b)) {
			return nil, fmt.Errorf("join key %s: %s vs %s", on[k], a, b)
		}
	}
	isKey := map[int]bool{}
	for _, i := range ri {
		isKey[i] = true
	}
	schema := append([]Col(nil), t.Schema...)
	var rcols []int
	if how != "anti" {
		names := map[string]bool{}
		for _, c := range schema {
			names[c.Name] = true
		}
		for j, c := range o.Schema {
			if isKey[j] {
				continue
			}
			name := c.Name
			for n := 2; names[name]; n++ {
				name = fmt.Sprintf("%s_%d", c.Name, n)
			}
			names[name] = true
			c.Name = name
			schema = append(schema, c)
			rcols = append(rcols, j)
		}
	}
	right := map[string][][]any{}
	for _, r := range o.Rows {
		k := rowKey(r, ri)
		right[k] = append(right[k], r)
	}
	out := &Table{Schema: schema}
	for _, l := range t.Rows {
		matches := right[rowKey(l, li)]
		switch how {
		case "anti":
			if len(matches) == 0 {
				out.Rows = append(out.Rows, l)
			}
		case "left", "inner", "":
			if len(matches) == 0 {
				if how == "left" {
					nr := append(append([]any(nil), l...), make([]any, len(rcols))...)
					out.Rows = append(out.Rows, nr)
				}
				continue
			}
			for _, m := range matches {
				nr := append([]any(nil), l...)
				for _, j := range rcols {
					nr = append(nr, m[j])
				}
				out.Rows = append(out.Rows, nr)
			}
		default:
			return nil, fmt.Errorf("unknown join kind %q (inner, left, anti)", how)
		}
	}
	out.Meta = t.Meta
	out.Meta.Source = append([]string(nil), t.Meta.Source...)
	out.MergeMeta(o)
	out.Meta.CostMS = t.Meta.CostMS + o.Meta.CostMS
	return out.Fix(), nil
}

func isStringy(t Type) bool { return t == Str || t == Path || t == Enum }

func compatible(a, b *Table) error {
	if len(a.Schema) != len(b.Schema) {
		return fmt.Errorf("tables have %d and %d columns", len(a.Schema), len(b.Schema))
	}
	for i := range a.Schema {
		if a.Schema[i].Name != b.Schema[i].Name {
			return fmt.Errorf("column %d is %s vs %s", i, a.Schema[i].Name, b.Schema[i].Name)
		}
	}
	return nil
}

// align reorders b's columns to a's names when both have the same set.
func align(a, b *Table) *Table {
	if compatible(a, b) == nil {
		return b
	}
	names := make([]string, len(a.Schema))
	for i, c := range a.Schema {
		names[i] = c.Name
	}
	if p, err := b.Project(names...); err == nil && len(a.Schema) == len(b.Schema) {
		return p
	}
	return b
}

// Union appends o's rows (columns matched by name). When schemas differ, the
// result has the union of columns.
func (t *Table) Union(o *Table) *Table {
	b := align(t, o)
	if compatible(t, b) == nil {
		out := t.WithRows(append(append([][]any(nil), t.Rows...), b.Rows...))
		out.MergeMeta(o)
		return out
	}
	schema := append([]Col(nil), t.Schema...)
	for _, c := range o.Schema {
		if t.ColIndex(c.Name) < 0 {
			schema = append(schema, c)
		}
	}
	out := &Table{Schema: schema, Meta: t.Meta}
	for _, src := range []*Table{t, o} {
		for _, r := range src.Rows {
			nr := make([]any, len(schema))
			for j, c := range schema {
				if k := src.ColIndex(c.Name); k >= 0 {
					nr[j] = r[k]
				}
			}
			out.Rows = append(out.Rows, nr)
		}
	}
	out.MergeMeta(o)
	return out.Fix()
}

// Except keeps rows of t not present in o (whole-row comparison).
func (t *Table) Except(o *Table) (*Table, error) {
	b := align(t, o)
	if err := compatible(t, b); err != nil {
		return nil, fmt.Errorf("except: %w", err)
	}
	idx, _ := t.indexes(nil)
	have := map[string]bool{}
	for _, r := range b.Rows {
		have[rowKey(r, idx)] = true
	}
	var rows [][]any
	for _, r := range t.Rows {
		if !have[rowKey(r, idx)] {
			rows = append(rows, r)
		}
	}
	out := t.WithRows(rows)
	out.MergeMeta(o)
	return out, nil
}

// Intersect keeps rows of t also present in o.
func (t *Table) Intersect(o *Table) (*Table, error) {
	b := align(t, o)
	if err := compatible(t, b); err != nil {
		return nil, fmt.Errorf("intersect: %w", err)
	}
	idx, _ := t.indexes(nil)
	have := map[string]bool{}
	for _, r := range b.Rows {
		have[rowKey(r, idx)] = true
	}
	var rows [][]any
	for _, r := range t.Rows {
		if have[rowKey(r, idx)] {
			rows = append(rows, r)
		}
	}
	out := t.WithRows(rows)
	out.MergeMeta(o)
	return out, nil
}

// Diff compares t (after) with before on key columns: rows gain a change
// column (added, removed, changed) and changed rows carry before values in
// <col>_before columns.
func (t *Table) Diff(before *Table, on []string) (*Table, error) {
	b := align(t, before)
	if len(on) == 0 {
		for _, c := range t.Schema {
			on = append(on, c.Name)
		}
	}
	ki, err := t.indexes(on)
	if err != nil {
		return nil, err
	}
	bi, err := b.indexes(on)
	if err != nil {
		return nil, err
	}
	keyCols := map[int]bool{}
	for _, i := range ki {
		keyCols[i] = true
	}
	schema := []Col{{Name: "change", Type: Enum, Values: []string{"added", "removed", "changed"}}}
	schema = append(schema, t.Schema...)
	var valCols []int
	for i, c := range t.Schema {
		if !keyCols[i] {
			c.Name += "_before"
			schema = append(schema, c)
			valCols = append(valCols, i)
		}
	}
	old := map[string][]any{}
	for _, r := range b.Rows {
		old[rowKey(r, bi)] = r
	}
	seen := map[string]bool{}
	out := &Table{Schema: schema}
	emit := func(change string, cur, prev []any) {
		nr := []any{change}
		if cur != nil {
			nr = append(nr, cur...)
		} else {
			nr = append(nr, prev...)
		}
		for _, i := range valCols {
			if prev != nil && cur != nil {
				nr = append(nr, prev[i])
			} else {
				nr = append(nr, nil)
			}
		}
		out.Rows = append(out.Rows, nr)
	}
	for _, r := range t.Rows {
		k := rowKey(r, ki)
		seen[k] = true
		p, ok := old[k]
		if !ok {
			emit("added", r, nil)
			continue
		}
		for _, i := range valCols {
			j := b.ColIndex(t.Schema[i].Name)
			if j >= 0 && !Equal(r[i], p[j]) {
				emit("changed", r, p)
				break
			}
		}
	}
	for _, r := range b.Rows {
		if !seen[rowKey(r, bi)] {
			emit("removed", nil, r)
		}
	}
	out.Meta = t.Meta
	out.MergeMeta(before)
	return out.Fix(), nil
}

// Nest collapses rows sharing by into a table-typed column named as.
func (t *Table) Nest(by []string, as string) (*Table, error) {
	ki, err := t.indexes(by)
	if err != nil {
		return nil, err
	}
	isKey := map[int]bool{}
	for _, i := range ki {
		isKey[i] = true
	}
	var subSchema []Col
	var subIdx []int
	for i, c := range t.Schema {
		if !isKey[i] {
			subSchema = append(subSchema, c)
			subIdx = append(subIdx, i)
		}
	}
	schema := make([]Col, 0, len(ki)+1)
	for _, i := range ki {
		schema = append(schema, t.Schema[i])
	}
	schema = append(schema, Col{Name: as, Type: TableT, Schema: subSchema})
	groups := map[string]*Table{}
	var order []string
	keys := map[string][]any{}
	for _, r := range t.Rows {
		k := rowKey(r, ki)
		g, ok := groups[k]
		if !ok {
			g = New(subSchema)
			groups[k] = g
			order = append(order, k)
			kv := make([]any, len(ki))
			for n, i := range ki {
				kv[n] = r[i]
			}
			keys[k] = kv
		}
		sub := make([]any, len(subIdx))
		for n, i := range subIdx {
			sub[n] = r[i]
		}
		g.Append(sub...)
	}
	out := &Table{Schema: schema, Meta: t.Meta}
	for _, k := range order {
		out.Rows = append(out.Rows, append(append([]any(nil), keys[k]...), groups[k]))
	}
	return out.Fix(), nil
}

// Unnest explodes a table-typed column into rows, repeating parent fields.
func (t *Table) Unnest(col string) (*Table, error) {
	ci := t.ColIndex(col)
	if ci < 0 {
		return nil, fmt.Errorf("no column %q", col)
	}
	if t.Schema[ci].Type != TableT {
		return nil, fmt.Errorf("column %q is %s, not table", col, t.Schema[ci].Type)
	}
	sub := t.Schema[ci].Schema
	if sub == nil {
		for _, r := range t.Rows {
			if st, ok := r[ci].(*Table); ok && st != nil {
				sub = st.Schema
				break
			}
		}
	}
	var schema []Col
	for i, c := range t.Schema {
		if i != ci {
			schema = append(schema, c)
		}
	}
	names := map[string]bool{}
	for _, c := range schema {
		names[c.Name] = true
	}
	subNames := make([]string, len(sub))
	for i, c := range sub {
		subNames[i] = c.Name
		if names[c.Name] {
			c.Name = col + "_" + c.Name
		}
		schema = append(schema, c)
	}
	out := &Table{Schema: schema, Meta: t.Meta}
	for _, r := range t.Rows {
		parent := make([]any, 0, len(r)-1)
		for i, v := range r {
			if i != ci {
				parent = append(parent, v)
			}
		}
		st, _ := r[ci].(*Table)
		if st == nil || st.Len() == 0 {
			continue
		}
		for _, sr := range st.Rows {
			nr := append([]any(nil), parent...)
			for _, n := range subNames {
				nr = append(nr, sr[st.ColIndex(n)])
			}
			out.Rows = append(out.Rows, nr)
		}
	}
	return out.Fix(), nil
}

// Pivot turns values of colKey into columns holding valCol, one row per rowKey.
func (t *Table) Pivot(rowKey, colKey, valCol string) (*Table, error) {
	ri, ci, vi := t.ColIndex(rowKey), t.ColIndex(colKey), t.ColIndex(valCol)
	if ri < 0 || ci < 0 || vi < 0 {
		return nil, fmt.Errorf("pivot needs existing columns %s, %s, %s", rowKey, colKey, valCol)
	}
	var cols []string
	seenCol := map[string]bool{}
	var rowsOrder []string
	rowVals := map[string]any{}
	cells := map[string]map[string]any{}
	for _, r := range t.Rows {
		rk, ck := Format(r[ri]), Format(r[ci])
		if !seenCol[ck] {
			seenCol[ck] = true
			cols = append(cols, ck)
		}
		if _, ok := cells[rk]; !ok {
			cells[rk] = map[string]any{}
			rowsOrder = append(rowsOrder, rk)
			rowVals[rk] = r[ri]
		}
		cells[rk][ck] = r[vi]
	}
	if len(cols) > 200 {
		return nil, fmt.Errorf("pivot would create %d columns (max 200)", len(cols))
	}
	schema := []Col{t.Schema[ri]}
	vt := t.Schema[vi]
	for _, c := range cols {
		vc := vt
		vc.Name = sanitize(c)
		schema = append(schema, vc)
	}
	out := &Table{Schema: schema, Meta: t.Meta}
	for _, rk := range rowsOrder {
		nr := []any{rowVals[rk]}
		for _, c := range cols {
			nr = append(nr, cells[rk][c])
		}
		out.Rows = append(out.Rows, nr)
	}
	return out.Fix(), nil
}

// Unpivot turns columns into (name, value) rows.
func (t *Table) Unpivot(cols ...string) (*Table, error) {
	idx, err := t.indexes(cols)
	if err != nil {
		return nil, err
	}
	isVal := map[int]bool{}
	for _, i := range idx {
		isVal[i] = true
	}
	var schema []Col
	var keep []int
	for i, c := range t.Schema {
		if !isVal[i] {
			schema = append(schema, c)
			keep = append(keep, i)
		}
	}
	vt := t.Schema[idx[0]]
	vt.Name = "value"
	for _, i := range idx[1:] {
		if t.Schema[i].Type != vt.Type {
			vt = Col{Name: "value", Type: Str}
		}
	}
	schema = append(schema, Col{Name: "name", Type: Str}, vt)
	out := &Table{Schema: schema, Meta: t.Meta}
	for _, r := range t.Rows {
		for _, i := range idx {
			nr := make([]any, 0, len(keep)+2)
			for _, k := range keep {
				nr = append(nr, r[k])
			}
			v := r[i]
			if vt.Type == Str && v != nil {
				v = Format(v)
			}
			nr = append(nr, t.Schema[i].Name, v)
			out.Rows = append(out.Rows, nr)
		}
	}
	return out.Fix(), nil
}

func sanitize(s string) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			b[i] = '_'
		}
	}
	if len(b) == 0 || b[0] >= '0' && b[0] <= '9' {
		return "c_" + string(b)
	}
	return string(b)
}
