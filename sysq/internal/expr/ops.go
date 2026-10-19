package expr

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// Expression-driven operators from design §13. Each returns a new table.

// Filter keeps rows where pred is truthy.
func Filter(t *table.Table, pred Node, base *Env) (*table.Table, error) {
	var rows [][]any
	for _, r := range t.Rows {
		env := *base
		env.Table, env.Row = t, r
		v, err := Eval(pred, &env)
		if err != nil {
			return nil, fmt.Errorf("filter %s: %w", String(pred), err)
		}
		if table.Truthy(v.V) {
			rows = append(rows, r)
		}
	}
	return t.WithRows(rows), nil
}

// Select projects and computes columns. "*" keeps every column.
func Select(t *table.Table, items []Item, base *Env) (*table.Table, error) {
	type out struct {
		col  table.Col
		node Node
		src  int // >=0: copy column directly
	}
	var cols []out
	for _, it := range items {
		if id, ok := it.X.(Ident); ok && id.Name == "*" {
			for i, c := range t.Schema {
				cols = append(cols, out{col: c, src: i})
			}
			continue
		}
		name := it.As
		if name == "" {
			name = DefaultName(it.X)
		}
		if id, ok := it.X.(Ident); ok {
			if i := t.ColIndex(id.Name); i >= 0 {
				c := t.Schema[i]
				c.Name = name
				cols = append(cols, out{col: c, src: i})
				continue
			}
		}
		c := InferType(it.X, t.Schema, base.Bindings)
		c.Name = name
		cols = append(cols, out{col: c, node: it.X, src: -1})
	}
	schema := make([]table.Col, len(cols))
	for i, c := range cols {
		schema[i] = c.col
	}
	res := &table.Table{Schema: schema, Meta: t.Meta}
	res.Meta.Source = append([]string(nil), t.Meta.Source...)
	for _, r := range t.Rows {
		nr := make([]any, len(cols))
		for i, c := range cols {
			if c.src >= 0 {
				nr[i] = r[c.src]
				continue
			}
			env := *base
			env.Table, env.Row = t, r
			v, err := Eval(c.node, &env)
			if err != nil {
				return nil, fmt.Errorf("select %s: %w", String(c.node), err)
			}
			nr[i] = table.Coerce(schema[i].Type, v.V)
			if schema[i].Type == table.Str && v.T != table.Str && v.V != nil {
				// Inferred str but a typed value arrived: adopt its type once.
				schema[i].Type = v.T
				nr[i] = v.V
			}
		}
		res.Rows = append(res.Rows, nr)
	}
	return res.Fix(), nil
}

// Sort orders rows by keys (stable).
func Sort(t *table.Table, keys []Item, base *Env) (*table.Table, error) {
	type keyed struct {
		row  []any
		vals []any
	}
	ks := make([]keyed, len(t.Rows))
	for i, r := range t.Rows {
		ks[i].row = r
		for _, k := range keys {
			env := *base
			env.Table, env.Row = t, r
			v, err := Eval(k.X, &env)
			if err != nil {
				return nil, fmt.Errorf("sort by %s: %w", String(k.X), err)
			}
			ks[i].vals = append(ks[i].vals, v.V)
		}
	}
	sort.SliceStable(ks, func(a, b int) bool {
		for n, k := range keys {
			va, vb := ks[a].vals[n], ks[b].vals[n]
			// Missing values sort last either way.
			if va == nil || vb == nil {
				if va == nil && vb != nil {
					return false
				}
				if vb == nil && va != nil {
					return true
				}
				continue
			}
			c := table.Compare(va, vb)
			if c == 0 {
				continue
			}
			if k.Desc {
				return c > 0
			}
			return c < 0
		}
		return false
	})
	rows := make([][]any, len(ks))
	for i, k := range ks {
		rows[i] = k.row
	}
	return t.WithRows(rows), nil
}

