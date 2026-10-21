package expr

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// Env resolves names during evaluation.
type Env struct {
	Table    *table.Table
	Row      []any
	Bindings map[string]any
	Now      time.Time
	Home     string
}

// Value is an evaluated value with its sysq type.
type Value struct {
	V any
	T table.Type
}

var scalarFuncs = map[string]bool{
	"contains": true, "matches": true, "startswith": true, "endswith": true, "coalesce": true,
	"now": true, "ago": true, "humanize": true, "basename": true, "dirname": true,
	"lower": true, "upper": true, "len": true, "abs": true, "round": true, "floor": true, "ceil": true,
	"str": true, "int": true, "float": true, "if": true, "concat": true, "trim": true, "replace": true,
	"split_first": true, "ext": true, "seconds": true, "hours": true, "minutes": true, "days": true,
	"greatest": true, "least": true, "pct": true, "is_null": true,
}

var aggFuncs = map[string]bool{
	"count": true, "sum": true, "avg": true, "min": true, "max": true, "p50": true, "p90": true,
	"p95": true, "p99": true, "any": true, "all": true, "list": true, "first": true, "last": true,
	"count_distinct": true,
}

var windowFuncs = map[string]bool{
	"lag": true, "lead": true, "rank": true, "row_number": true, "running_sum": true, "delta": true, "rate": true,
}

func knownFunc(name string) bool { return scalarFuncs[name] || aggFuncs[name] || windowFuncs[name] }

// IsAggregate reports whether a function name aggregates rows.
func IsAggregate(name string) bool { return aggFuncs[name] }

// IsWindow reports whether a function name is a window-only function.
func IsWindow(name string) bool { return windowFuncs[name] }

// Eval evaluates n against one row.
func Eval(n Node, env *Env) (Value, error) {
	switch x := n.(type) {
	case Lit:
		switch x.Type {
		case "value":
			return Value{x.Val, x.VT}, nil
		case "int":
			return Value{x.Val, table.Int}, nil
		case "float":
			return Value{x.Val, table.Float}, nil
		case "str":
			return Value{x.Val, table.Str}, nil
		case "bool":
			return Value{x.Val, table.Bool}, nil
		case "duration":
			return Value{x.Val, table.Duration}, nil
		case "bytes":
			return Value{x.Val, table.Bytes}, nil
		case "path":
			s := x.Val.(string)
			if env.Home != "" {
				if s == "~" {
					s = env.Home
				} else if strings.HasPrefix(s, "~/") {
					s = filepath.Join(env.Home, s[2:])
				}
			}
			return Value{s, table.Path}, nil
		}
		return Value{nil, table.Str}, nil
	case Ident:
		// A column of the input wins over a script variable of the same name.
		if env.Table != nil {
			if i := env.Table.ColIndex(x.Name); i >= 0 {
				var v any
				if i < len(env.Row) {
					v = env.Row[i]
				}
				return Value{v, env.Table.Schema[i].Type}, nil
			}
		}
		if v, ok := env.Bindings[x.Name]; ok {
			return bound(v), nil
		}
		if env.Table != nil {
			return Value{}, fmt.Errorf("unknown column %q (columns: %s)", x.Name, colNames(env.Table))
		}
		return Value{}, fmt.Errorf("unknown name %q", x.Name)
	case Attr:
		base, err := Eval(x.X, env)
		if err != nil {
			return Value{}, err
		}
		switch b := base.V.(type) {
		case map[string]any:
			v, ok := b[x.Name]
			if !ok {
				return Value{}, fmt.Errorf("no field %q", x.Name)
			}
			return bound(v), nil
		case Record:
			v, t, ok := b.Field(x.Name)
			if !ok {
				return Value{}, fmt.Errorf("no field %q", x.Name)
			}
			return Value{v, t}, nil
		case *table.Table:
			i := b.ColIndex(x.Name)
			if i < 0 {
				return Value{}, fmt.Errorf("no column %q", x.Name)
			}
			if b.Len() == 1 {
				return Value{b.Rows[0][i], b.Schema[i].Type}, nil
			}
			col := table.New([]table.Col{b.Schema[i]})
			for _, r := range b.Rows {
				col.Append(r[i])
			}
			return Value{col, table.TableT}, nil
		case nil:
			return Value{nil, table.Str}, nil
		}
		return Value{}, fmt.Errorf("cannot read .%s of %T", x.Name, base.V)
	case Index:
		base, err := Eval(x.X, env)
		if err != nil {
			return Value{}, err
		}
		idx, err := Eval(x.I, env)
		if err != nil {
			return Value{}, err
		}
		switch b := base.V.(type) {
		case []any:
			i, ok := table.ToInt(idx.V)
			if !ok || i < 0 || int(i) >= len(b) {
				return Value{nil, table.Str}, nil
			}
			return bound(b[i]), nil
		case map[string]any:
			return bound(b[table.Format(idx.V)]), nil
		case string:
			i, ok := table.ToInt(idx.V)
			if !ok || i < 0 || int(i) >= len(b) {
				return Value{nil, table.Str}, nil
			}
			return Value{string(b[i]), table.Str}, nil
		}
		return Value{nil, table.Str}, nil
	case Unary:
		v, err := Eval(x.X, env)
		if err != nil {
			return Value{}, err
		}
		if x.Op == "not" {
			return Value{!table.Truthy(v.V), table.Bool}, nil
		}
		if f, ok := table.ToFloat(v.V); ok {
			if v.T.IsIntegral() {
				return Value{-int64(f), v.T}, nil
			}
			return Value{-f, v.T}, nil
		}
		return Value{nil, v.T}, nil
	case Binary:
		return evalBinary(x, env)
	case Call:
		return evalCall(x, env)
	case List:
		out := make([]any, len(x.Items))
		for i, it := range x.Items {
			v, err := Eval(it, env)
			if err != nil {
				return Value{}, err
			}
			out[i] = v.V
		}
		return Value{out, table.JSON}, nil
	}
	return Value{}, fmt.Errorf("cannot evaluate %T", n)
}

