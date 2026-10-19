// Package expr is sysq's row expression language (design §13): the
// predicates and projections inside filter, select, sort, group and window.
// It is pure and total — no I/O, no loops, no user-defined functions.
package expr

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

// Node is an expression AST node.
type Node interface{ node() }

type (
	// Lit is a literal value (with its type).
	Lit struct {
		Val  any
		Type string // int, float, str, bool, null, duration, bytes, path, value
		Text string
		VT   table.Type // for Type "value": an already-evaluated typed value
	}
	// Ident names a column or a bound variable.
	Ident struct{ Name string }
	// Attr is x.name (on bound records/rows).
	Attr struct {
		X    Node
		Name string
	}
	// Index is x[i].
	Index struct{ X, I Node }
	// Unary is -x or not x.
	Unary struct {
		Op string
		X  Node
	}
	// Binary is x op y.
	Binary struct {
		Op   string // + - * / % == != < <= > >= and or in contains matches startswith endswith
		X, Y Node
		Not  bool // "not in"
	}
	// Call is f(args).
	Call struct {
		Fn   string
		Args []Node
		Over *Window // window functions only
	}
	// List is [a, b, c].
	List struct{ Items []Node }
)

// Window is a window specification.
type Window struct {
	Partition []Node
	Order     []SortKey
	Rows      int // 0 = unbounded preceding
}

// Item is one element of a projection/sort/aggregate list.
type Item struct {
	X    Node
	As   string
	Desc bool
	Src  string
}

// SortKey is one ordering key.
type SortKey struct {
	X    Node
	Desc bool
}

func (Lit) node()    {}
func (Ident) node()  {}
func (Attr) node()   {}
func (Index) node()  {}
func (Unary) node()  {}
func (Binary) node() {}
func (Call) node()   {}
func (List) node()   {}

// ---- tokenizer ---------------------------------------------------------------

type tok struct {
	kind string // num, str, ident, op, path, eof
	text string
	pos  int
}

var units = map[string]bool{
	"ms": true, "s": true, "m": true, "h": true, "d": true, "w": true,
	"b": true, "kb": true, "mb": true, "gb": true, "tb": true, "kib": true, "mib": true, "gib": true, "tib": true,
}

func lex(src string) ([]tok, error) {
	var out []tok
	i := 0
	prevValue := func() bool {
		if len(out) == 0 {
			return false
		}
		p := out[len(out)-1]
		return p.kind == "num" || p.kind == "str" || p.kind == "ident" || p.kind == "path" || p.text == ")" || p.text == "]"
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9' || (c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' || src[j] == '_') {
				j++
			}
			k := j
			for k < len(src) && unicode.IsLetter(rune(src[k])) {
				k++
			}
			if k > j && !units[strings.ToLower(src[j:k])] {
				return nil, fmt.Errorf("unknown unit %q at %d", src[j:k], j)
			}
			out = append(out, tok{"num", src[i:k], i})
			i = k
		case c == '"' || c == '\'':
			j := i + 1
			var b strings.Builder
			for j < len(src) && src[j] != c {
				if src[j] == '\\' && j+1 < len(src) {
					j++
					switch src[j] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(src[j])
					}
				} else {
					b.WriteByte(src[j])
				}
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unterminated string at %d", i)
			}
			out = append(out, tok{"str", b.String(), i})
			i = j + 1
		case (c == '~' || c == '/') && !prevValue():
			j := i
			for j < len(src) && isPathChar(src[j]) {
				j++
			}
			out = append(out, tok{"path", src[i:j], i})
			i = j
		case c == '_' || unicode.IsLetter(rune(c)):
			j := i
			for j < len(src) && (src[j] == '_' || unicode.IsLetter(rune(src[j])) || unicode.IsDigit(rune(src[j]))) {
				j++
			}
			out = append(out, tok{"ident", src[i:j], i})
			i = j
		default:
			two := ""
			if i+1 < len(src) {
				two = src[i : i+2]
			}
			switch two {
			case "==", "!=", "<=", ">=", "&&", "||", "??":
				out = append(out, tok{"op", two, i})
				i += 2
				continue
			}
			if strings.ContainsRune("+-*/%<>(),[].!", rune(c)) {
				out = append(out, tok{"op", string(c), i})
				i++
				continue
			}
			return nil, fmt.Errorf("unexpected %q at %d", c, i)
		}
	}
	out = append(out, tok{"eof", "", len(src)})
	return out, nil
}

