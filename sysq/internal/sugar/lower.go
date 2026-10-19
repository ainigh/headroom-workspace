package sugar

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ainigh/headroom-workspace/sysq/internal/expr"
)

// Mode selects the input syntax.
type Mode int

const (
	// Script is sysq-script: let, { } blocks, fn, emit (design §14.2).
	Script Mode = iota
	// Star is Starlark with expression sugar (recipes, design §19).
	Star
)

// Error is a lowering error with a line number.
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

type lowerer struct {
	src  string
	toks []token
	mode Mode
	// scope returns the variable names visible at a token index.
	scope func(k int) map[string]bool
	// stmtDepth is the bracket depth of the current statement; newlines and
	// comments at this depth inside an expression are line continuations.
	stmtDepth int
}

func (l *lowerer) errAt(k int, format string, args ...any) error {
	line := 0
	if k >= 0 && k < len(l.toks) {
		line = l.toks[k].line
	}
	return &Error{Line: line, Msg: fmt.Sprintf(format, args...)}
}

// gap is the source text between token k-1 and token k (whitespace).
func (l *lowerer) gap(k int) string {
	if k <= 0 || k >= len(l.toks) {
		return ""
	}
	g := l.src[l.toks[k-1].end:l.toks[k].start]
	return strings.ReplaceAll(g, "\\\n", " ")
}

func (l *lowerer) text(k int) string { return l.src[l.toks[k].start:l.toks[k].end] }

func (l *lowerer) is(k int, s string) bool {
	return k < len(l.toks) && (l.toks[k].k == kOp || l.toks[k].k == kIdent) && l.toks[k].text == s
}

// match returns the index of the bracket closing the one at k.
func (l *lowerer) match(k int) int {
	d := l.toks[k].depth
	for j := k + 1; j < len(l.toks); j++ {
		if l.toks[j].k == kOp && l.toks[j].depth == d && (l.toks[j].text == ")" || l.toks[j].text == "]" || l.toks[j].text == "}") {
			return j
		}
	}
	return len(l.toks) - 1
}

// skipTrivia advances past comments and (inside brackets) newlines.
func (l *lowerer) significant(a, b int) []int {
	var out []int
	for k := a; k < b; k++ {
		if l.toks[k].k != kComment && l.toks[k].k != kNewline {
			out = append(out, k)
		}
	}
	return out
}

// raw returns tokens a..b as compact source text (comments and newlines dropped),
// for embedding inside expr("...").
func (l *lowerer) raw(a, b int) string {
	var sb strings.Builder
	prev := -1
	for _, k := range l.significant(a, b) {
		if prev >= 0 && l.toks[k].start > l.toks[prev].end {
			sb.WriteByte(' ')
		}
		sb.WriteString(l.text(k))
		prev = k
	}
	return sb.String()
}

var stageWords = map[string]bool{
	"filter": true, "select": true, "sort": true, "limit": true, "head": true, "tail": true,
	"sample": true, "distinct": true, "group": true, "join": true, "union": true, "except": true,
	"intersect": true, "diff": true, "nest": true, "unnest": true, "pivot": true, "unpivot": true,
	"window": true,
}

var listStages = map[string]bool{"select": true, "sort": true, "distinct": true, "window": true, "pivot": true, "unpivot": true, "join": true, "diff": true, "nest": true, "group": true}

// stageAt reports whether token k is a pipe that starts a pipeline stage.
func (l *lowerer) stageAt(k int) bool {
	if !l.is(k, "|") {
		return false
	}
	n := k + 1
	for n < len(l.toks) && (l.toks[n].k == kNewline || l.toks[n].k == kComment) {
		n++
	}
	if n >= len(l.toks) || l.toks[n].k != kIdent {
		return false
	}
	w := l.toks[n].text
	if stageWords[w] {
		return true
	}
	if w == "left" || w == "inner" || w == "anti" {
		return l.is(l.nextSig(n), "join")
	}
	return false
}

func (l *lowerer) nextSig(k int) int {
	n := k + 1
	for n < len(l.toks) && (l.toks[n].k == kNewline || l.toks[n].k == kComment) {
		n++
	}
	return n
}

