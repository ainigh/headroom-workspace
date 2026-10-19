package expr

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// String renders canonical source for a node.
func String(n Node) string {
	switch x := n.(type) {
	case Lit:
		if x.Text != "" {
			return x.Text
		}
		switch v := x.Val.(type) {
		case string:
			return strconv.Quote(v)
		case nil:
			return "null"
		}
		return table.Format(x.Val)
	case Ident:
		return x.Name
	case Attr:
		return String(x.X) + "." + x.Name
	case Index:
		return String(x.X) + "[" + String(x.I) + "]"
	case Unary:
		if x.Op == "not" {
			return "not " + String(x.X)
		}
		return "-" + String(x.X)
	case Binary:
		op := x.Op
		if x.Not {
			op = "not " + op
		}
		return "(" + String(x.X) + " " + op + " " + String(x.Y) + ")"
	case Call:
		args := make([]string, len(x.Args))
		for i, a := range x.Args {
			args[i] = String(a)
		}
		return x.Fn + "(" + strings.Join(args, ", ") + ")"
	case List:
		items := make([]string, len(x.Items))
		for i, a := range x.Items {
			items[i] = String(a)
		}
		return "[" + strings.Join(items, ", ") + "]"
	}
	return fmt.Sprintf("%v", n)
}

// InferType guesses the result type of n over a schema (for select/group
// output columns). Unknown → str.
func InferType(n Node, schema []table.Col, bindings map[string]any) table.Col {
	find := func(name string) (table.Col, bool) {
		for _, c := range schema {
			if c.Name == name {
				return c, true
			}
		}
		return table.Col{}, false
	}
	switch x := n.(type) {
	case Lit:
		switch x.Type {
		case "int":
			return table.Col{Type: table.Int}
		case "float":
			return table.Col{Type: table.Float}
		case "bool":
			return table.Col{Type: table.Bool}
		case "duration":
			return table.Col{Type: table.Duration}
		case "bytes":
			return table.Col{Type: table.Bytes}
		case "path":
			return table.Col{Type: table.Path}
		case "value":
			return table.Col{Type: x.VT}
		}
		return table.Col{Type: table.Str}
	case Ident:
		if c, ok := find(x.Name); ok {
			c.Name = ""
			return c
		}
		if v, ok := bindings[x.Name]; ok {
			return table.Col{Type: bound(v).T}
		}
	case Unary:
		if x.Op == "not" {
			return table.Col{Type: table.Bool}
		}
		return InferType(x.X, schema, bindings)
	case Binary:
		switch x.Op {
		case "+", "-", "*", "/", "%":
			l := InferType(x.X, schema, bindings)
			r := InferType(x.Y, schema, bindings)
			if l.Type == table.TS && r.Type == table.TS && x.Op == "-" {
				return table.Col{Type: table.Duration}
			}
			if l.Type == table.TS {
				return table.Col{Type: table.TS}
			}
			if l.Type == table.Str && x.Op == "+" {
				return table.Col{Type: table.Str}
			}
			return table.Col{Type: arithType(x.Op, l.Type, r.Type), Unit: unitOf(l, r, x.Op)}
		}
		return table.Col{Type: table.Bool}
	case Call:
		switch x.Fn {
		case "count", "count_distinct", "len", "int":
			return table.Col{Type: table.Int}
		case "avg", "pct", "float", "seconds", "minutes", "hours", "days":
			if x.Fn == "avg" && len(x.Args) == 1 {
				c := InferType(x.Args[0], schema, bindings)
				if c.Type == table.Bytes || c.Type == table.Duration || c.Type == table.Float {
					c.Name = ""
					return c
				}
			}
			u := ""
			if x.Fn == "pct" {
				u = "%"
			}
			return table.Col{Type: table.Float, Unit: u}
		case "sum", "min", "max", "p50", "p90", "p95", "p99", "first", "last", "coalesce", "abs", "round", "floor", "ceil", "greatest", "least", "lag", "lead", "running_sum", "delta":
			if len(x.Args) > 0 {
				c := InferType(x.Args[0], schema, bindings)
				c.Name = ""
				return c
			}
		case "rate":
			return table.Col{Type: table.Float, Unit: "/s"}
		case "rank", "row_number":
			return table.Col{Type: table.Int}
		case "any", "all", "contains", "startswith", "endswith", "matches", "is_null":
			return table.Col{Type: table.Bool}
		case "now", "ago":
			return table.Col{Type: table.TS}
		case "dirname":
			return table.Col{Type: table.Path}
		case "list":
			sub := InferType(x.Args[0], schema, bindings)
			if id, ok := x.Args[0].(Ident); ok {
				sub.Name = id.Name
			} else {
				sub.Name = "value"
			}
			return table.Col{Type: table.TableT, Schema: []table.Col{sub}}
		case "if":
			if len(x.Args) == 3 {
				return InferType(x.Args[1], schema, bindings)
			}
		}
		return table.Col{Type: table.Str}
	}
	return table.Col{Type: table.Str}
}