func isPathChar(c byte) bool {
	return c == '/' || c == '~' || c == '.' || c == '_' || c == '-' || c == '@' || c == '+' || c == '%' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// ---- parser ------------------------------------------------------------------

type parser struct {
	src  string
	toks []tok
	i    int
}

func (p *parser) peek() tok { return p.toks[p.i] }
func (p *parser) next() tok {
	t := p.toks[p.i]
	if p.i < len(p.toks)-1 {
		p.i++
	}
	return t
}
func (p *parser) is(text string) bool {
	t := p.peek()
	return (t.kind == "op" || t.kind == "ident") && t.text == text
}
func (p *parser) accept(text string) bool {
	if p.is(text) {
		p.next()
		return true
	}
	return false
}
func (p *parser) expect(text string) error {
	if !p.accept(text) {
		return p.errf("expected %q", text)
	}
	return nil
}
func (p *parser) errf(format string, args ...any) error {
	t := p.peek()
	where := "end"
	if t.kind != "eof" {
		where = fmt.Sprintf("%q", t.text)
	}
	return fmt.Errorf("%s at %s (offset %d) in %q", fmt.Sprintf(format, args...), where, t.pos, p.src)
}

// Parse parses a single expression.
func Parse(src string) (Node, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks}
	n, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != "eof" {
		return nil, p.errf("unexpected trailing input")
	}
	return n, nil
}

// ParseList parses "a, b as c, x desc" style lists. mode is "select",
// "sort", "group" or "window".
func ParseList(src, mode string) ([]Item, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks}
	var items []Item
	if p.peek().kind == "eof" {
		return nil, nil
	}
	for {
		start := p.peek().pos
		x, err := p.expr()
		if err != nil {
			return nil, err
		}
		if mode == "window" {
			c, ok := x.(Call)
			if !ok {
				return nil, p.errf("window needs a function call")
			}
			if p.accept("over") {
				w, err := p.window()
				if err != nil {
					return nil, err
				}
				c.Over = w
			} else {
				c.Over = &Window{}
			}
			x = c
		}
		it := Item{X: x}
		if mode == "sort" {
			if p.accept("desc") {
				it.Desc = true
			} else {
				p.accept("asc")
			}
		}
		if p.accept("as") {
			t := p.next()
			if t.kind != "ident" {
				return nil, p.errf("expected a name after as")
			}
			it.As = t.text
		}
		it.Src = strings.TrimSpace(src[start:p.peek().pos])
		items = append(items, it)
		if !p.accept(",") {
			break
		}
	}
	if p.peek().kind != "eof" {
		return nil, p.errf("unexpected trailing input")
	}
	return items, nil
}

func (p *parser) window() (*Window, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	w := &Window{}
	if p.accept("partition") {
		if err := p.expect("by"); err != nil {
			return nil, err
		}
		for {
			x, err := p.add()
			if err != nil {
				return nil, err
			}
			w.Partition = append(w.Partition, x)
			if !p.accept(",") {
				break
			}
		}
	}
	if p.accept("order") {
		if err := p.expect("by"); err != nil {
			return nil, err
		}
		for {
			x, err := p.add()
			if err != nil {
				return nil, err
			}
			k := SortKey{X: x}
			if p.accept("desc") {
				k.Desc = true
			} else {
				p.accept("asc")
			}
			w.Order = append(w.Order, k)
			if !p.accept(",") {
				break
			}
		}
	}
	if p.accept("rows") {
		t := p.next()
		n, err := strconv.Atoi(t.text)
		if err != nil || n < 1 {
			return nil, p.errf("rows needs a positive integer")
		}
		w.Rows = n
	}
	return w, p.expect(")")
}

func (p *parser) expr() (Node, error) {
	x, err := p.or()
	if err != nil {
		return nil, err
	}
	for p.accept("??") {
		y, err := p.or()
		if err != nil {
			return nil, err
		}
		x = Call{Fn: "coalesce", Args: []Node{x, y}}
	}
	return x, nil
}

func (p *parser) or() (Node, error) {
	x, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.accept("or") || p.accept("||") {
		y, err := p.and()
		if err != nil {
			return nil, err
		}
		x = Binary{Op: "or", X: x, Y: y}
	}
	return x, nil
}

func (p *parser) and() (Node, error) {
	x, err := p.not()
	if err != nil {
		return nil, err
	}
	for p.accept("and") || p.accept("&&") {
		y, err := p.not()
		if err != nil {
			return nil, err
		}
		x = Binary{Op: "and", X: x, Y: y}
	}
	return x, nil
}

func (p *parser) not() (Node, error) {
	if p.accept("not") || p.accept("!") {
		x, err := p.not()
		if err != nil {
			return nil, err
		}
		return Unary{Op: "not", X: x}, nil
	}
	return p.cmp()
}

var cmpOps = map[string]bool{"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true,
	"in": true, "contains": true, "matches": true, "startswith": true, "endswith": true}

func (p *parser) cmp() (Node, error) {
	x, err := p.add()
	if err != nil {
		return nil, err
	}
	neg := false
	if p.is("not") && p.toks[p.i+1].text == "in" {
		p.next()
		neg = true
	}
	t := p.peek()
	if (t.kind == "op" || t.kind == "ident") && cmpOps[t.text] {
		p.next()
		if t.text == "=" {
			return nil, p.errf("use == for equality")
		}
		y, err := p.add()
		if err != nil {
			return nil, err
		}
		return Binary{Op: t.text, X: x, Y: y, Not: neg}, nil
	}
	if neg {
		return nil, p.errf("expected in after not")
	}
	return x, nil
}