var boundaryWords = map[string]bool{
	"return": true, "if": true, "elif": true, "else": true, "and": true, "or": true, "not": true,
	"in": true, "for": true, "lambda": true, "emit": true, "assert": true, "is": true,
}

var boundaryOps = map[string]bool{
	",": true, "=": true, ":": true, ";": true, "->": true, "==": true, "!=": true, "<": true,
	">": true, "<=": true, ">=": true, "+=": true, "-=": true, "*=": true, "/=": true, "%=": true,
	"|=": true, "&=": true, "^=": true, "//=": true, "**=": true, "<<=": true, ">>=": true,
}

type regionKind int

const (
	rPlain regionKind = iota
	rCall             // (...) after a callee: kwarg colons
	rRecord           // {...} in sysq-script: bare keys
	rBraces           // {...} in Starlark: dict/set
)

// region lowers tokens [a,b) that sit at one bracket depth. It writes the gap
// before each token, so the result starts with the gap before toks[a].
func (l *lowerer) region(a, b int, rk regionKind, callee string) (string, error) {
	if a >= b {
		return "", nil
	}
	base := l.toks[a].depth
	var out strings.Builder
	segStart := a
	inStage := ""      // current pipeline stage word in this segment
	argIndex := 0      // call argument index
	prevBoundary := "" // text of the last boundary ("" = region start)
	lambdaOpen := false
	trimNext := false
	flush := func(end int) error {
		if segStart >= end {
			return nil
		}
		s, err := l.segment(segStart, end, rk, callee, argIndex, prevBoundary)
		if err != nil {
			return err
		}
		if trimNext {
			s = strings.TrimLeft(s, " \t")
			trimNext = false
		}
		out.WriteString(s)
		return nil
	}
	for k := a; k < b; k++ {
		t := l.toks[k]
		if t.depth != base || t.k == kComment || t.k == kNewline {
			continue
		}
		if l.stageAt(k) {
			n := l.nextSig(k)
			inStage = l.toks[n].text
			if inStage == "left" || inStage == "inner" || inStage == "anti" {
				inStage = "join"
			}
			continue
		}
		isB := false
		predArg := rk == rCall && argIndex == 1 && (callee == "any" || callee == "all")
		if predArg && !(t.k == kOp && t.text == ",") {
			continue
		}
		if t.k == kOp && boundaryOps[t.text] {
			isB = true
			if inStage != "" {
				switch {
				case t.text == ",":
					isB = !listStages[inStage] || l.keyAhead(k+1)
				case t.text == "=" || t.text == ":" || t.text == ";":
					isB = true
				default:
					isB = false // comparisons inside a predicate
				}
			}
		} else if t.k == kIdent && boundaryWords[t.text] && !((rk == rCall || rk == rRecord) && l.significantFrom(segStart, k) && l.is(l.nextSig(k), ":")) {
			isB = inStage == "" || t.text == "for" || (t.text == "if" && !l.is(k+1, "(")) || t.text == "else"
		}
		if !isB {
			continue
		}
		if err := flush(k); err != nil {
			return "", err
		}
		// Boundary token itself.
		if t.text == "lambda" {
			lambdaOpen = true
		}
		switch {
		case t.text == ":" && lambdaOpen:
			lambdaOpen = false
			out.WriteString(l.gap(k) + t.text)
		case t.text == ":" && rk == rCall && l.singleIdent(segStart, k):
			// kwarg: name: value  ->  name=value (already wrote the name)
			out.WriteString("=")
			trimNext = true
		case t.text == ":" && rk == rRecord && l.singleIdent(segStart, k):
			out.WriteString(":")
		default:
			out.WriteString(l.gap(k) + t.text)
		}
		if t.text == "," {
			argIndex++
		}
		prevBoundary = t.text
		if lambdaOpen {
			prevBoundary = "lambda"
		}
		segStart = k + 1
		inStage = ""
	}
	if err := flush(b); err != nil {
		return "", err
	}
	return out.String(), nil
}

