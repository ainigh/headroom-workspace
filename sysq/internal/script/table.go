package script

import (
	"fmt"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/expr"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// source is something a lazy table materializes from: a primitive or a recipe.
type source struct {
	kind   string // "primitive" | "recipe"
	name   string
	prim   *catalog.Primitive
	params map[string]any
	ref    bool // a bare reference (no call yet): callable
}

// Table is the Starlark view of a sysq table. It may be lazy: a primitive
// call whose filter/limit can still be pushed into its parameters.
type Table struct {
	rt   *Runtime
	t    *table.Table
	src  *source
	post []func(*table.Table) (*table.Table, error)
	push []string
	err  error
}

var (
	_ starlark.HasAttrs  = (*Table)(nil)
	_ starlark.Iterable  = (*Table)(nil)
	_ starlark.Sequence  = (*Table)(nil)
	_ starlark.Indexable = (*Table)(nil)
	_ starlark.Callable  = (*Table)(nil)
)

func newTable(t *table.Table) *Table { return &Table{t: t} }

func (rt *Runtime) wrap(t *table.Table) *Table { return &Table{rt: rt, t: t} }

// Materialize returns the underlying table, running the primitive if needed.
// Plan-time errors (sensitivity, params, budget) are returned; runtime
// failures are tables with meta.error.
func (t *Table) Materialize() (*table.Table, error) {
	if t.t != nil {
		return t.t, nil
	}
	if t.err != nil {
		return nil, t.err
	}
	if t.src == nil || t.rt == nil {
		return table.New(nil), nil
	}
	var res *table.Table
	switch t.src.kind {
	case "primitive":
		r, err := t.rt.callPrimitive(t.src.name, t.src.params)
		if err != nil {
			t.err = err
			return nil, err
		}
		res = r
	case "recipe":
		v, err := t.rt.callRecipe(t.src.name, t.src.params)
		if err != nil {
			t.err = err
			return nil, err
		}
		tv, ok := v.(*Table)
		if !ok {
			err := fmt.Errorf("recipe %s returns %s, not a table; index its result", t.src.name, v.Type())
			t.err = err
			return nil, err
		}
		r, err := tv.Materialize()
		if err != nil {
			t.err = err
			return nil, err
		}
		res = r
	}
	for _, f := range t.post {
		r, err := f(res)
		if err != nil {
			t.err = err
			return nil, err
		}
		res = r
	}
	if len(t.push) > 0 {
		res = res.Clone()
		res.Meta.Pushdown = append(res.Meta.Pushdown, t.push...)
	}
	t.t = res
	return res, nil
}

// soft materializes for interfaces that cannot return errors: a failure
// becomes an empty table carrying meta.error.
func (t *Table) soft() *table.Table {
	r, err := t.Materialize()
	if err != nil {
		return table.Errorf("%v", err)
	}
	return r
}

func (t *Table) String() string {
	if t.t == nil && t.src != nil {
		return fmt.Sprintf("<%s %s>", t.src.kind, t.src.name)
	}
	r := t.soft()
	names := make([]string, len(r.Schema))
	for i, c := range r.Schema {
		names[i] = c.Name
	}
	return fmt.Sprintf("<table %d rows: %s>", r.Len(), strings.Join(names, ", "))
}
func (t *Table) Type() string          { return "table" }
func (t *Table) Freeze()               {}
func (t *Table) Truth() starlark.Bool  { return t.soft().Len() > 0 }
func (t *Table) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: table") }
func (t *Table) Len() int              { return t.soft().Len() }

func (t *Table) Index(i int) starlark.Value {
	r := t.soft()
	if len(r.Schema) == 1 {
		return toStarlark(r.Schema[0], r.Rows[i][0])
	}
	return &Row{t: r, row: r.Rows[i]}
}

func (t *Table) Iterate() starlark.Iterator {
	r := t.soft()
	return &rowIter{t: r}
}

type rowIter struct {
	t *table.Table
	i int
}

func (it *rowIter) Next(p *starlark.Value) bool {
	if it.i >= len(it.t.Rows) {
		return false
	}
	*p = &Row{t: it.t, row: it.t.Rows[it.i]}
	it.i++
	return true
}
func (it *rowIter) Done() {}

func (t *Table) Name() string {
	if t.src != nil {
		return t.src.name
	}
	return "table"
}

// CallInternal makes a bare primitive/recipe reference callable:
// proc.top(by="cpu", n=10).
func (t *Table) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if t.src == nil || !t.src.ref {
		return nil, fmt.Errorf("a table is not callable")
	}
	params := map[string]any{}
	order := t.rt.paramOrder(t.src)
	if len(args) > len(order) {
		return nil, fmt.Errorf("%s: takes at most %d positional arguments", t.src.name, len(order))
	}
	for i, a := range args {
		params[order[i]] = paramValue(a)
	}
	for _, kv := range kwargs {
		k := string(kv[0].(starlark.String))
		k = strings.TrimSuffix(k, "_")
		if _, dup := params[k]; dup {
			return nil, fmt.Errorf("%s: %s given twice", t.src.name, k)
		}
		params[k] = paramValue(kv[1])
	}
	if t.src.kind == "recipe" {
		// Recipes can return records; run now and return what they return.
		return t.rt.callRecipe(t.src.name, params)
	}
	return &Table{rt: t.rt, src: &source{kind: t.src.kind, name: t.src.name, prim: t.src.prim, params: params}}, nil
}