// Record is a bound value with typed fields (Starlark rows).
type Record interface {
	Field(name string) (any, table.Type, bool)
}

// Typed lets bindings carry a sysq type (Bytes, Duration, Path...).
type Typed struct {
	V any
	T table.Type
}

func bound(v any) Value {
	switch x := v.(type) {
	case Typed:
		return Value(x)
	case nil:
		return Value{nil, table.Str}
	case int64:
		return Value{x, table.Int}
	case int:
		return Value{int64(x), table.Int}
	case float64:
		return Value{x, table.Float}
	case string:
		return Value{x, table.Str}
	case bool:
		return Value{x, table.Bool}
	case time.Time:
		return Value{x, table.TS}
	case *table.Table:
		return Value{x, table.TableT}
	}
	return Value{v, table.JSON}
}

func colNames(t *table.Table) string {
	var n []string
	for _, c := range t.Schema {
		n = append(n, c.Name)
	}
	return strings.Join(n, ", ")
}

func evalBinary(x Binary, env *Env) (Value, error) {
	switch x.Op {
	case "and":
		l, err := Eval(x.X, env)
		if err != nil {
			return Value{}, err
		}
		if !table.Truthy(l.V) {
			return Value{false, table.Bool}, nil
		}
		r, err := Eval(x.Y, env)
		if err != nil {
			return Value{}, err
		}
		return Value{table.Truthy(r.V), table.Bool}, nil
	case "or":
		l, err := Eval(x.X, env)
		if err != nil {
			return Value{}, err
		}
		if table.Truthy(l.V) {
			return Value{true, table.Bool}, nil
		}
		r, err := Eval(x.Y, env)
		if err != nil {
			return Value{}, err
		}
		return Value{table.Truthy(r.V), table.Bool}, nil
	}
	l, err := Eval(x.X, env)
	if err != nil {
		return Value{}, err
	}
	r, err := Eval(x.Y, env)
	if err != nil {
		return Value{}, err
	}
	switch x.Op {
	case "==", "!=", "<", "<=", ">", ">=":
		if l.V == nil || r.V == nil {
			eq := l.V == nil && r.V == nil
			switch x.Op {
			case "==":
				return Value{eq, table.Bool}, nil
			case "!=":
				return Value{!eq, table.Bool}, nil
			}
			return Value{false, table.Bool}, nil
		}
		c := compare(l, r)
		var b bool
		switch x.Op {
		case "==":
			b = c == 0
		case "!=":
			b = c != 0
		case "<":
			b = c < 0
		case "<=":
			b = c <= 0
		case ">":
			b = c > 0
		case ">=":
			b = c >= 0
		}
		return Value{b, table.Bool}, nil
	case "in":
		b := member(l, r)
		if x.Not {
			b = !b
		}
		return Value{b, table.Bool}, nil
	case "contains":
		return Value{strContains(l.V, r.V), table.Bool}, nil
	case "startswith":
		return Value{strings.HasPrefix(table.Format(l.V), table.Format(r.V)), table.Bool}, nil
	case "endswith":
		return Value{strings.HasSuffix(table.Format(l.V), table.Format(r.V)), table.Bool}, nil
	case "matches":
		re, err := cachedRegexp(table.Format(r.V))
		if err != nil {
			return Value{}, err
		}
		return Value{re.MatchString(table.Format(l.V)), table.Bool}, nil
	case "+", "-", "*", "/", "%":
		return arith(x.Op, l, r)
	}
	return Value{}, fmt.Errorf("unknown operator %s", x.Op)
}

