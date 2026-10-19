package sugar

import (
	"strings"
)

// Lower lowers src in the given mode.
func Lower(src string, mode Mode) (string, error) {
	toks, err := lex(src)
	if err != nil {
		return "", err
	}
	l := &lowerer{src: src, toks: toks, mode: mode}
	if mode == Star {
		return l.star()
	}
	return l.script()
}

// Detect picks the syntax of an ad-hoc script: Starlark when it defines
// functions with def or uses indented blocks; sysq-script otherwise.
func Detect(src string) Mode {
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "def ") || strings.HasPrefix(t, "load(") || strings.HasPrefix(t, "recipe(") {
			return Star
		}
		if (strings.HasPrefix(t, "if ") || strings.HasPrefix(t, "for ") || strings.HasPrefix(t, "elif ") || t == "else:") && strings.HasSuffix(t, ":") {
			return Star
		}
	}
	return Script
}

// ---- sysq-script -----------------------------------------------------------------

type scriptState struct {
	scopes  []map[string]bool
	lines   []string
	emitted bool
}

func (l *lowerer) script() (string, error) {
	st := &scriptState{scopes: []map[string]bool{{}}}
	l.scope = func(int) map[string]bool {
		m := map[string]bool{}
		for _, s := range st.scopes {
			for k := range s {
				m[k] = true
			}
		}
		return m
	}
	lastExpr := -1
	_, err := l.block(0, 0, st, false, &lastExpr)
	if err != nil {
		return "", err
	}
	if !st.emitted && lastExpr >= 0 {
		line := st.lines[lastExpr]
		st.lines[lastExpr] = "emit(" + line + ")"
	}
	out := strings.Join(st.lines, "\n")
	if out != "" {
		out += "\n"
	}
	return out, nil
}

// block lowers statements from k until the closing "}" at depth (or EOF),
// returning the index after the block.
func (l *lowerer) block(k, indent int, st *scriptState, fnBody bool, lastExpr *int) (int, error) {
	depth := l.toks[k].depth
	if k > 0 && l.is(k-1, "{") {
		depth = l.toks[k-1].depth + 1
	}
	start := len(st.lines)
	lastStmt := -1
	saved := l.stmtDepth
	l.stmtDepth = depth
	defer func() { l.stmtDepth = saved }()
	pad := strings.Repeat("  ", indent)
	for {
		// skip blank lines and separators
		for k < len(l.toks) && (l.toks[k].k == kNewline || l.is(k, ";")) {
			k++
		}
		t := l.toks[k]
		if t.k == kEOF {
			if indent > 0 {
				return k, l.errAt(k, "missing }")
			}
			break
		}
		if l.is(k, "}") && t.depth == depth-1 {
			k++
			break
		}
		if t.k == kComment {
			st.lines = append(st.lines, pad+t.text)
			k++
			continue
		}
		next, isExpr, err := l.statement(k, indent, st, lastExpr)
		if err != nil {
			return k, err
		}
		if isExpr {
			lastStmt = len(st.lines) - 1
		} else {
			lastStmt = -1
		}
		k = next
	}
	if len(st.lines) == start {
		st.lines = append(st.lines, pad+"pass")
	}
	if fnBody && lastStmt >= 0 {
		st.lines[lastStmt] = pad + "return " + strings.TrimPrefix(st.lines[lastStmt], pad)
	}
	if indent == 0 && lastExpr != nil {
		*lastExpr = lastStmt
		if lastStmt >= 0 {
			// script() wraps this line in emit(); keep it unindented.
			st.lines[lastStmt] = strings.TrimPrefix(st.lines[lastStmt], pad)
		}
	}
	return k, nil
}

// stmtEnd finds the end of a simple statement starting at k.
func (l *lowerer) stmtEnd(k int) int {
	d := l.toks[k].depth
	for j := k; j < len(l.toks); j++ {
		t := l.toks[j]
		if t.k == kEOF {
			return j
		}
		if t.depth < d || (l.is(j, "}") && t.depth == d-1) {
			return j
		}
		if t.depth != d {
			continue
		}
		if l.is(j, ";") {
			return j
		}
		if t.k == kNewline && !l.continues(j) {
			return j
		}
	}
	return len(l.toks) - 1
}

