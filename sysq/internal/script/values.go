// Package script embeds Starlark (design §17): native Table, Row, Expr,
// Duration, Bytes, Path and Timestamp types, the catalog as namespaced
// builtins, emit, fuel and the cost ledger. Nothing here performs I/O
// except through engine primitives, which are read-only and sandboxed.
package script

import (
	"fmt"
	"math"
	"strings"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

// ---- Duration ------------------------------------------------------------------

// Duration is a span of time in seconds.
type Duration float64

var (
	_ starlark.Value      = Duration(0)
	_ starlark.HasBinary  = Duration(0)
	_ starlark.Comparable = Duration(0)
)

func (d Duration) String() string        { return table.HumanDuration(float64(d)) }
func (d Duration) Type() string          { return "duration" }
func (d Duration) Freeze()               {}
func (d Duration) Truth() starlark.Bool  { return d != 0 }
func (d Duration) Hash() (uint32, error) { return starlark.Float(d).Hash() }
func (d Duration) Seconds() float64      { return float64(d) }

func (d Duration) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	return starlark.CompareDepth(op, starlark.Float(d), starlark.Float(y.(Duration)), depth)
}

func (d Duration) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	switch yv := y.(type) {
	case Duration:
		switch op {
		case syntax.PLUS:
			return d + yv, nil
		case syntax.MINUS:
			if side == starlark.Left {
				return d - yv, nil
			}
			return yv - d, nil
		case syntax.SLASH:
			if side == starlark.Left {
				return starlark.Float(float64(d) / float64(yv)), nil
			}
			return starlark.Float(float64(yv) / float64(d)), nil
		}
	case starlark.Int, starlark.Float:
		f, _ := starlark.AsFloat(yv)
		switch op {
		case syntax.STAR:
			return d * Duration(f), nil
		case syntax.SLASH:
			if side == starlark.Left && f != 0 {
				return d / Duration(f), nil
			}
		}
	case Timestamp:
		if op == syntax.PLUS {
			return yv.Add(d), nil
		}
		if op == syntax.MINUS && side == starlark.Right {
			return yv.Add(-d), nil
		}
	}
	return nil, nil
}

// ---- Bytes ---------------------------------------------------------------------

// Bytes is a size in bytes.
type Bytes int64

var (
	_ starlark.HasBinary  = Bytes(0)
	_ starlark.Comparable = Bytes(0)
)

func (b Bytes) String() string        { return table.HumanBytes(int64(b)) }
func (b Bytes) Type() string          { return "bytes" }
func (b Bytes) Freeze()               {}
func (b Bytes) Truth() starlark.Bool  { return b != 0 }
func (b Bytes) Hash() (uint32, error) { return starlark.MakeInt64(int64(b)).Hash() }
func (b Bytes) ByteCount() int64      { return int64(b) }

func (b Bytes) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	return starlark.CompareDepth(op, starlark.MakeInt64(int64(b)), starlark.MakeInt64(int64(y.(Bytes))), depth)
}

func (b Bytes) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	switch yv := y.(type) {
	case Bytes:
		switch op {
		case syntax.PLUS:
			return b + yv, nil
		case syntax.MINUS:
			if side == starlark.Left {
				return b - yv, nil
			}
			return yv - b, nil
		case syntax.SLASH:
			if side == starlark.Left {
				if yv == 0 {
					return nil, fmt.Errorf("division by zero bytes")
				}
				return starlark.Float(float64(b) / float64(yv)), nil
			}
		}
	case starlark.Int, starlark.Float:
		f, _ := starlark.AsFloat(yv)
		switch op {
		case syntax.STAR:
			return Bytes(math.Round(float64(b) * f)), nil
		case syntax.SLASH:
			if side == starlark.Left && f != 0 {
				return Bytes(math.Round(float64(b) / f)), nil
			}
		}
	}
	return nil, nil
}

// ---- Path ----------------------------------------------------------------------

// Path is a filesystem path (validated when passed to a primitive).
type Path string

var _ starlark.HasAttrs = Path("")

func (p Path) String() string        { return string(p) }
func (p Path) Type() string          { return "path" }
func (p Path) Freeze()               {}
func (p Path) Truth() starlark.Bool  { return p != "" }
func (p Path) Hash() (uint32, error) { return starlark.String(p).Hash() }

func (p Path) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	return starlark.CompareDepth(op, starlark.String(p), starlark.String(y.(Path)), depth)
}

func (p Path) Attr(name string) (starlark.Value, error) {
	s := string(p)
	switch name {
	case "name":
		i := strings.LastIndexByte(strings.TrimRight(s, "/"), '/')
		return starlark.String(strings.TrimRight(s, "/")[i+1:]), nil
	case "parent":
		t := strings.TrimRight(s, "/")
		i := strings.LastIndexByte(t, '/')
		if i <= 0 {
			return Path("/"), nil
		}
		return Path(t[:i]), nil
	}
	return nil, nil
}

func (p Path) AttrNames() []string { return []string{"name", "parent"} }