// Group groups rows by key expressions and computes aggregates. With no keys
// the whole table is one group (even when empty, so sum() of nothing is 0).
func Group(t *table.Table, keys, aggs []Item, base *Env) (*table.Table, error) {
	var schema []table.Col
	for _, k := range keys {
		name := k.As
		if name == "" {
			name = DefaultName(k.X)
		}
		c := InferType(k.X, t.Schema, base.Bindings)
		c.Name = name
		schema = append(schema, c)
	}
	for _, a := range aggs {
		if !HasAggregate(a.X) {
			return nil, fmt.Errorf("%s is not an aggregate (count, sum, avg, min, max, p50, p95, any, all, list, …)", String(a.X))
		}
		name := a.As
		if name == "" {
			name = DefaultName(a.X)
		}
		c := InferType(a.X, t.Schema, base.Bindings)
		c.Name = name
		schema = append(schema, c)
	}
	type group struct {
		key  []any
		rows [][]any
	}
	var order []string
	groups := map[string]*group{}
	for _, r := range t.Rows {
		var kv []any
		var b strings.Builder
		for _, k := range keys {
			env := *base
			env.Table, env.Row = t, r
			v, err := Eval(k.X, &env)
			if err != nil {
				return nil, fmt.Errorf("group by %s: %w", String(k.X), err)
			}
			kv = append(kv, v.V)
			b.WriteString(table.Format(v.V))
			b.WriteByte(0)
		}
		key := b.String()
		g, ok := groups[key]
		if !ok {
			g = &group{key: kv}
			groups[key] = g
			order = append(order, key)
		}
		g.rows = append(g.rows, r)
	}
	if len(keys) == 0 && len(order) == 0 {
		groups[""] = &group{}
		order = append(order, "")
	}
	res := &table.Table{Schema: schema, Meta: t.Meta}
	res.Meta.Source = append([]string(nil), t.Meta.Source...)
	for _, k := range order {
		g := groups[k]
		nr := append([]any(nil), g.key...)
		for i, a := range aggs {
			v, err := EvalAgg(a.X, t, g.rows, base)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", String(a.X), err)
			}
			ci := len(keys) + i
			if v.T != "" && schema[ci].Type == table.Str && v.T != table.Str {
				schema[ci].Type = v.T
			}
			nr = append(nr, v.V)
		}
		res.Rows = append(res.Rows, nr)
	}
	return res.Fix(), nil
}

// Window adds one column per window item, computed over partitions in order.
// Supported: lag(x[,n]), lead(x[,n]), rank(), row_number(), running_sum(x),
// delta(x), rate(x) (per second of the order key), and any aggregate over a
// sliding frame of the last `rows` rows.
func ApplyWindow(t *table.Table, items []Item, base *Env) (*table.Table, error) {
	res := t.Clone()
	res.Rows = make([][]any, len(t.Rows))
	for i, r := range t.Rows {
		res.Rows[i] = append([]any(nil), r...)
	}
	for _, it := range items {
		c, ok := it.X.(Call)
		if !ok || c.Over == nil {
			return nil, fmt.Errorf("window needs fn(...) over (...)")
		}
		name := it.As
		if name == "" {
			name = DefaultName(c)
		}
		col := InferType(c, t.Schema, base.Bindings)
		col.Name = name
		// Partition rows (indexes into t.Rows), keep partition order of first appearance.
		parts := map[string][]int{}
		var porder []string
		for i, r := range t.Rows {
			var b strings.Builder
			for _, p := range c.Over.Partition {
				env := *base
				env.Table, env.Row = t, r
				v, err := Eval(p, &env)
				if err != nil {
					return nil, err
				}
				b.WriteString(table.Format(v.V))
				b.WriteByte(0)
			}
			k := b.String()
			if _, ok := parts[k]; !ok {
				porder = append(porder, k)
			}
			parts[k] = append(parts[k], i)
		}
		vals := make([]any, len(t.Rows))
		for _, k := range porder {
			idx := parts[k]
			if len(c.Over.Order) > 0 {
				sorted, err := orderIdx(t, idx, c.Over.Order, base)
				if err != nil {
					return nil, err
				}
				idx = sorted
			}
			if err := windowPartition(t, idx, c, vals, base); err != nil {
				return nil, err
			}
		}
		res.Schema = append(res.Schema, col)
		for i := range res.Rows {
			res.Rows[i] = append(res.Rows[i], table.Coerce(col.Type, vals[i]))
		}
	}
	return res.Fix(), nil
}