// continues reports whether the newline at j continues the statement: the
// line ends in an operator, or the next line starts with | . and or ?? .
func (l *lowerer) continues(j int) bool {
	p := j - 1
	for p >= 0 && (l.toks[p].k == kComment) {
		p--
	}
	if p >= 0 {
		pt := l.toks[p]
		if pt.k == kOp && pt.text != ")" && pt.text != "]" && pt.text != "}" && pt.text != ";" {
			return true
		}
		if pt.k == kIdent && (pt.text == "and" || pt.text == "or" || pt.text == "not" || pt.text == "in") {
			return true
		}
	}
	n := l.nextSig(j)
	if n < len(l.toks) {
		nt := l.toks[n]
		if nt.k == kOp && (nt.text == "|" || nt.text == "." || nt.text == "??") {
			return true
		}
		if nt.k == kIdent && (nt.text == "and" || nt.text == "or") {
			return true
		}
	}
	return false
}

// blockOpen finds the "{" that opens a block in a header starting at k.
func (l *lowerer) blockOpen(k int) int {
	d := l.toks[k].depth
	inGroup := false
	for j := k; j < len(l.toks); j++ {
		t := l.toks[j]
		if t.k == kEOF {
			return -1
		}
		if t.depth != d {
			continue
		}
		if l.stageAt(j) && l.is(l.nextSig(j), "group") {
			inGroup = true
			continue
		}
		if l.is(j, "{") {
			if inGroup {
				m := l.match(j)
				if n := l.nextSig(m); l.is(n, "{") {
					m = l.match(n)
				}
				j = m
				inGroup = false
				continue
			}
			p := j - 1
			for p >= k && (l.toks[p].k == kComment || l.toks[p].k == kNewline) {
				p--
			}
			if p >= k && endsValue(l.toks[p]) {
				return j
			}
		}
		if t.k == kNewline && !l.continues(j) {
			return -1
		}
	}
	return -1
}

func (l *lowerer) lowerExpr(a, b int) (string, error) {
	s, err := l.region(a, b, rPlain, "")
	return strings.TrimSpace(s), err
}