// paramValue converts a Starlark argument for primitive/recipe params.
func paramValue(v starlark.Value) any {
	switch x := v.(type) {
	case *Expr:
		return x
	case *Table:
		// A 1×1 table passes its scalar.
		if r := x.soft(); r.Len() == 1 && len(r.Schema) == 1 {
			c := r.Schema[0]
			switch c.Type {
			case table.Bytes:
				return Bytes(table.Coerce(table.Bytes, r.Rows[0][0]).(int64))
			case table.Duration:
				f, _ := table.ToFloat(r.Rows[0][0])
				return Duration(f)
			}
			return r.Rows[0][0]
		}
		return x
	}
	return goValue(v)
}

var tableMethods = []string{
	"filter", "select", "sort", "limit", "head", "tail", "sample", "distinct", "group", "join",
	"union", "except_", "intersect", "diff", "nest", "unnest", "pivot", "unpivot", "window",
	"col", "count", "first", "last", "max", "min", "sum", "avg", "values", "scalar", "meta", "columns",
}

func (t *Table) AttrNames() []string {
	names := append([]string(nil), tableMethods...)
	if t.t != nil {
		for _, c := range t.t.Schema {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return names
}

func (t *Table) Attr(name string) (starlark.Value, error) {
	for _, m := range tableMethods {
		if m == name {
			return starlark.NewBuiltin(name, t.method), nil
		}
	}
	// Column access: t.cpu is t.col("cpu").
	r, err := t.Materialize()
	if err != nil {
		return nil, err
	}
	if i := r.ColIndex(name); i >= 0 {
		// One row and a nested table column: drill in (design §12.2).
		if r.Schema[i].Type == table.TableT && r.Len() == 1 {
			if sub, ok := r.Rows[0][i].(*table.Table); ok {
				s := sub.Clone()
				s.MergeMeta(r)
				return t.derive(s), nil
			}
		}
		c, _ := r.Column(name)
		return t.derive(c), nil
	}
	return nil, nil
}

func (t *Table) derive(r *table.Table) *Table { return &Table{rt: t.rt, t: r} }

func (t *Table) env() *expr.Env {
	if t.rt == nil {
		return &expr.Env{}
	}
	return t.rt.exprEnv()
}

// method dispatches table methods.
func (t *Table) method(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	name := fn.Name()
	if t.rt != nil {
		if err := t.rt.tick(); err != nil {
			return nil, err
		}
	}
	// Pushdown-capable operations on a lazy primitive.
	if t.t == nil && t.src != nil && t.src.kind == "primitive" && t.src.prim != nil && t.src.prim.Pushdown != nil {
		switch name {
		case "filter":
			if len(args) == 1 {
				if e, ok := args[0].(*Expr); ok {
					if lt, ok := t.pushFilter(e); ok {
						return lt, nil
					}
				}
			}
		case "limit", "head":
			if len(args) >= 1 && len(kwargs) == 0 && (name == "head" || len(args) == 1) {
				if n, ok := starlark.AsInt32(args[0]); ok == nil {
					if lt, ok := t.pushLimit(n); ok {
						return lt, nil
					}
				}
			}
		}
	}
	r, err := t.Materialize()
	if err != nil {
		return nil, err
	}
	out, err := t.apply(thread, name, r, args, kwargs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

func (t *Table) apply(thread *starlark.Thread, name string, r *table.Table, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	env := t.env()
	switch name {
	case "filter":
		if len(args) != 1 {
			return nil, fmt.Errorf("takes one predicate")
		}
		switch p := args[0].(type) {
		case *Expr:
			n, b, err := p.node(t.rt)
			if err != nil {
				return nil, err
			}
			e := *env
			e.Bindings = b
			res, err := expr.Filter(r, n, &e)
			return t.derive(res), err
		case starlark.String:
			n, err := expr.Parse(string(p))
			if err != nil {
				return nil, err
			}
			res, err := expr.Filter(r, n, env)
			return t.derive(res), err
		case starlark.Callable:
			var rows [][]any
			for _, row := range r.Rows {
				v, err := starlark.Call(thread, p, starlark.Tuple{&Row{t: r, row: row}}, nil)
				if err != nil {
					return nil, err
				}
				if v.Truth() {
					rows = append(rows, row)
				}
			}
			return t.derive(r.WithRows(rows)), nil
		}
		return nil, fmt.Errorf("predicate must be expr(...), a string, or a function")
	case "select", "sort", "window":
		mode := name
		items, b, err := listArg(t.rt, args, mode)
		if err != nil {
			return nil, err
		}
		e := *env
		e.Bindings = b
		var res *table.Table
		switch name {
		case "select":
			res, err = expr.Select(r, items, &e)
		case "sort":
			res, err = expr.Sort(r, items, &e)
		case "window":
			res, err = expr.ApplyWindow(r, items, &e)
		}
		return t.derive(res), err
	case "group":
		if len(args) < 1 || len(args) > 2 {
			return nil, fmt.Errorf("group(keys, aggregates)")
		}
		var keys, aggs []expr.Item
		bindings := map[string]any{}
		for i, a := range args {
			items, b, err := listArg(t.rt, starlark.Tuple{a}, "group")
			if err != nil {
				return nil, err
			}
			for k, v := range b {
				bindings[k] = v
			}
			if len(args) == 1 || i == 1 {
				aggs = items
			} else {
				keys = items
			}
		}
		e := *env
		e.Bindings = bindings
		res, err := expr.Group(r, keys, aggs, &e)
		return t.derive(res), err
	case "limit":
		var n, offset int
		if err := starlark.UnpackArgs(name, args, kwargs, "n", &n, "offset?", &offset); err != nil {
			return nil, err
		}
		return t.derive(r.Limit(n, offset)), nil
	case "head", "tail", "sample":
		var n int
		seed := 1
		if err := starlark.UnpackArgs(name, args, kwargs, "n", &n, "seed?", &seed); err != nil {
			return nil, err
		}
		switch name {
		case "head":
			return t.derive(r.Head(n)), nil
		case "tail":
			return t.derive(r.Tail(n)), nil
		}
		return t.derive(r.Sample(n, int64(seed))), nil
	case "distinct", "unpivot":
		cols, err := strArgs(args)
		if err != nil {
			return nil, err
		}
		var res *table.Table
		if name == "distinct" {
			res, err = r.Distinct(cols...)
		} else {
			res, err = r.Unpivot(cols...)
		}
		return t.derive(res), err
	case "pivot":
		cols, err := strArgs(args)
		if err != nil || len(cols) != 3 {
			return nil, fmt.Errorf("pivot(row_key, column_key, value)")
		}
		res, err := r.Pivot(cols[0], cols[1], cols[2])
		return t.derive(res), err
	case "unnest", "col":
		cols, err := strArgs(args)
		if err != nil || len(cols) != 1 {
			return nil, fmt.Errorf("%s(column)", name)
		}
		var res *table.Table
		if name == "unnest" {
			res, err = r.Unnest(cols[0])
		} else {
			res, err = r.Column(cols[0])
		}
		return t.derive(res), err
	case "join":
		var other starlark.Value
		var on starlark.Value
		how := "inner"
		if err := starlark.UnpackArgs(name, args, kwargs, "other", &other, "on", &on, "how?", &how); err != nil {
			return nil, err
		}
		o, err := asTable(other)
		if err != nil {
			return nil, err
		}
		keys, err := strOrList(on)
		if err != nil {
			return nil, err
		}
		res, err := r.Join(o, keys, how)
		return t.derive(res), err
	case "union", "except_", "intersect":
		if len(args) != 1 {
			return nil, fmt.Errorf("takes one table")
		}
		o, err := asTable(args[0])
		if err != nil {
			return nil, err
		}
		switch name {
		case "union":
			return t.derive(r.Union(o)), nil
		case "except_":
			res, err := r.Except(o)
			return t.derive(res), err
		}
		res, err := r.Intersect(o)
		return t.derive(res), err
	case "diff":
		var other, on starlark.Value = nil, starlark.None
		if err := starlark.UnpackArgs(name, args, kwargs, "before", &other, "on?", &on); err != nil {
			return nil, err
		}
		o, err := asTable(other)
		if err != nil {
			return nil, err
		}
		var keys []string
		if on != starlark.None {
			if keys, err = strOrList(on); err != nil {
				return nil, err
			}
		}
		res, err := r.Diff(o, keys)
		return t.derive(res), err
	case "nest":
		var by starlark.Value
		as := "rows"
		if err := starlark.UnpackArgs(name, args, kwargs, "by", &by, "as_?", &as); err != nil {
			return nil, err
		}
		keys, err := strOrList(by)
		if err != nil {
			return nil, err
		}
		res, err := r.Nest(keys, as)
		return t.derive(res), err
	case "count":
		return starlark.MakeInt(r.Len()), nil
	case "columns":
		l := make([]starlark.Value, len(r.Schema))
		for i, c := range r.Schema {
			l[i] = starlark.String(c.Name)
		}
		return starlark.NewList(l), nil
	case "meta":
		return metaDict(r), nil
	case "first", "last", "max", "min", "sum", "avg", "values", "scalar":
		return columnReduce(r, name, args)
	}
	return nil, fmt.Errorf("unknown method")
}

func metaDict(r *table.Table) *starlark.Dict {
	d := starlark.NewDict(8)
	_ = d.SetKey(starlark.String("rows"), starlark.MakeInt(r.Len()))
	_ = d.SetKey(starlark.String("truncated"), starlark.Bool(r.Meta.Truncated))
	_ = d.SetKey(starlark.String("error"), starlark.String(r.Meta.Error))
	_ = d.SetKey(starlark.String("unavailable"), starlark.String(r.Meta.Unavailable))
	_ = d.SetKey(starlark.String("sensitivity"), starlark.String(r.Meta.Sensitivity))
	_ = d.SetKey(starlark.String("cached"), starlark.Bool(r.Meta.Cached))
	src := make([]starlark.Value, len(r.Meta.Source))
	for i, s := range r.Meta.Source {
		src[i] = starlark.String(s)
	}
	_ = d.SetKey(starlark.String("source"), starlark.NewList(src))
	return d
}

// columnReduce implements t.cpu.max(), t.first("pid"), t.scalar() ...
func columnReduce(r *table.Table, name string, args starlark.Tuple) (starlark.Value, error) {
	ci := 0
	if len(args) == 1 {
		s, ok := starlark.AsString(args[0])
		if !ok {
			return nil, fmt.Errorf("%s(column)", name)
		}
		ci = r.ColIndex(s)
		if ci < 0 {
			return nil, fmt.Errorf("no column %q", s)
		}
	} else if len(r.Schema) != 1 && name != "first" && name != "last" {
		return nil, fmt.Errorf("%s() on a table with %d columns needs a column name", name, len(r.Schema))
	}
	if len(r.Schema) == 0 {
		return starlark.None, nil
	}
	col := r.Schema[ci]
	switch name {
	case "first", "last":
		if r.Len() == 0 {
			return starlark.None, nil
		}
		row := r.Rows[0]
		if name == "last" {
			row = r.Rows[r.Len()-1]
		}
		if len(args) == 0 && len(r.Schema) > 1 {
			return &Row{t: r, row: row}, nil
		}
		return toStarlark(col, row[ci]), nil
	case "scalar":
		if r.Len() != 1 {
			return nil, fmt.Errorf("scalar() needs exactly one row, table has %d", r.Len())
		}
		return toStarlark(col, r.Rows[0][ci]), nil
	case "values":
		l := make([]starlark.Value, r.Len())
		for i, row := range r.Rows {
			l[i] = toStarlark(col, row[ci])
		}
		return starlark.NewList(l), nil
	}
	call := expr.Call{Fn: name, Args: []expr.Node{expr.Ident{Name: col.Name}}}
	v, err := expr.Aggregate(call, r, r.Rows, &expr.Env{})
	if err != nil {
		return nil, err
	}
	return toStarlark(table.Col{Type: v.T}, v.V), nil
}

func strArgs(args starlark.Tuple) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		s, ok := starlark.AsString(a)
		if !ok {
			return nil, fmt.Errorf("expected column names, got %s", a.Type())
		}
		out[i] = s
	}
	return out, nil
}

func strOrList(v starlark.Value) ([]string, error) {
	if s, ok := starlark.AsString(v); ok {
		return []string{s}, nil
	}
	if l, ok := v.(*starlark.List); ok {
		out := make([]string, l.Len())
		for i := 0; i < l.Len(); i++ {
			s, ok := starlark.AsString(l.Index(i))
			if !ok {
				return nil, fmt.Errorf("column names must be strings")
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected a column name or a list of names")
}

func asTable(v starlark.Value) (*table.Table, error) {
	t, ok := v.(*Table)
	if !ok {
		return nil, fmt.Errorf("expected a table, got %s", v.Type())
	}
	return t.Materialize()
}

func listArg(rt *Runtime, args starlark.Tuple, mode string) ([]expr.Item, map[string]any, error) {
	if len(args) != 1 {
		return nil, nil, fmt.Errorf("takes one expr(...) list")
	}
	switch p := args[0].(type) {
	case *Expr:
		return p.list(rt, mode)
	case starlark.String:
		items, err := expr.ParseList(string(p), mode)
		return items, nil, err
	}
	return nil, nil, fmt.Errorf("expected expr(...) or a string")
}

// pushFilter pushes the pushable conjuncts of e into the primitive's
// predicate/bound params. The full predicate still runs afterwards.
func (t *Table) pushFilter(e *Expr) (*Table, bool) {
	n, b, err := e.node(t.rt)
	if err != nil {
		return nil, false
	}
	pd := t.src.prim.Pushdown.Filter
	if pd == nil {
		return nil, false
	}
	params := copyParams(t.src.params)
	var pushed []string
	if pd.Predicate != "" {
		if _, set := params[pd.Predicate]; !set {
			spec := t.src.prim.Params[pd.Predicate]
			p, _ := expr.Split(n, spec.Dialect, spec.Fields, b)
			if p != nil {
				params[pd.Predicate] = expr.Pushable{Node: p, Bindings: b}
				pushed = append(pushed, pd.Predicate+": "+expr.String(p))
			}
		}
	}
	for _, bd := range pd.Bounds {
		if v, ok := boundValue(n, bd, b); ok {
			if _, set := params[bd.Param]; !set {
				params[bd.Param] = v
				pushed = append(pushed, fmt.Sprintf("%s: %s", bd.Param, table.Format(v)))
			}
		}
	}
	if len(pushed) == 0 {
		return nil, false
	}
	env := t.env()
	post := append(append([]func(*table.Table) (*table.Table, error){}, t.post...), func(r *table.Table) (*table.Table, error) {
		ev := *env
		ev.Bindings = b
		return expr.Filter(r, n, &ev)
	})
	return &Table{rt: t.rt, src: &source{kind: "primitive", name: t.src.name, prim: t.src.prim, params: params}, post: post, push: append(append([]string(nil), t.push...), pushed...)}, true
}

func boundValue(n expr.Node, bd catalog.BoundPushdown, b map[string]any) (any, bool) {
	var found any
	var walk func(expr.Node) bool
	walk = func(n expr.Node) bool {
		switch x := n.(type) {
		case expr.Binary:
			if x.Op == "and" {
				return walk(x.X) || walk(x.Y)
			}
			id, ok := x.X.(expr.Ident)
			if !ok || id.Name != bd.Column {
				return false
			}
			okOp := false
			for _, op := range bd.Ops {
				if op == x.Op {
					okOp = true
				}
			}
			if !okOp {
				return false
			}
			switch y := x.Y.(type) {
			case expr.Lit:
				found = y.Val
				return true
			case expr.Ident:
				if v, ok := b[y.Name]; ok {
					if tv, ok := v.(expr.Typed); ok {
						found = tv.V
					} else {
						found = v
					}
					return true
				}
			}
		}
		return false
	}
	if walk(n) {
		return found, true
	}
	return nil, false
}

func (t *Table) pushLimit(n int) (*Table, bool) {
	param := t.src.prim.Pushdown.Limit
	if param == "" || n < 1 {
		return nil, false
	}
	spec := t.src.prim.Params[param]
	if spec.Max != nil && float64(n) > *spec.Max {
		return nil, false
	}
	params := copyParams(t.src.params)
	if cur, ok := params[param]; ok {
		if c, ok := table.ToInt(cur); ok && c <= int64(n) {
			return nil, false
		}
	}
	params[param] = int64(n)
	post := append(append([]func(*table.Table) (*table.Table, error){}, t.post...), func(r *table.Table) (*table.Table, error) {
		return r.Limit(n, 0), nil
	})
	return &Table{rt: t.rt, src: &source{kind: "primitive", name: t.src.name, prim: t.src.prim, params: params}, post: post, push: append(append([]string(nil), t.push...), fmt.Sprintf("%s: %d", param, n))}, true
}

func copyParams(p map[string]any) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

// ---- Row -----------------------------------------------------------------------

// Row is one row of a table: r.pid, r["pid"].
type Row struct {
	t   *table.Table
	row []any
}

var (
	_ starlark.HasAttrs = (*Row)(nil)
	_ starlark.Mapping  = (*Row)(nil)
	_ expr.Record       = (*Row)(nil)
)

func (r *Row) String() string {
	parts := make([]string, len(r.t.Schema))
	for i, c := range r.t.Schema {
		parts[i] = c.Name + "=" + toStarlark(c, r.row[i]).String()
	}
	return "row(" + strings.Join(parts, ", ") + ")"
}
func (r *Row) Type() string          { return "row" }
func (r *Row) Freeze()               {}
func (r *Row) Truth() starlark.Bool  { return true }
func (r *Row) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: row") }

func (r *Row) Attr(name string) (starlark.Value, error) {
	if i := r.t.ColIndex(name); i >= 0 {
		return toStarlark(r.t.Schema[i], r.row[i]), nil
	}
	return nil, nil
}

func (r *Row) AttrNames() []string {
	out := make([]string, len(r.t.Schema))
	for i, c := range r.t.Schema {
		out[i] = c.Name
	}
	return out
}

func (r *Row) Get(k starlark.Value) (starlark.Value, bool, error) {
	s, ok := starlark.AsString(k)
	if !ok {
		return nil, false, fmt.Errorf("row keys are column names")
	}
	v, err := r.Attr(s)
	return v, v != nil, err
}

// Field implements expr.Record.
func (r *Row) Field(name string) (any, table.Type, bool) {
	if i := r.t.ColIndex(name); i >= 0 {
		return r.row[i], r.t.Schema[i].Type, true
	}
	return nil, "", false
}

// ---- Expr ----------------------------------------------------------------------

// Expr is a row expression built by expr("...", name=value, ...).
type Expr struct {
	src      string
	bindings map[string]starlark.Value
}

var _ starlark.Value = (*Expr)(nil)

func (e *Expr) String() string        { return fmt.Sprintf("expr(%q)", e.src) }
func (e *Expr) Type() string          { return "expr" }
func (e *Expr) Freeze()               {}
func (e *Expr) Truth() starlark.Bool  { return e.src != "" }
func (e *Expr) Hash() (uint32, error) { return starlark.String(e.src).Hash() }

// Binary lets the model write expr("a") and expr("b") without surprises.
func (e *Expr) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	return nil, fmt.Errorf("expressions are combined inside one expr(\"...\") string")
}

func (e *Expr) goBindings(rt *Runtime) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range e.bindings {
		b, err := exprBinding(v)
		if err != nil {
			return nil, fmt.Errorf("binding %s: %w", k, err)
		}
		out[k] = b
	}
	return out, nil
}

func (e *Expr) node(rt *Runtime) (expr.Node, map[string]any, error) {
	n, err := expr.Parse(e.src)
	if err != nil {
		return nil, nil, err
	}
	b, err := e.goBindings(rt)
	return n, b, err
}

func (e *Expr) list(rt *Runtime, mode string) ([]expr.Item, map[string]any, error) {
	items, err := expr.ParseList(e.src, mode)
	if err != nil {
		return nil, nil, err
	}
	b, err := e.goBindings(rt)
	return items, b, err
}

// Render implements argv.Predicate when an Expr is passed as a predicate param.
func (e *Expr) Render(dialect string, fields map[string]string) (string, error) {
	n, b, err := e.node(nil)
	if err != nil {
		return "", err
	}
	return expr.Pushable{Node: n, Bindings: b}.Render(dialect, fields)
}

func exprBinding(v starlark.Value) (any, error) {
	switch x := v.(type) {
	case Bytes:
		return expr.Typed{V: int64(x), T: table.Bytes}, nil
	case Duration:
		return expr.Typed{V: float64(x), T: table.Duration}, nil
	case Path:
		return expr.Typed{V: string(x), T: table.Path}, nil
	case Timestamp:
		return expr.Typed{V: x.T, T: table.TS}, nil
	case *Table:
		r, err := x.Materialize()
		if err != nil {
			return nil, err
		}
		return r, nil
	case *Row:
		return x, nil
	case *Expr:
		return nil, fmt.Errorf("an expression cannot be a binding")
	}
	return goValue(v), nil
}