// keyAhead reports a record key or kwarg pattern (name: / "s": / name=) at k.
func (l *lowerer) keyAhead(k int) bool {
	sig := l.significant(k, min(k+6, len(l.toks)))
	if len(sig) < 2 {
		return false
	}
	t0, t1 := l.toks[sig[0]], l.toks[sig[1]]
	return (t0.k == kIdent || t0.k == kStr) && t1.k == kOp && (t1.text == ":" || t1.text == "=")
}

// significantFrom reports whether k is the first significant token at or after a.
func (l *lowerer) significantFrom(a, k int) bool {
	sig := l.significant(a, k+1)
	return len(sig) == 1 && sig[0] == k
}

func (l *lowerer) singleIdent(a, b int) bool {
	sig := l.significant(a, b)
	return len(sig) == 1 && l.toks[sig[0]].k == kIdent
}

// segment lowers one boundary-free piece of a region.
func (l *lowerer) segment(a, b int, rk regionKind, callee string, argIndex int, prevBoundary string) (string, error) {
	sig := l.significant(a, b)
	if len(sig) == 0 {
		return l.copyRange(a, b), nil
	}
	base := l.toks[a].depth
	first := sig[0]
	lead := l.copyRange(a, first) // comments/newlines before the first token
	// Record keys / kwarg names.
	if (rk == rRecord || rk == rCall) && len(sig) == 1 && l.toks[first].k == kIdent && l.is(l.nextSig(first), ":") && prevBoundary != "lambda" {
		name := l.text(first)
		if rk == rRecord {
			return lead + l.gap(first) + `"` + name + `"`, nil
		}
		if keywords[name] {
			name += "_"
		}
		return lead + l.gap(first) + name, nil
	}
	// any(T, pred) / all(T, pred): the predicate argument.
	if rk == rCall && argIndex == 1 && (callee == "any" || callee == "all") {
		e, err := l.exprCall(sig[0], sig[len(sig)-1]+1, "filter")
		if err != nil {
			return "", err
		}
		return lead + l.gap(first) + e, nil
	}
	// a ?? b ?? c  ->  fallback(a, lambda: fallback(b, lambda: c))
	for _, k := range sig {
		if l.toks[k].depth == base && l.is(k, "??") {
			left, err := l.segment(a, k, rPlain, "", 0, "")
			if err != nil {
				return "", err
			}
			right, err := l.segment(k+1, b, rPlain, "", 0, "")
			if err != nil {
				return "", err
			}
			return l.gap(a) + "fallback(" + strings.TrimSpace(left) + ", lambda: " + strings.TrimSpace(right) + ")", nil
		}
	}
	// try X  ->  try_(lambda: X)
	if l.is(first, "try") {
		rest, err := l.segment(first+1, b, rPlain, "", 0, "")
		if err != nil {
			return "", err
		}
		return lead + l.gap(first) + "try_(lambda: " + strings.TrimSpace(rest) + ")", nil
	}
	// Pipeline?
	for _, k := range sig {
		if l.toks[k].depth == base && l.stageAt(k) {
			return l.pipeline(a, b, k)
		}
	}
	s, err := l.atoms(a, b)
	if err != nil {
		return "", err
	}
	// sysq-script: a dotted column read compared with a value is a scalar.
	if l.mode == Script && boundaryCompare(prevBoundary) || l.mode == Script && l.compareFollows(b) {
		if l.dottedName(sig) {
			return lead + l.gap(first) + "scalar(" + strings.TrimSpace(strings.TrimPrefix(s, lead)) + ")", nil
		}
	}
	return s, nil
}

func boundaryCompare(b string) bool {
	switch b {
	case "==", "!=", "<", ">", "<=", ">=":
		return true
	}
	return false
}

func (l *lowerer) compareFollows(b int) bool {
	for k := b; k < len(l.toks); k++ {
		if l.toks[k].k == kComment {
			continue
		}
		return l.toks[k].k == kOp && boundaryCompare(l.toks[k].text)
	}
	return false
}