func orderIdx(t *table.Table, idx []int, order []SortKey, base *Env) ([]int, error) {
	items := make([]Item, len(order))
	for i, o := range order {
		items[i] = Item{X: o.X, Desc: o.Desc}
	}
	sub := t.WithRows(nil)
	for _, i := range idx {
		sub.Rows = append(sub.Rows, append(append([]any(nil), t.Rows[i]...), int64(i)))
	}
	sub.Schema = append(append([]table.Col(nil), t.Schema...), table.Col{Name: "__idx", Type: table.Int})
	sorted, err := Sort(sub, items, base)
	if err != nil {
		return nil, err
	}
	out := make([]int, len(idx))
	for n, r := range sorted.Rows {
		out[n] = int(r[len(r)-1].(int64))
	}
	return out, nil
}

func windowPartition(t *table.Table, idx []int, c Call, vals []any, base *Env) error {
	evalAt := func(i int, n Node) (Value, error) {
		env := *base
		env.Table, env.Row = t, t.Rows[i]
		return Eval(n, &env)
	}
	argN := func(def int) int {
		if len(c.Args) > 1 {
			if l, ok := c.Args[1].(Lit); ok {
				if n, ok := table.ToInt(l.Val); ok {
					return int(n)
				}
			}
		}
		return def
	}
	switch c.Fn {
	case "row_number", "rank":
		for n, i := range idx {
			vals[i] = int64(n + 1)
		}
		return nil
	case "lag", "lead":
		k := argN(1)
		for n, i := range idx {
			j := n - k
			if c.Fn == "lead" {
				j = n + k
			}
			if j < 0 || j >= len(idx) {
				vals[i] = nil
				continue
			}
			v, err := evalAt(idx[j], c.Args[0])
			if err != nil {
				return err
			}
			vals[i] = v.V
		}
		return nil
	case "running_sum":
		sum := 0.0
		for _, i := range idx {
			v, err := evalAt(i, c.Args[0])
			if err != nil {
				return err
			}
			f, _ := table.ToFloat(v.V)
			sum += f
			vals[i] = sum
		}
		return nil
	case "delta", "rate":
		for n, i := range idx {
			if n == 0 {
				vals[i] = nil
				continue
			}
			cur, err := evalAt(i, c.Args[0])
			if err != nil {
				return err
			}
			prev, err := evalAt(idx[n-1], c.Args[0])
			if err != nil {
				return err
			}
			a, ok1 := table.ToFloat(cur.V)
			b, ok2 := table.ToFloat(prev.V)
			if !ok1 || !ok2 {
				vals[i] = nil
				continue
			}
			if c.Fn == "delta" {
				vals[i] = a - b
				continue
			}
			// rate: per second of the first order key (a ts).
			if len(c.Over.Order) == 0 {
				return fmt.Errorf("rate() needs order by a timestamp")
			}
			tc, err := evalAt(i, c.Over.Order[0].X)
			if err != nil {
				return err
			}
			tp, err := evalAt(idx[n-1], c.Over.Order[0].X)
			if err != nil {
				return err
			}
			d, err := arith("-", tc, tp)
			if err != nil {
				return err
			}
			secs, _ := table.ToFloat(d.V)
			if secs <= 0 {
				vals[i] = nil
				continue
			}
			vals[i] = (a - b) / secs
		}
		return nil
	}
	if !aggFuncs[c.Fn] {
		return fmt.Errorf("%s() is not a window function", c.Fn)
	}
	for n, i := range idx {
		lo := 0
		if c.Over.Rows > 0 {
			lo = n - c.Over.Rows + 1
			if lo < 0 {
				lo = 0
			}
		}
		var frame [][]any
		for _, j := range idx[lo : n+1] {
			frame = append(frame, t.Rows[j])
		}
		v, err := Aggregate(Call{Fn: c.Fn, Args: c.Args}, t, frame, base)
		if err != nil {
			return err
		}
		vals[i] = v.V
	}
	return nil
}
