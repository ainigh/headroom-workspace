// Package plan explains a script without running it (design §14.5, §19.1):
// the lowered Starlark, primitives and recipes it can reach, derived cost
// class and sensitivity, and the predicates the planner would push down.
package plan

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.starlark.net/syntax"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/expr"
	"github.com/ainigh/headroom-workspace/sysq/internal/sugar"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// RecipeFacts are a recipe's derived facts.
type RecipeFacts struct {
	Cost        string
	Sensitivity string
	Primitives  []string
	Units       int
	Network     bool
}

// Resolver looks up names a script can call.
type Resolver interface {
	Primitive(name string) (*catalog.Primitive, bool)
	Recipe(name string) (RecipeFacts, bool)
}

// Plan is the result of explain.
type Plan struct {
	Lowered     string   `json:"lowered"`
	Primitives  []string `json:"primitives"`
	Recipes     []string `json:"recipes,omitempty"`
	Cost        string   `json:"cost"`        // cheap | moderate | expensive
	Units       int      `json:"cost_units"`  // ledger units, loops counted once per row cap estimate
	Sensitivity string   `json:"sensitivity"` // public | personal | secret
	Network     bool     `json:"network,omitempty"`
	Pushdown    []string `json:"pushdown,omitempty"`
	Loops       int      `json:"loops,omitempty"`
	Unknown     []string `json:"unknown,omitempty"`
	Schema      any      `json:"schema,omitempty"`
}

// loopFactor is how many calls a call inside a for loop is assumed to make
// when estimating cost units.
const loopFactor = 10

// Explain lowers src (detecting syntax) and analyzes it.
func Explain(src string, r Resolver) (*Plan, error) {
	lowered, err := sugar.Lower(src, sugar.Detect(src))
	if err != nil {
		return nil, err
	}
	return Analyze(lowered, r)
}

// Analyze inspects already-lowered Starlark.
func Analyze(lowered string, r Resolver) (*Plan, error) {
	opts := &syntax.FileOptions{TopLevelControl: true, GlobalReassign: true, Set: true}
	f, err := opts.Parse("script.star", lowered, 0)
	if err != nil {
		return nil, err
	}
	p := &Plan{Lowered: lowered, Cost: "cheap", Sensitivity: "public"}
	a := &analyzer{p: p, r: r, prims: map[string]bool{}, recipes: map[string]bool{}, unknown: map[string]bool{}, locals: map[string]bool{}}
	collectLocals(f, a.locals)
	for _, st := range f.Stmts {
		a.stmt(st, 1)
	}
	p.Primitives = keys(a.prims)
	p.Recipes = keys(a.recipes)
	p.Unknown = keys(a.unknown)
	if len(p.Primitives) == 0 && len(p.Recipes) == 0 {
		p.Cost = "cheap"
	}
	return p, nil
}

