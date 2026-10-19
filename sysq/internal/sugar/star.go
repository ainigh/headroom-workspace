package sugar

import (
	"strings"
)

// star lowers Starlark-with-sugar: statements stay as written; expression
// regions are rewritten in place.
func (l *lowerer) star() (string, error) {
	l.scope = l.starScopes()
	var out strings.Builder
	k := 0
	for k < len(l.toks) && l.toks[k].k != kEOF {
		// logical line: up to a newline at depth 0
		end := k
		for end < len(l.toks) && l.toks[end].k != kEOF && !(l.toks[end].k == kNewline && l.toks[end].depth == 0) {
			end++
		}
		s, err := l.starLine(k, end)
		if err != nil {
			return "", err
		}
		out.WriteString(s)
		if end < len(l.toks) && l.toks[end].k == kNewline {
			out.WriteString(l.gap(end) + "\n")
			end++
		}
		k = end
	}
	if k < len(l.toks) {
		out.WriteString(l.gap(k))
	}
	return out.String(), nil
}

func (l *lowerer) starLine(a, b int) (string, error) {
	sig := l.significant(a, b)
	if len(sig) == 0 {
		return l.copyRange(a, b), nil
	}
	f := sig[0]
	lead := l.copyRange(a, f) + l.gap(f)
	w := l.text(f)
	rest := func(from int) (string, error) { return l.region(from, b, rPlain, "") }
	switch {
	case l.toks[f].k == kIdent && (w == "if" || w == "elif" || w == "while" || w == "for"):
		colon := -1
		for _, j := range sig {
			if l.is(j, ":") && l.toks[j].depth == 0 {
				colon = j
			}
		}
		if colon < 0 {
			return "", l.errAt(f, "%s needs a colon", w)
		}
		if w == "for" {
			in := -1
			for _, j := range sig {
				if l.is(j, "in") && l.toks[j].depth == 0 {
					in = j
					break
				}
			}
			if in < 0 {
				return "", l.errAt(f, "for needs in")
			}
			iter, err := l.region(in+1, colon, rPlain, "")
			if err != nil {
				return "", err
			}
			tail, err := l.inlineBody(colon+1, b)
			if err != nil {
				return "", err
			}
			return lead + w + l.copyRange(f+1, in+1) + iter + l.gap(colon) + ":" + tail, nil
		}
		cond, err := l.region(f+1, colon, rPlain, "")
		if err != nil {
			return "", err
		}
		tail, err := l.inlineBody(colon+1, b)
		if err != nil {
			return "", err
		}
		return lead + w + cond + l.gap(colon) + ":" + tail, nil
	case w == "def":
		// def name(params): — defaults may hold literals with units
		if !l.is(f+2, "(") {
			return l.copyRange(a, b), nil
		}
		m := l.match(f + 2)
		params, err := l.region(f+3, m, rPlain, "")
		if err != nil {
			return "", err
		}
		tail := l.copyRange(m+1, b)
		return lead + "def" + l.gap(f+1) + l.text(f+1) + l.gap(f+2) + "(" + params + l.gap(m) + ")" + tail, nil
	case w == "else" || w == "load" || w == "pass" || w == "break" || w == "continue":
		return l.copyRange(a, b), nil
	case w == "return":
		r, err := rest(f + 1)
		return lead + "return" + r, err
	case w == "emit" && !l.is(l.nextSig(f), "("):
		n := l.nextSig(f)
		if n < b && l.toks[n].k == kIdent && l.is(l.nextSig(n), ":") {
			body, err := l.region(l.nextSig(n)+1, b, rPlain, "")
			if err != nil {
				return "", err
			}
			return lead + "emit(" + strings.TrimSpace(body) + `, label="` + l.text(n) + `")`, nil
		}
		body, err := rest(f + 1)
		return lead + "emit(" + strings.TrimSpace(body) + ")", err
	case w == "assert":
		body, err := rest(f + 1)
		return lead + "assert_(" + strings.TrimSpace(body) + ")", err
	}
	// assignment: targets = value (first depth-0 '=' or augmented op)
	for _, j := range sig {
		t := l.toks[j]
		if t.depth == 0 && t.k == kOp && (t.text == "=" || len(t.text) >= 2 && strings.HasSuffix(t.text, "=") && t.text != "==" && t.text != "!=" && t.text != "<=" && t.text != ">=") {
			v, err := l.region(j+1, b, rPlain, "")
			if err != nil {
				return "", err
			}
			return lead + l.text(f) + l.copyRange(f+1, j) + l.gap(j) + t.text + v, nil
		}
	}
	s, err := l.region(f, b, rPlain, "")
	if err != nil {
		return "", err
	}
	return lead + strings.TrimLeft(s, " \t"), nil
}