func compare(l, r Value) int {
	// A ts compared with a duration literal means "older than": now - d.
	if lt, ok := l.V.(time.Time); ok {
		if rt, ok := r.V.(time.Time); ok {
			return lt.Compare(rt)
		}
	}
	return table.Compare(l.V, r.V)
}

func member(l, r Value) bool {
	switch c := r.V.(type) {
	case []any:
		for _, x := range c {
			if table.Compare(l.V, x) == 0 {
				return true
			}
		}
	case *table.Table:
		for _, row := range c.Rows {
			if len(row) > 0 && table.Compare(l.V, row[0]) == 0 {
				return true
			}
		}
	case map[string]any:
		_, ok := c[table.Format(l.V)]
		return ok
	case string:
		return strings.Contains(c, table.Format(l.V))
	}
	return false
}

func strContains(hay, needle any) bool {
	switch h := hay.(type) {
	case []any:
		for _, x := range h {
			if table.Compare(x, needle) == 0 {
				return true
			}
		}
		return false
	}
	return strings.Contains(strings.ToLower(table.Format(hay)), strings.ToLower(table.Format(needle)))
}

var reCache sync.Map

func cachedRegexp(s string) (*regexp.Regexp, error) {
	if re, ok := reCache.Load(s); ok {
		return re.(*regexp.Regexp), nil
	}
	if len(s) > 512 {
		return nil, fmt.Errorf("regular expression too long")
	}
	re, err := regexp.Compile(s)
	if err != nil {
		return nil, err
	}
	reCache.Store(s, re)
	return re, nil
}