type analyzer struct {
	locals  map[string]bool // names a script binds itself (they shadow namespaces)
	p       *Plan
	r       Resolver
	prims   map[string]bool
	recipes map[string]bool
	unknown map[string]bool
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (a *analyzer) stmt(s syntax.Stmt, mult int) {
	switch x := s.(type) {
	case *syntax.ExprStmt:
		a.expr(x.X, mult)
	case *syntax.AssignStmt:
		a.expr(x.RHS, mult)
	case *syntax.ReturnStmt:
		if x.Result != nil {
			a.expr(x.Result, mult)
		}
	case *syntax.IfStmt:
		a.expr(x.Cond, mult)
		for _, b := range x.True {
			a.stmt(b, mult)
		}
		for _, b := range x.False {
			a.stmt(b, mult)
		}
	case *syntax.ForStmt:
		a.p.Loops++
		a.expr(x.X, mult)
		for _, b := range x.Body {
			a.stmt(b, mult*loopFactor)
		}
	case *syntax.DefStmt:
		for _, b := range x.Body {
			a.stmt(b, mult)
		}
	case *syntax.LoadStmt:
	}
}

// dotted returns "a.b.c" for an identifier/dot chain.
func dotted(e syntax.Expr) (string, bool) {
	switch x := e.(type) {
	case *syntax.Ident:
		return x.Name, true
	case *syntax.DotExpr:
		base, ok := dotted(x.X)
		if !ok {
			return "", false
		}
		return base + "." + x.Name.Name, true
	}
	return "", false
}

func (a *analyzer) name(n string, mult int) bool {
	if a.locals[strings.SplitN(n, ".", 2)[0]] {
		return false
	}
	n = strings.TrimPrefix(n, "recipes.")
	if p, ok := a.r.Primitive(n); ok {
		a.prims[n] = true
		a.charge(p.Cost, catalog.CostUnits[p.Cost]*mult, p.Sensitivity, p.Network)
		return true
	}
	if f, ok := a.r.Recipe(n); ok {
		a.recipes[n] = true
		for _, pn := range f.Primitives {
			a.prims[pn] = true
		}
		a.charge(f.Cost, f.Units*mult, f.Sensitivity, f.Network)
		return true
	}
	return false
}

func (a *analyzer) charge(cost string, units int, sens string, network bool) {
	if catalog.CostRank(cost) > catalog.CostRank(a.p.Cost) {
		a.p.Cost = cost
	}
	a.p.Units += units
	a.p.Sensitivity = table.MaxSensitivity(a.p.Sensitivity, sens)
	a.p.Network = a.p.Network || network
}

func (a *analyzer) expr(e syntax.Expr, mult int) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *syntax.DotExpr:
		if n, ok := dotted(x); ok {
			if a.name(n, mult) {
				return
			}
			// t.col / method on a value: analyze the receiver
			if strings.Count(n, ".") >= 1 && looksLikeCatalog(n) {
				head := n[:strings.LastIndex(n, ".")]
				if !a.name(head, mult) && looksLikeCatalog(head) && !strings.HasPrefix(n, "recipes.") {
					a.maybeUnknown(n)
				}
			}
			return
		}
		a.expr(x.X, mult)
	case *syntax.CallExpr:
		if d, ok := x.Fn.(*syntax.DotExpr); ok {
			a.pushdown(d, x)
		}
		a.expr(x.Fn, mult)
		for _, arg := range x.Args {
			a.expr(arg, mult)
		}
	case *syntax.BinaryExpr:
		a.expr(x.X, mult)
		a.expr(x.Y, mult)
	case *syntax.UnaryExpr:
		a.expr(x.X, mult)
	case *syntax.ParenExpr:
		a.expr(x.X, mult)
	case *syntax.ListExpr:
		for _, it := range x.List {
			a.expr(it, mult)
		}
	case *syntax.TupleExpr:
		for _, it := range x.List {
			a.expr(it, mult)
		}
	case *syntax.DictExpr:
		for _, it := range x.List {
			if de, ok := it.(*syntax.DictEntry); ok {
				a.expr(de.Value, mult)
			}
		}
	case *syntax.CondExpr:
		a.expr(x.Cond, mult)
		a.expr(x.True, mult)
		a.expr(x.False, mult)
	case *syntax.LambdaExpr:
		a.expr(x.Body, mult)
	case *syntax.IndexExpr:
		a.expr(x.X, mult)
		a.expr(x.Y, mult)
	case *syntax.Comprehension:
		a.expr(x.Body, mult*loopFactor)
		for _, c := range x.Clauses {
			if fc, ok := c.(*syntax.ForClause); ok {
				a.expr(fc.X, mult)
			}
		}
	}
}

var namespaces = map[string]bool{}

// SetNamespaces tells the analyzer which first segments are catalog namespaces
// (so a typo like proc.tpo is reported).
func SetNamespaces(ns []string) {
	for _, n := range ns {
		namespaces[n] = true
	}
}

func looksLikeCatalog(n string) bool {
	return namespaces[strings.SplitN(n, ".", 2)[0]] || strings.HasPrefix(n, "recipes.")
}

func (a *analyzer) maybeUnknown(n string) {
	if !a.locals[strings.SplitN(n, ".", 2)[0]] {
		a.unknown[n] = true
	}
}