func (l *lowerer) dottedName(sig []int) bool {
	if len(sig) < 3 || len(sig)%2 == 0 {
		return false
	}
	for i, k := range sig {
		if i%2 == 0 && l.toks[k].k != kIdent || i%2 == 1 && !l.is(k, ".") {
			return false
		}
	}
	return true
}

func (l *lowerer) copyRange(a, b int) string {
	var sb strings.Builder
	for k := a; k < b; k++ {
		sb.WriteString(l.gap(k) + l.tokText(k))
	}
	return sb.String()
}

// tokText is a token's text, except that a newline or comment at the
// statement's own depth (a continued sysq-script line) becomes a space.
func (l *lowerer) tokText(k int) string {
	t := l.toks[k]
	if t.depth == l.stmtDepth && (t.k == kNewline || t.k == kComment) {
		return " "
	}
	return l.text(k)
}

// atoms copies tokens, rewriting literals and recursing into brackets.
func (l *lowerer) atoms(a, b int) (string, error) {
	var out strings.Builder
	if a >= b {
		return "", nil
	}
	for k := a; k < b; k++ {
		t := l.toks[k]
		switch {
		case t.depth == l.stmtDepth && (t.k == kNewline || t.k == kComment):
			out.WriteString(" ")
		case t.k == kUnit:
			out.WriteString(l.gap(k) + unitLit(t.text))
		case t.k == kPath:
			out.WriteString(l.gap(k) + `Path("` + t.text + `")`)
		case t.k == kStr && isFString(t.text):
			s, err := l.fstring(k)
			if err != nil {
				return "", err
			}
			out.WriteString(l.gap(k) + s)
		case t.k == kIdent && t.text == "ago" && l.is(k+1, "("):
			m := l.match(k + 1)
			inner, err := l.region(k+2, m, rPlain, "")
			if err != nil {
				return "", err
			}
			out.WriteString(l.gap(k) + "(now() - " + strings.TrimSpace(inner) + ")")
			k = m
		case t.k == kOp && (t.text == "(" || t.text == "[" || t.text == "{"):
			m := l.match(k)
			rk, callee := rPlain, ""
			if t.text == "(" && k > 0 && endsValue(l.toks[k-1]) && l.toks[k-1].k != kStr {
				rk = rCall
				callee = l.text(k - 1)
			}
			if t.text == "{" {
				rk = rBraces
				if l.mode == Script {
					rk = rRecord
				}
			}
			inner, err := l.region(k+1, m, rk, callee)
			if err != nil {
				return "", err
			}
			out.WriteString(l.gap(k) + t.text + inner + l.gap(m) + l.text(m))
			k = m
		case t.k == kIdent && t.text == "except" && k > 0 && l.is(k-1, "."):
			out.WriteString(l.gap(k) + "except_")
		default:
			out.WriteString(l.gap(k) + t.text)
		}
	}
	return out.String(), nil
}

func unitLit(s string) string {
	j := len(s)
	for j > 0 && (s[j-1] >= 'a' && s[j-1] <= 'z' || s[j-1] >= 'A' && s[j-1] <= 'Z') {
		j--
	}
	switch s[j:] {
	case "ms", "s", "m", "h", "d", "w":
		return `Duration("` + s + `")`
	}
	return `Bytes("` + s + `")`
}

func isFString(s string) bool {
	return len(s) > 1 && (s[0] == 'f' || s[0] == 'F' || (len(s) > 2 && strings.ContainsAny(s[:2], "fF") && strings.ContainsAny(s[:2], "rR")))
}