func (p *parser) add() (Node, error) {
	x, err := p.mul()
	if err != nil {
		return nil, err
	}
	for p.is("+") || p.is("-") {
		op := p.next().text
		y, err := p.mul()
		if err != nil {
			return nil, err
		}
		x = Binary{Op: op, X: x, Y: y}
	}
	return x, nil
}

func (p *parser) mul() (Node, error) {
	x, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.is("*") || p.is("/") || p.is("%") {
		op := p.next().text
		y, err := p.unary()
		if err != nil {
			return nil, err
		}
		x = Binary{Op: op, X: x, Y: y}
	}
	return x, nil
}

func (p *parser) unary() (Node, error) {
	if p.accept("-") {
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return Unary{Op: "-", X: x}, nil
	}
	return p.postfix()
}

func (p *parser) postfix() (Node, error) {
	x, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.accept("."):
			t := p.next()
			if t.kind != "ident" {
				return nil, p.errf("expected a name after .")
			}
			x = Attr{X: x, Name: t.text}
		case p.accept("["):
			i, err := p.expr()
			if err != nil {
				return nil, err
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			x = Index{X: x, I: i}
		default:
			return x, nil
		}
	}
}

func (p *parser) primary() (Node, error) {
	t := p.next()
	switch t.kind {
	case "num":
		return numLit(t.text)
	case "str":
		return Lit{Val: t.text, Type: "str", Text: strconv.Quote(t.text)}, nil
	case "path":
		return Lit{Val: t.text, Type: "path", Text: t.text}, nil
	case "ident":
		switch t.text {
		case "true", "True":
			return Lit{Val: true, Type: "bool", Text: "true"}, nil
		case "false", "False":
			return Lit{Val: false, Type: "bool", Text: "false"}, nil
		case "null", "None", "none":
			return Lit{Val: nil, Type: "null", Text: "null"}, nil
		}
		if p.accept("(") {
			c := Call{Fn: t.text}
			if !p.accept(")") {
				for {
					a, err := p.expr()
					if err != nil {
						return nil, err
					}
					c.Args = append(c.Args, a)
					if p.accept(")") {
						break
					}
					if err := p.expect(","); err != nil {
						return nil, err
					}
				}
			}
			if !knownFunc(c.Fn) {
				return nil, fmt.Errorf("unknown function %s() in %q", c.Fn, p.src)
			}
			return c, nil
		}
		return Ident{Name: t.text}, nil
	case "op":
		switch t.text {
		case "(":
			x, err := p.expr()
			if err != nil {
				return nil, err
			}
			return x, p.expect(")")
		case "[":
			l := List{}
			if p.accept("]") {
				return l, nil
			}
			for {
				x, err := p.expr()
				if err != nil {
					return nil, err
				}
				l.Items = append(l.Items, x)
				if p.accept("]") {
					return l, nil
				}
				if err := p.expect(","); err != nil {
					return nil, err
				}
			}
		}
	}
	p.i--
	return nil, p.errf("expected a value")
}

func numLit(text string) (Node, error) {
	s := strings.ReplaceAll(text, "_", "")
	j := len(s)
	for j > 0 && unicode.IsLetter(rune(s[j-1])) {
		j--
	}
	num, unit := s[:j], strings.ToLower(s[j:])
	switch unit {
	case "":
		if strings.ContainsAny(num, ".eE") {
			f, err := strconv.ParseFloat(num, 64)
			return Lit{Val: f, Type: "float", Text: text}, err
		}
		n, err := strconv.ParseInt(num, 10, 64)
		return Lit{Val: n, Type: "int", Text: text}, err
	case "ms", "s", "m", "h", "d", "w":
		sec, err := argv.ParseDuration(num + unit)
		return Lit{Val: sec, Type: "duration", Text: text}, err
	default:
		n, err := argv.ParseBytes(num + unit)
		return Lit{Val: n, Type: "bytes", Text: text}, err
	}
}

// Idents returns the free identifiers an expression refers to (the roots of
// attribute chains), in first-use order.
func Idents(n Node) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(Node)
	walk = func(n Node) {
		switch x := n.(type) {
		case Ident:
			if !seen[x.Name] {
				seen[x.Name] = true
				out = append(out, x.Name)
			}
		case Attr:
			walk(x.X)
		case Index:
			walk(x.X)
			walk(x.I)
		case Unary:
			walk(x.X)
		case Binary:
			walk(x.X)
			walk(x.Y)
		case Call:
			for _, a := range x.Args {
				walk(a)
			}
			if x.Over != nil {
				for _, p := range x.Over.Partition {
					walk(p)
				}
				for _, k := range x.Over.Order {
					walk(k.X)
				}
			}
		case List:
			for _, it := range x.Items {
				walk(it)
			}
		}
	}
	walk(n)
	return out
}

// ListIdents returns free identifiers across list items.
func ListIdents(items []Item) []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		for _, id := range Idents(it.X) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}