// collectLocals records every name the file assigns, loops over, defines
// or takes as a parameter.
func collectLocals(f *syntax.File, out map[string]bool) {
	var target func(e syntax.Expr)
	target = func(e syntax.Expr) {
		switch x := e.(type) {
		case *syntax.Ident:
			out[x.Name] = true
		case *syntax.TupleExpr:
			for _, it := range x.List {
				target(it)
			}
		case *syntax.ListExpr:
			for _, it := range x.List {
				target(it)
			}
		case *syntax.ParenExpr:
			target(x.X)
		}
	}
	var stmts func([]syntax.Stmt)
	stmts = func(ss []syntax.Stmt) {
		for _, s := range ss {
			switch x := s.(type) {
			case *syntax.AssignStmt:
				target(x.LHS)
			case *syntax.ForStmt:
				target(x.Vars)
				stmts(x.Body)
			case *syntax.IfStmt:
				stmts(x.True)
				stmts(x.False)
			case *syntax.DefStmt:
				out[x.Name.Name] = true
				for _, p := range x.Params {
					switch pp := p.(type) {
					case *syntax.Ident:
						out[pp.Name] = true
					case *syntax.BinaryExpr:
						target(pp.X)
					case *syntax.UnaryExpr:
						target(pp.X)
					}
				}
				stmts(x.Body)
			case *syntax.LoadStmt:
				for _, id := range x.To {
					out[id.Name] = true
				}
			}
		}
	}
	stmts(f.Stmts)
}

// pushdown reports filter(expr("...")) / limit(n) applied directly to a
// primitive call that can take them.
func (a *analyzer) pushdown(method *syntax.DotExpr, call *syntax.CallExpr) {
	recv := method.X
	// Skip earlier pushable stages: prim(...).filter(...).limit(n).
	for {
		c, ok := recv.(*syntax.CallExpr)
		if !ok {
			break
		}
		d, ok := c.Fn.(*syntax.DotExpr)
		if !ok || (d.Name.Name != "filter" && d.Name.Name != "limit" && d.Name.Name != "head") {
			recv = c.Fn
			break
		}
		recv = d.X
	}
	n, ok := dotted(recv)
	if !ok {
		return
	}
	p, ok := a.r.Primitive(n)
	if !ok || p.Pushdown == nil {
		return
	}
	switch method.Name.Name {
	case "filter":
		if p.Pushdown.Filter == nil || len(call.Args) != 1 {
			return
		}
		src, ok := exprLiteral(call.Args[0])
		if !ok {
			return
		}
		node, err := expr.Parse(src)
		if err != nil {
			return
		}
		if pd := p.Pushdown.Filter.Predicate; pd != "" {
			spec := p.Params[pd]
			if pushed, _ := expr.Split(node, spec.Dialect, spec.Fields, nil); pushed != nil {
				text, err := expr.Pushable{Node: pushed}.Render(spec.Dialect, spec.Fields)
				if err == nil {
					a.p.Pushdown = append(a.p.Pushdown, fmt.Sprintf("%s: %s --%s %s", n, pd, spec.Dialect, text))
				}
			}
		}
		for _, b := range p.Pushdown.Filter.Bounds {
			if strings.Contains(src, b.Column) {
				a.p.Pushdown = append(a.p.Pushdown, fmt.Sprintf("%s: %s from %s", n, b.Param, b.Column))
			}
		}
	case "limit", "head":
		if p.Pushdown.Limit != "" && len(call.Args) >= 1 {
			if lit, ok := call.Args[0].(*syntax.Literal); ok {
				a.p.Pushdown = append(a.p.Pushdown, fmt.Sprintf("%s: %s=%v", n, p.Pushdown.Limit, lit.Value))
			}
		}
	}
}

func exprLiteral(e syntax.Expr) (string, bool) {
	c, ok := e.(*syntax.CallExpr)
	if !ok {
		return "", false
	}
	if id, ok := c.Fn.(*syntax.Ident); !ok || id.Name != "expr" || len(c.Args) < 1 {
		return "", false
	}
	lit, ok := c.Args[0].(*syntax.Literal)
	if !ok || lit.Token != syntax.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Raw)
	if err != nil {
		s, _ = lit.Value.(string)
	}
	return s, true
}

// CatalogResolver resolves primitives only.
type CatalogResolver struct{ Catalog *catalog.Catalog }

// Primitive implements Resolver.
func (c CatalogResolver) Primitive(n string) (*catalog.Primitive, bool) { return c.Catalog.Get(n) }

// Recipe implements Resolver.
func (c CatalogResolver) Recipe(string) (RecipeFacts, bool) { return RecipeFacts{}, false }