// inlineBody lowers a simple statement after a header colon (if x: return y).
func (l *lowerer) inlineBody(a, b int) (string, error) {
	sig := l.significant(a, b)
	if len(sig) == 0 {
		return l.copyRange(a, b), nil
	}
	s, err := l.starLine(a, b)
	return s, err
}

// starScopes computes, for each token, the variable names bound before it:
// module-level names plus the enclosing function's parameters and locals,
// each counted from the end of the statement that first assigns it (so a
// predicate never references a local before assignment).
func (l *lowerer) starScopes() func(k int) map[string]bool {
	type fn struct {
		indent int
		names  map[string]int // name -> token index from which it is bound
		start  int
		end    int
	}
	module := map[string]int{}
	var fns []*fn
	var stack []*fn
	bind := func(m map[string]int, name string, at int) {
		if _, ok := m[name]; !ok {
			m[name] = at
		}
	}
	k := 0
	for k < len(l.toks) && l.toks[k].k != kEOF {
		end := k
		for end < len(l.toks) && l.toks[end].k != kEOF && !(l.toks[end].k == kNewline && l.toks[end].depth == 0) {
			end++
		}
		sig := l.significant(k, end)
		if len(sig) > 0 {
			f := sig[0]
			indent := l.toks[f].start - lineStart(l.src, l.toks[f].start)
			for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
				stack[len(stack)-1].end = f
				stack = stack[:len(stack)-1]
			}
			target := module
			if len(stack) > 0 {
				target = stack[len(stack)-1].names
			}
			switch {
			case l.is(f, "def") && l.toks[f+1].k == kIdent:
				bind(target, l.text(f+1), end)
				nf := &fn{indent: indent, names: map[string]int{}, start: f}
				if l.is(f+2, "(") {
					m := l.match(f + 2)
					for j := f + 3; j < m; j++ {
						if l.toks[j].k == kIdent && l.toks[j].depth == l.toks[f+3].depth {
							p := j - 1
							if p == f+2 || l.is(p, ",") || l.is(p, "*") || l.is(p, "**") {
								bind(nf.names, l.text(j), f)
							}
						}
					}
				}
				fns = append(fns, nf)
				stack = append(stack, nf)
			case l.is(f, "for"):
				for _, j := range sig[1:] {
					if l.is(j, "in") {
						break
					}
					if l.toks[j].k == kIdent {
						bind(target, l.text(j), end)
					}
				}
			case l.is(f, "load") && l.is(f+1, "("):
				m := l.match(f + 1)
				for j := f + 2; j < m; j++ {
					t := l.toks[j]
					if t.k == kStr && j > f+2 {
						bind(target, strings.Trim(t.text, `"'`), end)
					}
					if t.k == kIdent && l.is(j+1, "=") {
						bind(target, t.text, end)
					}
				}
			default:
				for _, j := range sig {
					t := l.toks[j]
					if t.depth == 0 && t.k == kOp && (t.text == "=" || len(t.text) >= 2 && strings.HasSuffix(t.text, "=") && t.text != "==" && t.text != "!=" && t.text != "<=" && t.text != ">=") {
						for _, x := range sig {
							if x == j {
								break
							}
							if l.toks[x].k == kIdent {
								bind(target, l.text(x), end)
							}
						}
						break
					}
				}
			}
		}
		if end < len(l.toks) && l.toks[end].k == kNewline {
			end++
		}
		k = end
	}
	for _, f := range stack {
		f.end = len(l.toks)
	}
	return func(k int) map[string]bool {
		out := map[string]bool{}
		for n, at := range module {
			if at <= k {
				out[n] = true
			}
		}
		for _, f := range fns {
			if k >= f.start && (f.end == 0 || k < f.end) {
				for n, at := range f.names {
					if at <= k {
						out[n] = true
					}
				}
			}
		}
		return out
	}
}

func lineStart(src string, pos int) int {
	i := strings.LastIndexByte(src[:pos], '\n')
	return i + 1
}