// statement lowers one statement and returns the next index and whether it
// was a bare expression.
func (l *lowerer) statement(k, indent int, st *scriptState, lastExpr *int) (int, bool, error) {
	pad := strings.Repeat("  ", indent)
	t := l.toks[k]
	add := func(s string) { st.lines = append(st.lines, pad+s) }
	cur := st.scopes[len(st.scopes)-1]
	switch {
	case l.is(k, "let"):
		end := l.stmtEnd(k)
		eq := -1
		for j := k + 1; j < end; j++ {
			if l.is(j, "=") && l.toks[j].depth == t.depth {
				eq = j
				break
			}
		}
		if eq < 0 {
			return 0, false, l.errAt(k, "let needs = ")
		}
		targets := strings.TrimSpace(l.copyRange(k+1, eq))
		rhs, err := l.lowerExpr(eq+1, end)
		if err != nil {
			return 0, false, err
		}
		add(targets + " = " + rhs)
		for _, n := range strings.Split(targets, ",") {
			cur[strings.TrimSpace(n)] = true
		}
		return end, false, nil
	case l.is(k, "fn"):
		if l.toks[k+1].k != kIdent || !l.is(k+2, "(") {
			return 0, false, l.errAt(k, "write fn name(params) { ... }")
		}
		name := l.text(k + 1)
		m := l.match(k + 2)
		params, err := l.region(k+3, m, rPlain, "")
		if err != nil {
			return 0, false, err
		}
		open := l.nextSig(m)
		if !l.is(open, "{") {
			return 0, false, l.errAt(open, "fn body needs { }")
		}
		cur[name] = true
		add("def " + name + "(" + strings.TrimSpace(params) + "):")
		scope := map[string]bool{}
		for _, p := range strings.Split(l.raw(k+3, m), ",") {
			p = strings.TrimSpace(p)
			p, _, _ = strings.Cut(p, "=")
			scope[strings.Trim(strings.TrimSpace(p), "*")] = true
		}
		st.scopes = append(st.scopes, scope)
		next, err := l.block(open+1, indent+1, st, true, nil)
		st.scopes = st.scopes[:len(st.scopes)-1]
		return next, false, err
	case l.is(k, "if"):
		return l.ifStmt(k, indent, st, "if")
	case l.is(k, "for"):
		in := -1
		for j := k + 1; j < len(l.toks); j++ {
			if l.is(j, "in") && l.toks[j].depth == t.depth {
				in = j
				break
			}
		}
		if in < 0 {
			return 0, false, l.errAt(k, "write for x in table { ... }")
		}
		open := l.blockOpen(in + 1)
		if open < 0 {
			return 0, false, l.errAt(k, "for needs a { } block")
		}
		targets := strings.TrimSpace(l.copyRange(k+1, in))
		iter, err := l.lowerExpr(in+1, open)
		if err != nil {
			return 0, false, err
		}
		for _, n := range strings.Split(targets, ",") {
			cur[strings.TrimSpace(n)] = true
		}
		add("for " + targets + " in " + iter + ":")
		next, err := l.block(open+1, indent+1, st, false, nil)
		return next, false, err
	case l.is(k, "emit"):
		end := l.stmtEnd(k)
		st.emitted = true
		n := l.nextSig(k)
		if n < end && l.toks[n].k == kIdent && l.is(l.nextSig(n), ":") {
			label := l.text(n)
			body, err := l.lowerExpr(l.nextSig(n)+1, end)
			if err != nil {
				return 0, false, err
			}
			add("emit(" + body + `, label="` + label + `")`)
			return end, false, nil
		}
		body, err := l.lowerExpr(k+1, end)
		if err != nil {
			return 0, false, err
		}
		add("emit(" + body + ")")
		return end, false, nil
	case l.is(k, "assert"):
		end := l.stmtEnd(k)
		body, err := l.lowerExpr(k+1, end)
		if err != nil {
			return 0, false, err
		}
		add("assert_(" + body + ")")
		return end, false, nil
	case l.is(k, "return"):
		end := l.stmtEnd(k)
		body, err := l.lowerExpr(k+1, end)
		if err != nil {
			return 0, false, err
		}
		add(strings.TrimSpace("return " + body))
		return end, false, nil
	case l.is(k, "break") || l.is(k, "continue") || l.is(k, "pass"):
		add(t.text)
		return k + 1, false, nil
	case l.is(k, "else"):
		return 0, false, l.errAt(k, "else without if")
	}
	end := l.stmtEnd(k)
	// assignment?
	for j := k; j < end; j++ {
		tj := l.toks[j]
		if tj.depth == t.depth && tj.k == kOp && (tj.text == "=" || strings.HasSuffix(tj.text, "=") && len(tj.text) >= 2 && tj.text != "==" && tj.text != "!=" && tj.text != "<=" && tj.text != ">=") {
			if l.stageBefore(k, j) {
				break
			}
			targets := strings.TrimSpace(l.copyRange(k, j))
			rhs, err := l.lowerExpr(j+1, end)
			if err != nil {
				return 0, false, err
			}
			add(targets + " " + tj.text + " " + rhs)
			for _, n := range strings.Split(targets, ",") {
				cur[strings.TrimSpace(n)] = true
			}
			return end, false, nil
		}
	}
	e, err := l.lowerExpr(k, end)
	if err != nil {
		return 0, false, err
	}
	add(e)
	return end, true, nil
}

func (l *lowerer) stageBefore(a, b int) bool {
	for j := a; j < b; j++ {
		if l.stageAt(j) {
			return true
		}
	}
	return false
}

func (l *lowerer) ifStmt(k, indent int, st *scriptState, kw string) (int, bool, error) {
	pad := strings.Repeat("  ", indent)
	open := l.blockOpen(k + 1)
	if open < 0 {
		return 0, false, l.errAt(k, "%s needs a { } block", kw)
	}
	cond, err := l.lowerExpr(k+1, open)
	if err != nil {
		return 0, false, err
	}
	st.lines = append(st.lines, pad+kw+" "+cond+":")
	next, err := l.block(open+1, indent+1, st, false, nil)
	if err != nil {
		return 0, false, err
	}
	// else / else if, possibly on the next line
	n := next
	for n < len(l.toks) && (l.toks[n].k == kNewline || l.toks[n].k == kComment) {
		n++
	}
	if l.is(n, "else") {
		if l.is(l.nextSig(n), "if") {
			return l.ifStmt(l.nextSig(n), indent, st, "elif")
		}
		o := l.nextSig(n)
		if !l.is(o, "{") {
			return 0, false, l.errAt(o, "else needs { }")
		}
		st.lines = append(st.lines, pad+"else:")
		next, err = l.block(o+1, indent+1, st, false, nil)
		return next, false, err
	}
	return next, false, nil
}