func arith(op string, l, r Value) (Value, error) {
	if l.V == nil || r.V == nil {
		return Value{nil, l.T}, nil
	}
	// time arithmetic
	if lt, ok := l.V.(time.Time); ok {
		if rt, ok := r.V.(time.Time); ok && op == "-" {
			return Value{lt.Sub(rt).Seconds(), table.Duration}, nil
		}
		if d, ok := table.ToFloat(r.V); ok {
			delta := time.Duration(d * float64(time.Second))
			switch op {
			case "+":
				return Value{lt.Add(delta), table.TS}, nil
			case "-":
				return Value{lt.Add(-delta), table.TS}, nil
			}
		}
		return Value{}, fmt.Errorf("cannot %s a timestamp", op)
	}
	if op == "+" {
		if ls, ok := l.V.(string); ok {
			return Value{ls + table.Format(r.V), table.Str}, nil
		}
	}
	lf, ok1 := table.ToFloat(l.V)
	rf, ok2 := table.ToFloat(r.V)
	if !ok1 || !ok2 {
		return Value{}, fmt.Errorf("cannot %s %v and %v", op, l.V, r.V)
	}
	t := arithType(op, l.T, r.T)
	var f float64
	switch op {
	case "+":
		f = lf + rf
	case "-":
		f = lf - rf
	case "*":
		f = lf * rf
	case "/":
		if rf == 0 {
			return Value{nil, table.Float}, nil
		}
		f = lf / rf
	case "%":
		if rf == 0 {
			return Value{nil, t}, nil
		}
		f = math.Mod(lf, rf)
	}
	if t.IsIntegral() {
		return Value{int64(math.Round(f)), t}, nil
	}
	return Value{f, t}, nil
}

func arithType(op string, a, b table.Type) table.Type {
	unitful := func(t table.Type) bool { return t == table.Bytes || t == table.Duration }
	switch op {
	case "/":
		if unitful(a) && !unitful(b) {
			return a
		}
		return table.Float
	case "*":
		if unitful(a) {
			return a
		}
		if unitful(b) {
			return b
		}
	case "+", "-", "%":
		if unitful(a) {
			return a
		}
		if unitful(b) {
			return b
		}
	}
	if a.IsIntegral() && b.IsIntegral() {
		return table.Int
	}
	return table.Float
}

