// Package argv validates parameters and substitutes them into frozen argv
// templates. It is shared by sysq (read-only primitives) and sysact (confirmed
// actions), so both obey the same rules:
//
//   - a template is a list of elements; each element is a string that may
//     contain {param} placeholders, or an optional group (a list of strings)
//     that is included only when every param it names has a value;
//   - a placeholder substitutes into exactly one argv element — never split,
//     never shell-quoted, never passed through a shell;
//   - values are validated against their spec (type, enum, bounds, pattern,
//     path allowlist) before substitution;
//   - a value that would start an element with "-" is refused (argument
//     injection) unless the spec says allow_dash.
package argv

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Spec describes one parameter.
type Spec struct {
	Type      string   `yaml:"type" json:"type"`
	Values    []string `yaml:"values,omitempty" json:"values,omitempty"`
	Default   any      `yaml:"default,omitempty" json:"default,omitempty"`
	Min       *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max       *float64 `yaml:"max,omitempty" json:"max,omitempty"`
	Pattern   string   `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	MaxLen    int      `yaml:"max_len,omitempty" json:"max_len,omitempty"`
	Required  bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Doc       string   `yaml:"doc,omitempty" json:"doc,omitempty"`
	AllowDash bool     `yaml:"allow_dash,omitempty" json:"allow_dash,omitempty"`
	// Render selects how a typed value becomes text: for duration "seconds"
	// (default), "minutes", "log" (e.g. 90m); for bytes "bytes" (default),
	// "kilobytes", "find_k" (+Nk).
	Render string `yaml:"render,omitempty" json:"render,omitempty"`
	// MustExist requires a path to exist (paths only).
	MustExist bool `yaml:"must_exist,omitempty" json:"must_exist,omitempty"`
	// Within further restricts a path to these roots (checked after the path
	// policy has cleaned it and resolved symlinks).
	Within []string `yaml:"within,omitempty" json:"within,omitempty"`
	// Predicate parameters: the dialect a predicate is rendered into and the
	// mapping from sysq column names to dialect field names.
	Dialect string            `yaml:"dialect,omitempty" json:"dialect,omitempty"`
	Fields  map[string]string `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// Param types.
const (
	TStr       = "str"
	TInt       = "int"
	TFloat     = "float"
	TBool      = "bool"
	TEnum      = "enum"
	TPath      = "path"
	TDuration  = "duration"
	TBytes     = "bytes"
	TPredicate = "predicate"
	TPID       = "pid"
)

// KnownTypes lists valid parameter types.
var KnownTypes = []string{TStr, TInt, TFloat, TBool, TEnum, TPath, TDuration, TBytes, TPredicate, TPID}

// PathPolicy decides which paths a parameter may name.
type PathPolicy interface {
	// CheckPath returns the cleaned absolute path or an error.
	CheckPath(p string) (string, error)
}

// Predicate is a rendered predicate (built by the planner from an Expr).
// The argv layer only passes it through as one element.
type Predicate interface {
	Render(dialect string, fields map[string]string) (string, error)
}

// Value is a validated parameter: its canonical Go value and argv text.
type Value struct {
	Typed any
	Text  string
	Set   bool
	Dash  bool // may start an element with '-'
}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid parameter")

func invalid(name, format string, args ...any) error {
	return fmt.Errorf("%w %q: %s", ErrInvalid, name, fmt.Sprintf(format, args...))
}

// Validate checks raw values against specs and returns canonical values. Raw
// values may be strings (from a CLI or HTTP) or typed Go values (from scripts).
// Unknown parameters are refused.
func Validate(specs map[string]Spec, raw map[string]any, pol PathPolicy) (map[string]Value, error) {
	out := map[string]Value{}
	for k := range raw {
		if _, ok := specs[k]; !ok {
			return nil, invalid(k, "unknown parameter")
		}
	}
	for name, spec := range specs {
		v, present := raw[name]
		if !present || v == nil {
			if spec.Default != nil {
				v = spec.Default
			} else if spec.Required {
				return nil, invalid(name, "required")
			} else {
				out[name] = Value{}
				continue
			}
		}
		val, err := validateOne(name, spec, v, pol)
		if err != nil {
			return nil, err
		}
		val.Dash = spec.AllowDash
		out[name] = val
	}
	return out, nil
}

func validateOne(name string, s Spec, v any, pol PathPolicy) (Value, error) {
	if str, ok := v.(string); ok {
		if strings.ContainsRune(str, 0) {
			return Value{}, invalid(name, "contains NUL")
		}
		if !utf8.ValidString(str) {
			return Value{}, invalid(name, "not valid UTF-8")
		}
		max := s.MaxLen
		if max == 0 {
			max = 1024
		}
		if len(str) > max {
			return Value{}, invalid(name, "longer than %d bytes", max)
		}
	}
	switch s.Type {
	case TStr:
		str, ok := v.(string)
		if !ok {
			str = fmt.Sprint(v)
		}
		if s.Pattern != "" {
			re, err := regexp.Compile(s.Pattern)
			if err != nil {
				return Value{}, invalid(name, "bad pattern in spec: %v", err)
			}
			if !re.MatchString(str) {
				return Value{}, invalid(name, "does not match %s", s.Pattern)
			}
		}
		if len(s.Values) > 0 && !contains(s.Values, str) {
			return Value{}, invalid(name, "must be one of %v", s.Values)
		}
		return Value{Typed: str, Text: str, Set: true}, nil
	case TEnum:
		str := fmt.Sprint(v)
		if !contains(s.Values, str) {
			return Value{}, invalid(name, "must be one of %v", s.Values)
		}
		return Value{Typed: str, Text: str, Set: true}, nil
	case TInt, TPID:
		n, err := toInt(v)
		if err != nil {
			return Value{}, invalid(name, "not an integer")
		}
		if err := bounds(name, s, float64(n)); err != nil {
			return Value{}, err
		}
		if s.Type == TPID && n <= 0 {
			return Value{}, invalid(name, "pid must be positive")
		}
		return Value{Typed: n, Text: strconv.FormatInt(n, 10), Set: true}, nil
	case TFloat:
		f, err := toFloat(v)
		if err != nil {
			return Value{}, invalid(name, "not a number")
		}
		if err := bounds(name, s, f); err != nil {
			return Value{}, err
		}
		return Value{Typed: f, Text: strconv.FormatFloat(f, 'f', -1, 64), Set: true}, nil
	case TBool:
		b, err := toBool(v)
		if err != nil {
			return Value{}, invalid(name, "not a boolean")
		}
		return Value{Typed: b, Text: strconv.FormatBool(b), Set: true}, nil
	case TDuration:
		sec, err := ParseDuration(v)
		if err != nil {
			return Value{}, invalid(name, "%v", err)
		}
		if sec < 0 {
			return Value{}, invalid(name, "negative duration")
		}
		if err := bounds(name, s, sec); err != nil {
			return Value{}, err
		}
		return Value{Typed: sec, Text: renderDuration(sec, s.Render), Set: true}, nil
	case TBytes:
		n, err := ParseBytes(v)
		if err != nil {
			return Value{}, invalid(name, "%v", err)
		}
		if n < 0 {
			return Value{}, invalid(name, "negative size")
		}
		if err := bounds(name, s, float64(n)); err != nil {
			return Value{}, err
		}
		return Value{Typed: n, Text: renderBytes(n, s.Render), Set: true}, nil
	case TPath:
		str, ok := v.(string)
		if !ok {
			if p, ok := v.(fmt.Stringer); ok {
				str = p.String()
			} else {
				return Value{}, invalid(name, "not a path")
			}
		}
		if pol == nil {
			return Value{}, invalid(name, "no path policy")
		}
		clean, err := pol.CheckPath(str)
		if err != nil {
			return Value{}, invalid(name, "%v", err)
		}
		if len(s.Within) > 0 {
			home := ""
			if hp, ok := pol.(interface{ HomeDir() string }); ok {
				home = hp.HomeDir()
			}
			ok := false
			for _, root := range s.Within {
				r := filepath.Clean(ExpandHome(root, home))
				if within(clean, r) || within(clean, resolve(r)) {
					ok = true
					break
				}
			}
			if !ok {
				return Value{}, invalid(name, "%s is not under %s", str, strings.Join(s.Within, ", "))
			}
		}
		if s.MustExist {
			if _, err := os.Lstat(clean); err != nil {
				return Value{}, invalid(name, "does not exist")
			}
		}
		return Value{Typed: clean, Text: clean, Set: true}, nil
	case TPredicate:
		p, ok := v.(Predicate)
		if !ok {
			return Value{}, invalid(name, "predicates are built from filter expressions, not passed as text")
		}
		text, err := p.Render(s.Dialect, s.Fields)
		if err != nil {
			return Value{}, invalid(name, "%v", err)
		}
		return Value{Typed: p, Text: text, Set: true}, nil
	}
	return Value{}, invalid(name, "unknown type %q", s.Type)
}

func bounds(name string, s Spec, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return invalid(name, "not finite")
	}
	if s.Min != nil && f < *s.Min {
		return invalid(name, "below minimum %v", *s.Min)
	}
	if s.Max != nil && f > *s.Max {
		return invalid(name, "above maximum %v", *s.Max)
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case int32:
		return int64(x), nil
	case float64:
		if x != math.Trunc(x) {
			return 0, errors.New("fractional")
		}
		return int64(x), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(x), 10, 64)
	}
	return 0, errors.New("type")
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case float64:
		return x, nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(x), 64)
	}
	return 0, errors.New("type")
}