// fstring lowers f"…{x}…" to "…{}…".format(x).
func (l *lowerer) fstring(k int) (string, error) {
	s := l.text(k)
	i := strings.IndexAny(s, `"'`)
	prefix := strings.NewReplacer("f", "", "F", "").Replace(s[:i])
	body := s[i:]
	q := body[:1]
	if strings.HasPrefix(body, strings.Repeat(q, 3)) {
		q = body[:3]
	}
	inner := body[len(q) : len(body)-len(q)]
	var lit strings.Builder
	var args []string
	for j := 0; j < len(inner); j++ {
		c := inner[j]
		if c == '{' && j+1 < len(inner) && inner[j+1] == '{' {
			lit.WriteString("{{")
			j++
			continue
		}
		if c == '}' && j+1 < len(inner) && inner[j+1] == '}' {
			lit.WriteString("}}")
			j++
			continue
		}
		if c == '{' {
			depth, e := 1, j+1
			for ; e < len(inner) && depth > 0; e++ {
				switch inner[e] {
				case '{':
					depth++
				case '}':
					depth--
				}
			}
			if depth != 0 {
				return "", l.errAt(k, "unbalanced { in f-string")
			}
			code := inner[j+1 : e-1]
			if strings.ContainsAny(code, `"'`) && q == `"` && strings.Contains(code, `"`) {
				return "", l.errAt(k, "quotes inside f-string braces")
			}
			sub, err := lowerFragment(code, l.mode, l.scope)
			if err != nil {
				return "", l.errAt(k, "in f-string: %v", err)
			}
			args = append(args, strings.TrimSpace(sub))
			lit.WriteString("{}")
			j = e - 1
			continue
		}
		lit.WriteByte(c)
	}
	return prefix + q + lit.String() + q + ".format(" + strings.Join(args, ", ") + ")", nil
}

// lowerFragment lowers a standalone expression (used inside f-strings).
func lowerFragment(code string, mode Mode, scope func(int) map[string]bool) (string, error) {
	toks, err := lex(code)
	if err != nil {
		return "", err
	}
	sub := &lowerer{src: code, toks: toks, mode: mode, scope: func(int) map[string]bool { return scope(0) }}
	return sub.region(0, len(toks)-1, rPlain, "")
}

// ---- pipelines -----------------------------------------------------------------

func (l *lowerer) pipeline(a, b, firstPipe int) (string, error) {
	base := l.toks[a].depth
	head, err := l.atoms(a, firstPipe)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(head) == "" {
		return "", l.errAt(firstPipe, "pipeline has no input before |")
	}
	var out strings.Builder
	out.WriteString(strings.TrimRight(head, " \t\n"))
	// Stage boundaries.
	var pipes []int
	for k := firstPipe; k < b; k++ {
		if l.toks[k].depth == base && l.stageAt(k) {
			pipes = append(pipes, k)
		}
	}
	for i, p := range pipes {
		end := b
		if i+1 < len(pipes) {
			end = pipes[i+1]
		}
		s, err := l.stage(l.nextSig(p), end)
		if err != nil {
			return "", err
		}
		out.WriteString(s)
	}
	return out.String(), nil
}