func evalCall(c Call, env *Env) (Value, error) {
	if aggFuncs[c.Fn] && !(c.Fn == "min" || c.Fn == "max") {
		return Value{}, fmt.Errorf("%s() is an aggregate; use it inside group { ... }", c.Fn)
	}
	if windowFuncs[c.Fn] {
		return Value{}, fmt.Errorf("%s() is a window function; use it in window ... over (...)", c.Fn)
	}
	args := make([]Value, len(c.Args))
	for i, a := range c.Args {
		v, err := Eval(a, env)
		if err != nil {
			return Value{}, err
		}
		args[i] = v
	}
	need := func(n int) error {
		if len(args) != n {
			return fmt.Errorf("%s() takes %d argument(s), got %d", c.Fn, n, len(args))
		}
		return nil
	}
	str := func(i int) string { return table.Format(args[i].V) }
	switch c.Fn {
	case "now":
		return Value{env.now(), table.TS}, nil
	case "ago":
		if err := need(1); err != nil {
			return Value{}, err
		}
		d, _ := table.ToFloat(args[0].V)
		return Value{env.now().Add(-time.Duration(d * float64(time.Second))), table.TS}, nil
	case "contains", "startswith", "endswith", "matches":
		if err := need(2); err != nil {
			return Value{}, err
		}
		return evalBinary(Binary{Op: c.Fn, X: c.Args[0], Y: c.Args[1]}, env)
	case "coalesce":
		for _, a := range args {
			if a.V != nil && !(a.T == table.Str && a.V == "") {
				return a, nil
			}
		}
		if len(args) > 0 {
			return Value{nil, args[0].T}, nil
		}
		return Value{nil, table.Str}, nil
	case "is_null":
		if err := need(1); err != nil {
			return Value{}, err
		}
		return Value{args[0].V == nil, table.Bool}, nil
	case "humanize":
		if err := need(1); err != nil {
			return Value{}, err
		}
		return Value{table.Humanize(table.Col{Type: args[0].T}, args[0].V), table.Str}, nil
	case "basename":
		if err := need(1); err != nil {
			return Value{}, err
		}
		return Value{filepath.Base(str(0)), table.Str}, nil
	case "dirname":
		if err := need(1); err != nil {
			return Value{}, err
		}
		return Value{filepath.Dir(str(0)), table.Path}, nil
	case "ext":
		if err := need(1); err != nil {
			return Value{}, err
		}
		return Value{strings.TrimPrefix(strings.ToLower(filepath.Ext(str(0))), "."), table.Str}, nil
	case "lower", "upper", "trim", "str":
		if err := need(1); err != nil {
			return Value{}, err
		}
		s := str(0)
		switch c.Fn {
		case "lower":
			s = strings.ToLower(s)
		case "upper":
			s = strings.ToUpper(s)
		case "trim":
			s = strings.TrimSpace(s)
		}
		return Value{s, table.Str}, nil
	case "concat":
		var b strings.Builder
		for i := range args {
			b.WriteString(str(i))
		}
		return Value{b.String(), table.Str}, nil
	case "replace":
		if err := need(3); err != nil {
			return Value{}, err
		}
		return Value{strings.ReplaceAll(str(0), str(1), str(2)), table.Str}, nil
	case "split_first":
		if err := need(2); err != nil {
			return Value{}, err
		}
		a, _, _ := strings.Cut(str(0), str(1))
		return Value{a, table.Str}, nil
	case "len":
		if err := need(1); err != nil {
			return Value{}, err
		}
		switch v := args[0].V.(type) {
		case []any:
			return Value{int64(len(v)), table.Int}, nil
		case *table.Table:
			return Value{int64(v.Len()), table.Int}, nil
		}
		return Value{int64(len(str(0))), table.Int}, nil
	case "int":
		if err := need(1); err != nil {
			return Value{}, err
		}
		f, ok := table.ToFloat(args[0].V)
		if !ok {
			var n int64
			if _, err := fmt.Sscan(str(0), &n); err != nil {
				return Value{nil, table.Int}, nil
			}
			return Value{n, table.Int}, nil
		}
		return Value{int64(f), table.Int}, nil
	case "float":
		if err := need(1); err != nil {
			return Value{}, err
		}
		f, ok := table.ToFloat(args[0].V)
		if !ok {
			if _, err := fmt.Sscan(str(0), &f); err != nil {
				return Value{nil, table.Float}, nil
			}
		}
		return Value{f, table.Float}, nil
	case "abs", "floor", "ceil":
		if err := need(1); err != nil {
			return Value{}, err
		}
		f, ok := table.ToFloat(args[0].V)
		if !ok {
			return Value{nil, args[0].T}, nil
		}
		switch c.Fn {
		case "abs":
			f = math.Abs(f)
		case "floor":
			f = math.Floor(f)
		case "ceil":
			f = math.Ceil(f)
		}
		if args[0].T.IsIntegral() {
			return Value{int64(f), args[0].T}, nil
		}
		return Value{f, args[0].T}, nil
	case "round":
		if len(args) != 1 && len(args) != 2 {
			return Value{}, fmt.Errorf("round() takes 1 or 2 arguments, got %d", len(args))
		}
		f, ok := table.ToFloat(args[0].V)
		if !ok {
			return Value{nil, args[0].T}, nil
		}
		digits := 0.0
		if len(args) > 1 {
			digits, _ = table.ToFloat(args[1].V)
		}
		m := math.Pow(10, digits)
		r := math.Round(f*m) / m
		if args[0].T.IsIntegral() {
			return Value{int64(r), args[0].T}, nil
		}
		return Value{r, table.Float}, nil
	case "pct":
		if err := need(2); err != nil {
			return Value{}, err
		}
		a, ok1 := table.ToFloat(args[0].V)
		b, ok2 := table.ToFloat(args[1].V)
		if !ok1 || !ok2 || b == 0 {
			return Value{nil, table.Float}, nil
		}
		return Value{math.Round(a/b*1000) / 10, table.Float}, nil
	case "seconds", "minutes", "hours", "days":
		if err := need(1); err != nil {
			return Value{}, err
		}
		f, ok := table.ToFloat(args[0].V)
		if !ok {
			return Value{nil, table.Float}, nil
		}
		div := map[string]float64{"seconds": 1, "minutes": 60, "hours": 3600, "days": 86400}[c.Fn]
		return Value{f / div, table.Float}, nil
	case "if":
		if err := need(3); err != nil {
			return Value{}, err
		}
		if table.Truthy(args[0].V) {
			return args[1], nil
		}
		return args[2], nil
	case "greatest", "least", "min", "max":
		if len(args) < 2 {
			return Value{}, fmt.Errorf("%s() of one value: use it inside group { ... }", c.Fn)
		}
		best := args[0]
		for _, a := range args[1:] {
			cmp := table.Compare(a.V, best.V)
			if (c.Fn == "greatest" || c.Fn == "max") && cmp > 0 || (c.Fn == "least" || c.Fn == "min") && cmp < 0 {
				best = a
			}
		}
		return best, nil
	}
	return Value{}, fmt.Errorf("unknown function %s()", c.Fn)
}