func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		return strconv.ParseBool(x)
	}
	return false, errors.New("type")
}

// Seconds is implemented by duration values from scripts.
type Seconds interface{ Seconds() float64 }

// ByteCount is implemented by bytes values from scripts.
type ByteCount interface{ ByteCount() int64 }

var durRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(ms|s|m|h|d|w)?$`)

// ParseDuration accepts 30, "30s", "5m", "2h", "1d", "1w", "1.5h" or a Seconds value.
func ParseDuration(v any) (float64, error) {
	switch x := v.(type) {
	case Seconds:
		return x.Seconds(), nil
	case int, int64, float64:
		return toFloat(x)
	case string:
		m := durRe.FindStringSubmatch(strings.TrimSpace(x))
		if m == nil {
			return 0, fmt.Errorf("bad duration %q", x)
		}
		f, _ := strconv.ParseFloat(m[1], 64)
		mult := map[string]float64{"": 1, "ms": 0.001, "s": 1, "m": 60, "h": 3600, "d": 86400, "w": 604800}[m[2]]
		return f * mult, nil
	}
	return 0, fmt.Errorf("bad duration %v", v)
}

var bytesRe = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)\s*(b|kb|mb|gb|tb|kib|mib|gib|tib|k|m|g|t)?$`)

// ParseBytes accepts 1024, "512MB", "1.5GB", "4GiB" or a ByteCount value.
// KB/MB/GB are decimal (as Finder shows); KiB/MiB/GiB are binary.
func ParseBytes(v any) (int64, error) {
	switch x := v.(type) {
	case ByteCount:
		return x.ByteCount(), nil
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	case string:
		m := bytesRe.FindStringSubmatch(strings.TrimSpace(x))
		if m == nil {
			return 0, fmt.Errorf("bad size %q", x)
		}
		f, _ := strconv.ParseFloat(m[1], 64)
		mult := map[string]float64{
			"": 1, "b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
			"k": 1e3, "m": 1e6, "g": 1e9, "t": 1e12,
			"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
		}[strings.ToLower(m[2])]
		return int64(f * mult), nil
	}
	return 0, fmt.Errorf("bad size %v", v)
}

