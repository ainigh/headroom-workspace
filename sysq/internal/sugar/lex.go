// Package sugar lowers sysq-script and Starlark-with-sugar into plain
// Starlark (design §18). Lowering is a pure text transform: the output is
// what `explain` shows, and it is the only thing that executes.
package sugar

import (
	"fmt"
	"strings"
	"unicode"
)

type kind int

const (
	kEOF kind = iota
	kIdent
	kNum
	kUnit // number with a unit: 2s, 500MB
	kStr
	kPath // ~/x, /usr/bin (expression position only)
	kOp
	kNewline
	kComment
)

type token struct {
	k     kind
	text  string
	start int
	end   int
	depth int // bracket depth before this token
	line  int
}

var unitSuffixes = []string{"KiB", "MiB", "GiB", "TiB", "kib", "mib", "gib", "tib", "KB", "MB", "GB", "TB", "kb", "mb", "gb", "tb", "ms", "s", "m", "h", "d", "w", "B"}

// Starlark keywords (and reserved words) that can never end a value.
var keywords = map[string]bool{
	"and": true, "break": true, "continue": true, "def": true, "elif": true, "else": true,
	"for": true, "if": true, "in": true, "lambda": true, "load": true, "not": true, "or": true,
	"pass": true, "return": true, "while": true, "as": true, "assert": true, "async": true,
	"await": true, "class": true, "del": true, "except": true, "finally": true, "from": true,
	"global": true, "import": true, "is": true, "nonlocal": true, "raise": true, "try": true,
	"with": true, "yield": true,
	// sysq-script statement words
	"let": true, "fn": true, "emit": true,
}

func endsValue(t token) bool {
	switch t.k {
	case kNum, kUnit, kStr, kPath:
		return true
	case kIdent:
		return !keywords[t.text]
	case kOp:
		return t.text == ")" || t.text == "]" || t.text == "}"
	}
	return false
}

var ops3 = []string{"**=", "//=", "<<=", ">>="}
var ops2 = []string{"==", "!=", "<=", ">=", "**", "//", "->", "??", "+=", "-=", "*=", "/=", "%=", "|=", "&=", "^=", "<<", ">>"}

func lex(src string) ([]token, error) {
	var toks []token
	depth, line := 0, 1
	i := 0
	last := func() (token, bool) {
		for j := len(toks) - 1; j >= 0; j-- {
			if toks[j].k != kComment && toks[j].k != kNewline {
				return toks[j], true
			}
			if toks[j].k == kNewline {
				return toks[j], false
			}
		}
		return token{}, false
	}
	add := func(k kind, s, e int) {
		toks = append(toks, token{k: k, text: src[s:e], start: s, end: e, depth: depth, line: line})
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			add(kNewline, i, i+1)
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\\' && i+1 < len(src) && src[i+1] == '\n':
			i += 2
			line++
		case c == '#':
			j := i
			for j < len(src) && src[j] != '\n' {
				j++
			}
			add(kComment, i, j)
			i = j
		case isStrStart(src, i):
			j, err := scanString(src, i)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			add(kStr, i, j)
			line += strings.Count(src[i:j], "\n")
			i = j
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9':
			j := i
			if c == '0' && i+1 < len(src) && (src[i+1] == 'x' || src[i+1] == 'X' || src[i+1] == 'o' || src[i+1] == 'b') {
				j += 2
				for j < len(src) && isHex(src[j]) {
					j++
				}
				add(kNum, i, j)
				i = j
				continue
			}
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' || src[j] == '_') {
				j++
			}
			if j < len(src) && (src[j] == 'e' || src[j] == 'E') && j+1 < len(src) && (src[j+1] >= '0' && src[j+1] <= '9' || src[j+1] == '-' || src[j+1] == '+') {
				j += 2
				for j < len(src) && src[j] >= '0' && src[j] <= '9' {
					j++
				}
			}
			unit := ""
			for _, u := range unitSuffixes {
				if strings.HasPrefix(src[j:], u) {
					k := j + len(u)
					if k >= len(src) || !isIdentChar(src[k]) {
						unit = u
						break
					}
				}
			}
			if unit != "" {
				add(kUnit, i, j+len(unit))
				i = j + len(unit)
			} else {
				if j < len(src) && isIdentStart(src[j]) {
					k := j
					for k < len(src) && isIdentChar(src[k]) {
						k++
					}
					return nil, fmt.Errorf("line %d: unknown unit %q (use ms s m h d w, B KB MB GB TB, KiB MiB GiB TiB)", line, src[j:k])
				}
				add(kNum, i, j)
				i = j
			}
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			add(kIdent, i, j)
			i = j
		case (c == '~' || c == '/') && pathStart(src, i):
			if t, ok := last(); ok && endsValue(t) {
				// Division or bitwise-not after a value.
				lexOp(src, &i, &toks, depth, line)
				continue
			}
			j := i + 1
			for j < len(src) && isPathChar(src[j]) {
				j++
			}
			add(kPath, i, j)
			i = j
		default:
			switch c {
			case '(', '[', '{':
				add(kOp, i, i+1)
				depth++
				i++
				continue
			case ')', ']', '}':
				depth--
				if depth < 0 {
					return nil, fmt.Errorf("line %d: unmatched %q", line, c)
				}
				add(kOp, i, i+1)
				i++
				continue
			}
			if !lexOp(src, &i, &toks, depth, line) {
				return nil, fmt.Errorf("line %d: unexpected character %q", line, c)
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unclosed bracket at end of input")
	}
	toks = append(toks, token{k: kEOF, start: len(src), end: len(src), depth: 0, line: line})
	return toks, nil
}