func unitOf(l, r table.Col, op string) string {
	if op == "/" && l.Unit == r.Unit {
		return ""
	}
	if l.Unit != "" {
		return l.Unit
	}
	return r.Unit
}

// DefaultName names an output column for an unaliased projection.
func DefaultName(n Node) string {
	switch x := n.(type) {
	case Ident:
		return x.Name
	case Attr:
		return x.Name
	case Call:
		if len(x.Args) == 1 {
			if id, ok := x.Args[0].(Ident); ok {
				return x.Fn + "_" + id.Name
			}
		}
		return x.Fn
	}
	s := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '_'
	}, String(n))
	s = strings.Trim(s, "_")
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	if s == "" {
		return "expr"
	}
	return s
}

// ---- pushdown rendering ------------------------------------------------------

// Pushable wraps an expression for a predicate parameter; it implements
// argv.Predicate so only structured, escaped text reaches argv.
type Pushable struct {
	Node     Node
	Bindings map[string]any
}

// Render implements argv.Predicate.
func (p Pushable) Render(dialect string, fields map[string]string) (string, error) {
	switch dialect {
	case "nspredicate":
		return nsPredicate(p.Node, fields, p.Bindings)
	case "mdquery":
		return mdQuery(p.Node, fields, p.Bindings)
	}
	return "", fmt.Errorf("unknown predicate dialect %q", dialect)
}

func (p Pushable) String() string { return String(p.Node) }

// Split separates the conjuncts of n that can render in a dialect from the
// rest. Either side may be nil.
func Split(n Node, dialect string, fields map[string]string, bindings map[string]any) (pushed, residual Node) {
	var conj []Node
	var flatten func(Node)
	flatten = func(n Node) {
		if b, ok := n.(Binary); ok && b.Op == "and" {
			flatten(b.X)
			flatten(b.Y)
			return
		}
		conj = append(conj, n)
	}
	flatten(n)
	join := func(a, b Node) Node {
		if a == nil {
			return b
		}
		return Binary{Op: "and", X: a, Y: b}
	}
	for _, c := range conj {
		if _, err := (Pushable{Node: c, Bindings: bindings}).Render(dialect, fields); err == nil {
			pushed = join(pushed, c)
		} else {
			residual = join(residual, c)
		}
	}
	return pushed, residual
}

func constValue(n Node, bindings map[string]any) (any, bool) {
	switch x := n.(type) {
	case Lit:
		return x.Val, true
	case Ident:
		if v, ok := bindings[x.Name]; ok {
			return bound(v).V, true
		}
	case List:
		out := make([]any, 0, len(x.Items))
		for _, it := range x.Items {
			v, ok := constValue(it, bindings)
			if !ok {
				return nil, false
			}
			out = append(out, v)
		}
		return out, true
	}
	return nil, false
}

func nsQuote(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(x) + `"`, nil
	case int64, float64:
		return table.Format(x), nil
	case bool:
		if x {
			return "TRUE", nil
		}
		return "FALSE", nil
	}
	return "", fmt.Errorf("cannot render %T in a predicate", v)
}

var logLevels = map[string]bool{"default": true, "info": true, "debug": true, "error": true, "fault": true}