func renderDuration(sec float64, how string) string {
	switch how {
	case "minutes":
		return strconv.FormatInt(int64(math.Ceil(sec/60)), 10)
	case "find_mtime":
		// find -mtime -7200s: modified less than this long ago
		return "-" + strconv.FormatInt(int64(math.Max(1, math.Ceil(sec))), 10) + "s"
	case "log":
		if sec >= 3600 && math.Mod(sec, 3600) == 0 {
			return fmt.Sprintf("%dh", int64(sec/3600))
		}
		return fmt.Sprintf("%dm", int64(math.Max(1, math.Ceil(sec/60))))
	}
	return strconv.FormatInt(int64(math.Ceil(sec)), 10)
}

func renderBytes(n int64, how string) string {
	switch how {
	case "kilobytes":
		return strconv.FormatInt((n+1023)/1024, 10)
	case "find_k":
		return "+" + strconv.FormatInt(n/1024, 10) + "k"
	}
	return strconv.FormatInt(n, 10)
}

// Element is one template element: a string, or an optional group.
type Element struct {
	Text  string
	Group []string
}

// placeholder matches {name}; {{ and }} are literal braces.
var placeholder = regexp.MustCompile(`\{\{|\}\}|\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Names returns the parameter names an element refers to.
func Names(s string) []string {
	var out []string
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		if m[1] != "" {
			out = append(out, m[1])
		}
	}
	return out
}

// Build substitutes validated values into a template. Every placeholder must
// name a declared parameter; a required element naming an unset parameter is
// an error; an optional group naming an unset parameter is dropped.
func Build(tmpl []Element, vals map[string]Value) ([]string, error) {
	var out []string
	for _, el := range tmpl {
		if el.Group != nil {
			ok := true
			for _, s := range el.Group {
				for _, n := range Names(s) {
					if v, found := vals[n]; !found || !v.Set {
						ok = false
					}
				}
			}
			if !ok {
				continue
			}
			for _, s := range el.Group {
				r, err := fill(s, vals)
				if err != nil {
					return nil, err
				}
				out = append(out, r)
			}
			continue
		}
		r, err := fill(el.Text, vals)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func fill(s string, vals map[string]Value) (string, error) {
	var err error
	startsWithPlaceholder := false
	if loc := placeholder.FindStringSubmatchIndex(s); loc != nil && loc[0] == 0 && loc[2] >= 0 {
		startsWithPlaceholder = true
	}
	res := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		switch m {
		case "{{":
			return "{"
		case "}}":
			return "}"
		}
		name := m[1 : len(m)-1]
		v, ok := vals[name]
		if !ok {
			err = fmt.Errorf("template names undeclared parameter %q", name)
			return ""
		}
		if !v.Set {
			err = fmt.Errorf("parameter %q has no value", name)
			return ""
		}
		return v.Text
	})
	if err != nil {
		return "", err
	}
	if strings.ContainsRune(res, 0) {
		return "", errors.New("argv element contains NUL")
	}
	if startsWithPlaceholder && strings.HasPrefix(res, "-") {
		name := Names(s)[0]
		if !vals[name].allowDash() {
			return "", fmt.Errorf("%w %q: value may not start with '-'", ErrInvalid, name)
		}
	}
	return res, nil
}

// allowDash: the spec allows it, or the value is a number whose bounds were
// already checked.
func (v Value) allowDash() bool {
	switch v.Typed.(type) {
	case int64, float64:
		return true
	}
	return v.Dash
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// RootPolicy allows paths under Allowed roots and refuses Excluded subtrees.
type RootPolicy struct {
	Home     string
	Allowed  []string
	Excluded []string
}

// HomeDir returns the home directory used for ~ expansion.
func (r RootPolicy) HomeDir() string { return r.Home }

// CheckPath implements PathPolicy.
func (r RootPolicy) CheckPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("empty path")
	}
	p = ExpandHome(p, r.Home)
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute or start with ~")
	}
	clean := filepath.Clean(p)
	// Resolve symlinks in existing prefixes so a link can't escape the roots.
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	}
	// Excludes compare case-folded: macOS volumes are case-insensitive by
	// default, so ~/.SSH is ~/.ssh.
	folded := strings.ToLower(clean)
	for _, ex := range r.Excluded {
		ex = filepath.Clean(ExpandHome(ex, r.Home))
		if within(folded, strings.ToLower(ex)) || within(folded, strings.ToLower(resolve(ex))) {
			return "", fmt.Errorf("%s is excluded", p)
		}
	}
	for _, root := range r.Allowed {
		root = filepath.Clean(ExpandHome(root, r.Home))
		if within(clean, root) || within(clean, resolve(root)) {
			return clean, nil
		}
	}
	return "", fmt.Errorf("%s is outside the allowed roots", p)
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func within(p, root string) bool {
	if root == "/" {
		return true
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