// stage lowers one stage starting at its keyword token k.
func (l *lowerer) stage(k, end int) (string, error) {
	word := l.text(k)
	args := l.nextSig(k)
	if args > end {
		args = end
	}
	sig := l.significant(args, end)
	argText := func() (string, error) {
		s, err := l.region(args, end, rPlain, "")
		return strings.TrimSpace(s), err
	}
	switch word {
	case "filter":
		if len(sig) > 0 && l.is(sig[0], "(") && l.match(sig[0]) == sig[len(sig)-1] {
			inner, err := l.region(sig[0]+1, sig[len(sig)-1], rPlain, "")
			if err != nil {
				return "", err
			}
			return ".filter(" + strings.TrimSpace(inner) + ")", nil
		}
		if len(sig) == 0 {
			return "", l.errAt(k, "filter needs a predicate")
		}
		e, err := l.exprCall(args, end, "filter")
		return ".filter(" + e + ")", err
	case "select", "window":
		if len(sig) == 0 {
			return "", l.errAt(k, "%s needs columns", word)
		}
		e, err := l.exprCall(args, end, word)
		return "." + word + "(" + e + ")", err
	case "sort":
		if len(sig) == 0 || !l.is(sig[0], "by") {
			return "", l.errAt(k, "write sort by <column> [asc|desc], ...")
		}
		e, err := l.exprCall(sig[0]+1, end, "sort")
		return ".sort(" + e + ")", err
	case "limit":
		off := -1
		for _, j := range sig {
			if l.toks[j].depth == l.toks[args].depth && l.is(j, "offset") {
				off = j
			}
		}
		if off < 0 {
			n, err := argText()
			if err != nil {
				return "", err
			}
			if n == "" {
				return "", l.errAt(k, "limit needs a number")
			}
			return ".limit(" + n + ")", nil
		}
		n, err := l.region(args, off, rPlain, "")
		if err != nil {
			return "", err
		}
		m, err := l.region(off+1, end, rPlain, "")
		if err != nil {
			return "", err
		}
		return ".limit(" + strings.TrimSpace(n) + ", offset=" + strings.TrimSpace(m) + ")", nil
	case "head", "tail", "sample":
		n, err := argText()
		if err != nil {
			return "", err
		}
		if n == "" {
			return "", l.errAt(k, "%s needs a number", word)
		}
		return "." + word + "(" + n + ")", nil
	case "distinct", "unnest", "unpivot", "pivot":
		names, err := l.names(sig)
		if err != nil {
			return "", err
		}
		if word == "unnest" && len(names) != 1 {
			return "", l.errAt(k, "unnest takes one column")
		}
		if word == "pivot" && len(names) != 3 {
			return "", l.errAt(k, "pivot takes row key, column key, value column")
		}
		return "." + word + "(" + quoteAll(names) + ")", nil
	case "group":
		return l.groupStage(k, sig, end)
	case "left", "inner", "anti":
		j := l.nextSig(k) // "join"
		return l.joinStage(word, l.nextSig(j), end)
	case "join":
		return l.joinStage("", args, end)
	case "union", "except", "intersect":
		t, err := argText()
		if err != nil {
			return "", err
		}
		if t == "" {
			return "", l.errAt(k, "%s needs a table", word)
		}
		if word == "except" {
			word = "except_"
		}
		return "." + word + "(" + t + ")", nil
	case "diff":
		on := l.findWord(sig, "on")
		if on < 0 {
			t, err := argText()
			return ".diff(" + t + ")", err
		}
		t, err := l.region(args, on, rPlain, "")
		if err != nil {
			return "", err
		}
		names, err := l.names(l.significant(on+1, end))
		if err != nil {
			return "", err
		}
		return ".diff(" + strings.TrimSpace(t) + ", on=" + onArg(names) + ")", nil
	case "nest":
		if len(sig) == 0 || !l.is(sig[0], "by") {
			return "", l.errAt(k, "write nest by <column> as <name>")
		}
		as := l.findWord(sig, "as")
		if as < 0 {
			return "", l.errAt(k, "nest needs as <name>")
		}
		names, err := l.names(l.significant(sig[0]+1, as))
		if err != nil {
			return "", err
		}
		alias, err := l.names(l.significant(as+1, end))
		if err != nil || len(alias) != 1 {
			return "", l.errAt(k, "nest needs one name after as")
		}
		return ".nest(by=" + onArg(names) + `, as_="` + alias[0] + `")`, nil
	}
	return "", l.errAt(k, "unknown stage %s", word)
}

func (l *lowerer) findWord(sig []int, w string) int {
	if len(sig) == 0 {
		return -1
	}
	d := l.toks[sig[0]].depth
	for _, j := range sig {
		if l.toks[j].depth == d && l.is(j, w) && l.toks[j].k == kIdent {
			return j
		}
	}
	return -1
}

func (l *lowerer) names(sig []int) ([]string, error) {
	var out []string
	want := true
	for _, j := range sig {
		t := l.toks[j]
		if want {
			if t.k != kIdent {
				return nil, l.errAt(j, "expected a column name, got %q", t.text)
			}
			out = append(out, t.text)
			want = false
			continue
		}
		if !l.is(j, ",") {
			return nil, l.errAt(j, "expected , between column names, got %q", t.text)
		}
		want = true
	}
	return out, nil
}

func quoteAll(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = `"` + n + `"`
	}
	return strings.Join(q, ", ")
}

func onArg(names []string) string {
	if len(names) == 1 {
		return `"` + names[0] + `"`
	}
	return "[" + quoteAll(names) + "]"
}