// Binary supports path / "child".
func (p Path) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	if op == syntax.SLASH && side == starlark.Left {
		if s, ok := starlark.AsString(y); ok {
			return Path(strings.TrimRight(string(p), "/") + "/" + strings.TrimLeft(s, "/")), nil
		}
	}
	return nil, nil
}

// ---- Timestamp -----------------------------------------------------------------

// Timestamp is a point in time.
type Timestamp struct{ T time.Time }

var _ starlark.HasBinary = Timestamp{}

func (t Timestamp) String() string        { return t.T.Format(time.RFC3339) }
func (t Timestamp) Type() string          { return "timestamp" }
func (t Timestamp) Freeze()               {}
func (t Timestamp) Truth() starlark.Bool  { return starlark.Bool(!t.T.IsZero()) }
func (t Timestamp) Hash() (uint32, error) { return starlark.MakeInt64(t.T.UnixNano()).Hash() }

// Add returns t + d.
func (t Timestamp) Add(d Duration) Timestamp {
	return Timestamp{t.T.Add(time.Duration(float64(d) * float64(time.Second)))}
}

func (t Timestamp) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	c := t.T.Compare(y.(Timestamp).T)
	return starlark.CompareDepth(op, starlark.MakeInt(c), starlark.MakeInt(0), depth)
}

func (t Timestamp) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	switch yv := y.(type) {
	case Timestamp:
		if op == syntax.MINUS {
			if side == starlark.Left {
				return Duration(t.T.Sub(yv.T).Seconds()), nil
			}
			return Duration(yv.T.Sub(t.T).Seconds()), nil
		}
	case Duration:
		if op == syntax.PLUS {
			return t.Add(yv), nil
		}
		if op == syntax.MINUS && side == starlark.Left {
			return t.Add(-yv), nil
		}
	}
	return nil, nil
}

// ---- constructors ----------------------------------------------------------------

func durationCtor(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	sec, err := argv.ParseDuration(goValue(v))
	if err != nil {
		return nil, fmt.Errorf("Duration: %v", err)
	}
	return Duration(sec), nil
}

func bytesCtor(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	n, err := argv.ParseBytes(goValue(v))
	if err != nil {
		return nil, fmt.Errorf("Bytes: %v", err)
	}
	return Bytes(n), nil
}

func pathCtor(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &s); err != nil {
		return nil, err
	}
	if strings.ContainsRune(s, 0) {
		return nil, fmt.Errorf("Path: NUL in path")
	}
	return Path(s), nil
}

// ---- conversions -----------------------------------------------------------------

// toStarlark converts a cell to a Starlark value using its column type.
func toStarlark(col table.Col, v any) starlark.Value {
	if v == nil {
		return starlark.None
	}
	switch col.Type {
	case table.Bytes:
		if n, ok := table.ToInt(v); ok {
			return Bytes(n)
		}
	case table.Duration:
		if f, ok := table.ToFloat(v); ok {
			return Duration(f)
		}
	case table.Path:
		if s, ok := v.(string); ok {
			return Path(s)
		}
	}
	return fromGo(v)
}

// fromGo converts plain Go values.
func fromGo(v any) starlark.Value {
	switch x := v.(type) {
	case nil:
		return starlark.None
	case bool:
		return starlark.Bool(x)
	case int64:
		return starlark.MakeInt64(x)
	case int:
		return starlark.MakeInt(x)
	case float64:
		return starlark.Float(x)
	case string:
		return starlark.String(x)
	case time.Time:
		return Timestamp{x}
	case *table.Table:
		return newTable(x)
	case []any:
		l := make([]starlark.Value, len(x))
		for i, e := range x {
			l[i] = fromGo(e)
		}
		return starlark.NewList(l)
	case map[string]any:
		d := starlark.NewDict(len(x))
		for k, e := range x {
			_ = d.SetKey(starlark.String(k), fromGo(e))
		}
		return d
	}
	return starlark.String(fmt.Sprint(v))
}

// goValue converts a Starlark value into a Go value for params and bindings.
// Typed sysq values keep their type via expr.Typed or the argv interfaces.
func goValue(v starlark.Value) any {
	switch x := v.(type) {
	case starlark.NoneType:
		return nil
	case starlark.Bool:
		return bool(x)
	case starlark.Int:
		if n, ok := x.Int64(); ok {
			return n
		}
		f, _ := starlark.AsFloat(x)
		return f
	case starlark.Float:
		return float64(x)
	case starlark.String:
		return string(x)
	case Duration, Bytes:
		return x
	case Path:
		return string(x)
	case Timestamp:
		return x.T
	case *Table:
		return x
	case *starlark.List:
		out := make([]any, x.Len())
		for i := 0; i < x.Len(); i++ {
			out[i] = goValue(x.Index(i))
		}
		return out
	case starlark.Tuple:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = goValue(e)
		}
		return out
	case *starlark.Dict:
		out := map[string]any{}
		for _, it := range x.Items() {
			k, _ := starlark.AsString(it[0])
			out[k] = goValue(it[1])
		}
		return out
	case *Row:
		return x
	}
	return v.String()
}