func (e *Env) now() time.Time {
	if e.Now.IsZero() {
		return time.Now()
	}
	return e.Now
}

// ---- aggregates --------------------------------------------------------------

// Aggregate evaluates an aggregate call over the rows of a group.
func Aggregate(c Call, t *table.Table, rows [][]any, base *Env) (Value, error) {
	vals := func() ([]Value, error) {
		if len(c.Args) == 0 {
			return nil, nil
		}
		out := make([]Value, 0, len(rows))
		for _, r := range rows {
			env := *base
			env.Table, env.Row = t, r
			v, err := Eval(c.Args[0], &env)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	vs, err := vals()
	if err != nil {
		return Value{}, err
	}
	typ := table.Float
	if len(vs) > 0 {
		typ = vs[0].T
	}
	nonNil := func() []Value {
		var out []Value
		for _, v := range vs {
			if v.V != nil {
				out = append(out, v)
			}
		}
		return out
	}
	switch c.Fn {
	case "count":
		if len(c.Args) == 0 {
			return Value{int64(len(rows)), table.Int}, nil
		}
		n := int64(0)
		for _, v := range vs {
			if v.V != nil && (v.T != table.Bool || v.V == true) {
				n++
			}
		}
		return Value{n, table.Int}, nil
	case "count_distinct":
		seen := map[string]bool{}
		for _, v := range nonNil() {
			seen[table.Format(v.V)] = true
		}
		return Value{int64(len(seen)), table.Int}, nil
	case "sum", "avg":
		sum, n := 0.0, 0
		for _, v := range nonNil() {
			if f, ok := table.ToFloat(v.V); ok {
				sum += f
				n++
			}
		}
		if c.Fn == "avg" {
			if n == 0 {
				return Value{nil, table.Float}, nil
			}
			avgT := typ
			if avgT.IsIntegral() && avgT != table.Bytes {
				avgT = table.Float
			}
			if avgT == table.Bytes {
				return Value{int64(sum / float64(n)), avgT}, nil
			}
			return Value{sum / float64(n), avgT}, nil
		}
		if typ.IsIntegral() {
			return Value{int64(sum), typ}, nil
		}
		return Value{sum, typ}, nil
	case "min", "max":
		nn := nonNil()
		if len(nn) == 0 {
			return Value{nil, typ}, nil
		}
		best := nn[0]
		for _, v := range nn[1:] {
			cmp := table.Compare(v.V, best.V)
			if c.Fn == "max" && cmp > 0 || c.Fn == "min" && cmp < 0 {
				best = v
			}
		}
		return best, nil
	case "p50", "p90", "p95", "p99":
		var fs []float64
		for _, v := range nonNil() {
			if f, ok := table.ToFloat(v.V); ok {
				fs = append(fs, f)
			}
		}
		if len(fs) == 0 {
			return Value{nil, typ}, nil
		}
		sort.Float64s(fs)
		q := map[string]float64{"p50": .5, "p90": .9, "p95": .95, "p99": .99}[c.Fn]
		f := fs[int(math.Min(float64(len(fs)-1), math.Floor(q*float64(len(fs)))))]
		if typ.IsIntegral() {
			return Value{int64(f), typ}, nil
		}
		return Value{f, typ}, nil
	case "any", "all":
		res := c.Fn == "all"
		for _, v := range vs {
			b := table.Truthy(v.V)
			if c.Fn == "any" && b {
				return Value{true, table.Bool}, nil
			}
			if c.Fn == "all" && !b {
				return Value{false, table.Bool}, nil
			}
		}
		return Value{res, table.Bool}, nil
	case "first", "last":
		if len(vs) == 0 {
			return Value{nil, typ}, nil
		}
		if c.Fn == "first" {
			return vs[0], nil
		}
		return vs[len(vs)-1], nil
	case "list":
		col := table.Col{Name: "value", Type: typ}
		if id, ok := c.Args[0].(Ident); ok {
			col.Name = id.Name
			if tc, ok := t.Col(id.Name); ok {
				col = tc
			}
		}
		sub := table.New([]table.Col{col})
		for _, v := range vs {
			sub.Append(v.V)
		}
		return Value{sub, table.TableT}, nil
	}
	return Value{}, fmt.Errorf("unknown aggregate %s()", c.Fn)
}

// HasAggregate reports whether n contains an aggregate call.
func HasAggregate(n Node) bool {
	found := false
	var walk func(Node)
	walk = func(n Node) {
		switch x := n.(type) {
		case Call:
			if aggFuncs[x.Fn] && !(len(x.Args) >= 2 && (x.Fn == "min" || x.Fn == "max")) {
				found = true
			}
			for _, a := range x.Args {
				walk(a)
			}
		case Binary:
			walk(x.X)
			walk(x.Y)
		case Unary:
			walk(x.X)
		}
	}
	walk(n)
	return found
}

// EvalAgg evaluates an expression that may combine aggregates
// (e.g. sum(size) / count()).
func EvalAgg(n Node, t *table.Table, rows [][]any, base *Env) (Value, error) {
	switch x := n.(type) {
	case Call:
		if aggFuncs[x.Fn] && !(len(x.Args) >= 2 && (x.Fn == "min" || x.Fn == "max")) {
			return Aggregate(x, t, rows, base)
		}
		args := make([]Node, len(x.Args))
		for i, a := range x.Args {
			v, err := EvalAgg(a, t, rows, base)
			if err != nil {
				return Value{}, err
			}
			args[i] = Lit{Val: v.V, Type: "value", VT: v.T}
		}
		return Eval(Call{Fn: x.Fn, Args: args}, base)
	case Binary:
		l, err := EvalAgg(x.X, t, rows, base)
		if err != nil {
			return Value{}, err
		}
		r, err := EvalAgg(x.Y, t, rows, base)
		if err != nil {
			return Value{}, err
		}
		return evalBinary(Binary{Op: x.Op, X: Lit{Val: l.V, Type: "value", VT: l.T}, Y: Lit{Val: r.V, Type: "value", VT: r.T}, Not: x.Not}, base)
	case Unary:
		v, err := EvalAgg(x.X, t, rows, base)
		if err != nil {
			return Value{}, err
		}
		return Eval(Unary{Op: x.Op, X: Lit{Val: v.V, Type: "value", VT: v.T}}, base)
	}
	// Non-aggregate: take it from the first row (group keys).
	env := *base
	env.Table = t
	if len(rows) > 0 {
		env.Row = rows[0]
	}
	return Eval(n, &env)
}