func nsPredicate(n Node, fields map[string]string, bindings map[string]any) (string, error) {
	switch x := n.(type) {
	case Binary:
		switch x.Op {
		case "and", "or":
			l, err := nsPredicate(x.X, fields, bindings)
			if err != nil {
				return "", err
			}
			r, err := nsPredicate(x.Y, fields, bindings)
			if err != nil {
				return "", err
			}
			return "(" + l + " " + strings.ToUpper(x.Op) + " " + r + ")", nil
		}
		id, ok := x.X.(Ident)
		if !ok {
			return "", fmt.Errorf("left side must be a column")
		}
		field, ok := fields[id.Name]
		if !ok {
			return "", fmt.Errorf("column %s cannot be pushed", id.Name)
		}
		if yid, isIdent := x.Y.(Ident); isIdent {
			if _, isCol := fields[yid.Name]; isCol {
				return "", fmt.Errorf("right side is a column")
			}
		}
		v, ok := constValue(x.Y, bindings)
		if !ok {
			return "", fmt.Errorf("right side must be a constant")
		}
		if field == "messageType" {
			s, ok := v.(string)
			if !ok || !logLevels[s] || (x.Op != "==" && x.Op != "!=") {
				return "", fmt.Errorf("level compares with ==/!= against a level name")
			}
			return field + " " + x.Op + " " + s, nil
		}
		switch x.Op {
		case "==", "!=", "<", "<=", ">", ">=":
			q, err := nsQuote(v)
			if err != nil {
				return "", err
			}
			return field + " " + x.Op + " " + q, nil
		case "contains", "startswith", "endswith", "matches":
			s, ok := v.(string)
			if !ok {
				return "", fmt.Errorf("%s needs a string", x.Op)
			}
			q, _ := nsQuote(s)
			op := map[string]string{"contains": "CONTAINS[c]", "startswith": "BEGINSWITH", "endswith": "ENDSWITH", "matches": "MATCHES"}[x.Op]
			return field + " " + op + " " + q, nil
		case "in":
			list, ok := v.([]any)
			if !ok || len(list) == 0 {
				return "", fmt.Errorf("in needs a constant list")
			}
			parts := make([]string, len(list))
			for i, it := range list {
				q, err := nsQuote(it)
				if err != nil {
					return "", err
				}
				parts[i] = q
			}
			s := field + " IN {" + strings.Join(parts, ", ") + "}"
			if x.Not {
				s = "NOT (" + s + ")"
			}
			return s, nil
		}
	case Unary:
		if x.Op == "not" {
			s, err := nsPredicate(x.X, fields, bindings)
			if err != nil {
				return "", err
			}
			return "NOT (" + s + ")", nil
		}
	}
	return "", fmt.Errorf("not pushable: %s", String(n))
}

func mdQuery(n Node, fields map[string]string, bindings map[string]any) (string, error) {
	switch x := n.(type) {
	case Binary:
		switch x.Op {
		case "and", "or":
			l, err := mdQuery(x.X, fields, bindings)
			if err != nil {
				return "", err
			}
			r, err := mdQuery(x.Y, fields, bindings)
			if err != nil {
				return "", err
			}
			op := "&&"
			if x.Op == "or" {
				op = "||"
			}
			return "(" + l + " " + op + " " + r + ")", nil
		}
		id, ok := x.X.(Ident)
		if !ok {
			return "", fmt.Errorf("left side must be a column")
		}
		field, ok := fields[id.Name]
		if !ok {
			return "", fmt.Errorf("column %s cannot be pushed", id.Name)
		}
		v, ok := constValue(x.Y, bindings)
		if !ok {
			return "", fmt.Errorf("right side must be a constant")
		}
		esc := func(s string) string {
			return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `*`, `\*`, `'`, `\'`).Replace(s)
		}
		switch x.Op {
		case "==":
			if s, ok := v.(string); ok {
				return field + ` == "` + esc(s) + `"c`, nil
			}
			return field + " == " + table.Format(v), nil
		case "contains":
			s, _ := v.(string)
			return field + ` == "*` + esc(s) + `*"cd`, nil
		case "startswith":
			s, _ := v.(string)
			return field + ` == "` + esc(s) + `*"cd`, nil
		case "endswith":
			s, _ := v.(string)
			return field + ` == "*` + esc(s) + `"cd`, nil
		case ">", ">=", "<", "<=":
			if f, ok := table.ToFloat(v); ok {
				return field + " " + x.Op + " " + table.Format(f), nil
			}
		}
	}
	return "", fmt.Errorf("not pushable: %s", String(n))
}