func lexOp(src string, i *int, toks *[]token, depth, line int) bool {
	for _, set := range [][]string{ops3, ops2} {
		for _, op := range set {
			if strings.HasPrefix(src[*i:], op) {
				*toks = append(*toks, token{k: kOp, text: op, start: *i, end: *i + len(op), depth: depth, line: line})
				*i += len(op)
				return true
			}
		}
	}
	if strings.ContainsRune("+-*/%<>=!|&^~.,:;@", rune(src[*i])) {
		*toks = append(*toks, token{k: kOp, text: src[*i : *i+1], start: *i, end: *i + 1, depth: depth, line: line})
		*i++
		return true
	}
	return false
}

// pathStart: "~" followed by "/" or a non-value char; "/" followed by a path char.
func pathStart(src string, i int) bool {
	if src[i] == '~' {
		return i+1 >= len(src) || src[i+1] == '/' || !isIdentChar(src[i+1]) && src[i+1] != '(' && src[i+1] != '-'
	}
	return i+1 < len(src) && (isIdentChar(src[i+1]) || src[i+1] == '.' || src[i+1] == '~')
}

func isStrStart(src string, i int) bool {
	c := src[i]
	if c == '"' || c == '\'' {
		return true
	}
	// prefixes: r, f, b, rb, br, fr, rf
	for _, p := range []string{"rb", "br", "fr", "rf", "r", "f", "b"} {
		if strings.HasPrefix(strings.ToLower(src[i:]), p) && i+len(p) < len(src) && (src[i+len(p)] == '"' || src[i+len(p)] == '\'') {
			if i > 0 && isIdentChar(src[i-1]) {
				return false
			}
			return true
		}
	}
	return false
}

func scanString(src string, i int) (int, error) {
	j := i
	for src[j] != '"' && src[j] != '\'' {
		j++
	}
	q := src[j]
	triple := strings.HasPrefix(src[j:], strings.Repeat(string(q), 3))
	if triple {
		end := strings.Index(src[j+3:], strings.Repeat(string(q), 3))
		if end < 0 {
			return 0, fmt.Errorf("unterminated triple-quoted string")
		}
		return j + 3 + end + 3, nil
	}
	k := j + 1
	for k < len(src) {
		switch src[k] {
		case '\\':
			k += 2
			continue
		case q:
			return k + 1, nil
		case '\n':
			return 0, fmt.Errorf("unterminated string")
		}
		k++
	}
	return 0, fmt.Errorf("unterminated string")
}

func isIdentStart(c byte) bool { return c == '_' || unicode.IsLetter(rune(c)) }
func isIdentChar(c byte) bool  { return c == '_' || unicode.IsLetter(rune(c)) || c >= '0' && c <= '9' }
func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == '_'
}
func isPathChar(c byte) bool {
	return c == '/' || c == '~' || c == '.' || c == '_' || c == '-' || c == '@' || c == '+' || c == '%' ||
		c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