func (l *lowerer) joinStage(how string, args, end int) (string, error) {
	sig := l.significant(args, end)
	on := l.findWord(sig, "on")
	if on < 0 {
		return "", l.errAt(args, "join needs on <column>")
	}
	t, err := l.region(args, on, rPlain, "")
	if err != nil {
		return "", err
	}
	keys := l.significant(on+1, end)
	// trailing "left" / "inner" / "anti" (design §16: join T on pid left)
	if n := len(keys); n > 0 {
		if w := l.text(keys[n-1]); w == "left" || w == "inner" || w == "anti" {
			if how == "" {
				how = w
			}
			keys = keys[:n-1]
		}
	}
	names, err := l.names(keys)
	if err != nil {
		return "", err
	}
	s := ".join(" + strings.TrimSpace(t) + ", on=" + onArg(names)
	if how != "" && how != "inner" {
		s += `, how="` + how + `"`
	}
	return s + ")", nil
}

func (l *lowerer) groupStage(k int, sig []int, end int) (string, error) {
	keysA, keysB := -1, -1
	aggA, aggB := -1, -1
	i := 0
	if i < len(sig) && l.is(sig[i], "by") {
		i++
		keysA = sig[i-1] + 1
		for i < len(sig) && !(l.is(sig[i], "{") && l.toks[sig[i]].depth == l.toks[sig[0]].depth) {
			i++
		}
		if i >= len(sig) {
			return "", l.errAt(k, "group by <keys> needs { <aggregates> }")
		}
		keysB = sig[i]
	}
	if i >= len(sig) || !l.is(sig[i], "{") {
		return "", l.errAt(k, "write group by <keys> { count() as n, ... } or group { ... }")
	}
	m := l.match(sig[i])
	// group {} { aggs } or group {keys} { aggs }
	j := l.nextSig(m)
	if j < end && l.is(j, "{") {
		if keysA >= 0 {
			return "", l.errAt(k, "group has keys twice")
		}
		keysA, keysB = sig[i]+1, m
		aggA, aggB = j+1, l.match(j)
	} else {
		aggA, aggB = sig[i]+1, m
	}
	keys := ""
	var idents []string
	if keysA >= 0 {
		keys = l.raw(keysA, keysB)
		items, err := expr.ParseList(keys, "group")
		if err != nil {
			return "", l.errAt(k, "group keys: %v", err)
		}
		idents = append(idents, expr.ListIdents(items)...)
	}
	aggs := l.raw(aggA, aggB)
	items, err := expr.ParseList(aggs, "group")
	if err != nil {
		return "", l.errAt(k, "group aggregates: %v", err)
	}
	for _, it := range items {
		if !expr.HasAggregate(it.X) {
			return "", l.errAt(k, "%s is not an aggregate", it.Src)
		}
	}
	idents = append(idents, expr.ListIdents(items)...)
	b := l.bindings(k, idents)
	return ".group(expr(" + quote(keys) + "), expr(" + quote(aggs) + ")" + b + ")", nil
}

// exprCall builds expr("<text>", name=name, ...) for tokens [a,b).
func (l *lowerer) exprCall(a, b int, mode string) (string, error) {
	text := l.raw(a, b)
	var idents []string
	switch mode {
	case "filter":
		n, err := expr.Parse(text)
		if err != nil {
			return "", l.errAt(a, "%v", err)
		}
		idents = expr.Idents(n)
	default:
		items, err := expr.ParseList(text, mode)
		if err != nil {
			return "", l.errAt(a, "%v", err)
		}
		idents = expr.ListIdents(items)
	}
	return "expr(" + quote(text) + l.bindings(a, idents) + ")", nil
}

// bindings renders ", x=x, y=y" for identifiers that are script variables.
func (l *lowerer) bindings(k int, idents []string) string {
	scope := l.scope(k)
	var names []string
	seen := map[string]bool{}
	for _, id := range idents {
		if scope[id] && !seen[id] {
			seen[id] = true
			names = append(names, id)
		}
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString(", " + n + "=" + n)
	}
	return sb.String()
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}
