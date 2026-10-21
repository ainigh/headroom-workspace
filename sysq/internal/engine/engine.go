// Package engine runs primitives: validation, policy, sandboxed execution
// (or recorded fixtures), parsing, redaction, caps, caching and the cost
// ledger. Scripts, recipes, the sampler and the HTTP layer all call through
// here, so one cache is shared across UI, API and assistant.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/exec"
	"github.com/ainigh/headroom-workspace/sysq/internal/parsers"
	"github.com/ainigh/headroom-workspace/sysq/internal/policy"
	"github.com/ainigh/headroom-workspace/sysq/internal/redact"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

// BuiltinFunc implements a primitive in Go (history.* over the store).
type BuiltinFunc func(ctx context.Context, p *catalog.Primitive, params map[string]any) (*table.Table, error)

// Engine executes primitives.
type Engine struct {
	Catalog  *catalog.Catalog
	Policy   policy.Policy
	Runner   *exec.Runner
	Home     string
	Fixtures fs.FS // non-nil: serve recorded output instead of executing
	Now      func() time.Time
	Loc      *time.Location

	mu       sync.Mutex
	builtins map[string]BuiltinFunc
	cache    map[string]cacheEntry
	inflight map[string]*sync.Mutex
	sudoOK   map[string]bool
}

type cacheEntry struct {
	t       *table.Table
	expires time.Time
}

// Errors returned at plan time (before anything executes).
var (
	ErrUnknown     = errors.New("unknown primitive")
	ErrSensitivity = errors.New("sensitivity class not enabled")
	ErrBudget      = errors.New("cost budget exhausted")
)

// Ledger charges primitive calls against a cost budget (design §17).
type Ledger struct {
	mu         sync.Mutex
	Budget     int
	Spent      int
	Primitives []string
	Calls      int
}

// Charge records a call; it fails when the budget would be exceeded.
func (l *Ledger) Charge(name, cost string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	units := catalog.CostUnits[cost]
	if l.Budget > 0 && l.Spent+units > l.Budget {
		return fmt.Errorf("%w: %s needs %d units, %d of %d left", ErrBudget, name, units, l.Budget-l.Spent, l.Budget)
	}
	l.Spent += units
	l.Calls++
	found := false
	for _, p := range l.Primitives {
		if p == name {
			found = true
		}
	}
	if !found {
		l.Primitives = append(l.Primitives, name)
	}
	return nil
}

// Snapshot returns a copy of the ledger's counters.
func (l *Ledger) Snapshot() (spent, calls int, prims []string) {
	if l == nil {
		return 0, 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Spent, l.Calls, append([]string(nil), l.Primitives...)
}

// CallOpts carries caller limits.
type CallOpts struct {
	MaxSensitivity string // "" = policy's highest enabled class
	Ledger         *Ledger
	Fresh          bool // bypass the cache
}

// RegisterBuiltin installs a Go-implemented primitive body.
func (e *Engine) RegisterBuiltin(name string, f BuiltinFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.builtins == nil {
		e.builtins = map[string]BuiltinFunc{}
	}
	e.builtins[name] = f
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Allowed checks sensitivity for a caller at plan time.
func (e *Engine) Allowed(sensitivity string, opts CallOpts) error {
	if !e.Policy.SensitivityEnabled(sensitivity) {
		return fmt.Errorf("%w: %s (enable it in policy.yaml)", ErrSensitivity, sensitivity)
	}
	if opts.MaxSensitivity != "" && table.SensitivityRank(sensitivity) > table.SensitivityRank(opts.MaxSensitivity) {
		return fmt.Errorf("%w: %s exceeds this caller's limit (%s)", ErrSensitivity, sensitivity, opts.MaxSensitivity)
	}
	return nil
}

// PathPolicy returns the parameter path policy.
func (e *Engine) PathPolicy() argv.PathPolicy { return e.Policy.Paths(e.Home) }

// Validate checks params for a primitive without running it.
func (e *Engine) Validate(p *catalog.Primitive, raw map[string]any) (map[string]argv.Value, error) {
	specs := p.Params
	if e.Fixtures != nil {
		specs = map[string]argv.Spec{}
		for k, s := range p.Params {
			s.MustExist = false
			specs[k] = s
		}
	}
	return argv.Validate(specs, raw, e.PathPolicy())
}

// Primitive runs one primitive. Plan-time problems (unknown name, invalid
// params, disabled sensitivity, budget) are errors; runtime failures come back
// as a table with meta.error (errors are values, design §14.3).
func (e *Engine) Primitive(ctx context.Context, name string, raw map[string]any, opts CallOpts) (*table.Table, error) {
	p, ok := e.Catalog.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknown, name)
	}
	if err := e.Allowed(p.Sensitivity, opts); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	vals, err := e.Validate(p, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := opts.Ledger.Charge(name, p.Cost); err != nil {
		return nil, err
	}
	key := cacheKey(name, vals)
	if !opts.Fresh && p.CacheTTL > 0 {
		if t := e.cached(key); t != nil {
			return t, nil
		}
	}
	lock := e.keyLock(key)
	lock.Lock()
	defer lock.Unlock()
	if !opts.Fresh && p.CacheTTL > 0 {
		if t := e.cached(key); t != nil {
			return t, nil
		}
	}
	t := e.execute(ctx, p, vals)
	t.Meta.Source = []string{name}
	t.Meta.Sensitivity = p.Sensitivity
	if t.Meta.SnapshotTS.IsZero() {
		t.Meta.SnapshotTS = e.now()
	}
	if p.Redact {
		t.Meta.Redacted += Redact(t)
	}
	ApplyCaps(t, e.Policy.Caps)
	if p.CacheTTL > 0 && t.Meta.Error == "" {
		e.mu.Lock()
		if e.cache == nil {
			e.cache = map[string]cacheEntry{}
		}
		e.cache[key] = cacheEntry{t: t.Clone(), expires: e.now().Add(p.CacheTTL)}
		e.mu.Unlock()
	}
	return t, nil
}

func (e *Engine) cached(key string) *table.Table {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.cache[key]; ok && e.now().Before(c.expires) {
		t := c.t.Clone()
		t.Meta.Cached = true
		return t
	}
	return nil
}

func (e *Engine) keyLock(key string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inflight == nil {
		e.inflight = map[string]*sync.Mutex{}
	}
	m, ok := e.inflight[key]
	if !ok {
		m = &sync.Mutex{}
		e.inflight[key] = m
	}
	return m
}

// FlushCache drops cached results (tests, `sysq run --fresh`).
func (e *Engine) FlushCache() {
	e.mu.Lock()
	e.cache = nil
	e.mu.Unlock()
}

func cacheKey(name string, vals map[string]argv.Value) string {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		if vals[k].Set {
			fmt.Fprintf(&b, "\x00%s=%s", k, vals[k].Text)
		}
	}
	return b.String()
}

func typed(vals map[string]argv.Value) map[string]any {
	out := map[string]any{}
	for k, v := range vals {
		if v.Set {
			out[k] = v.Typed
		}
	}
	return out
}

func (e *Engine) execute(ctx context.Context, p *catalog.Primitive, vals map[string]argv.Value) *table.Table {
	start := time.Now()
	fail := func(format string, args ...any) *table.Table {
		t := table.New(p.Schema)
		t.Meta.Error = fmt.Sprintf(format, args...)
		t.Meta.CostMS = time.Since(start).Milliseconds()
		return t
	}
	unavailable := func(why string) *table.Table {
		t := table.New(p.Schema)
		t.Meta.Unavailable = why
		return t
	}
	if p.Network && !e.Policy.NetworkEnabled(p.Name) {
		return unavailable("network primitive; enable it by name in policy.yaml network_primitives")
	}
	if p.Builtin != "" {
		e.mu.Lock()
		f := e.builtins[p.Builtin]
		e.mu.Unlock()
		if f == nil {
			return unavailable("builtin " + p.Builtin + " is not running in this process (it needs sysqd)")
		}
		t, err := f(ctx, p, typed(vals))
		if err != nil {
			return fail("%v", err)
		}
		t.Meta.CostMS = time.Since(start).Milliseconds()
		return t
	}
	outputs := map[string][]byte{}
	truncated := false
	if e.Fixtures != nil {
		for _, c := range p.AllCommands() {
			file := p.Name + "." + c.ID + ".txt"
			if c.ID == "main" {
				file = p.Name + ".txt"
			}
			data, err := fs.ReadFile(e.Fixtures, file)
			if err != nil {
				if c.Optional {
					continue
				}
				return unavailable("no fixture " + file)
			}
			outputs[c.ID] = data
		}
	} else {
		if p.RequiresSudo != nil && !e.sudoAllowed(ctx, p) {
			return unavailable(fmt.Sprintf("needs a NOPASSWD sudoers rule for exactly: %s %s", p.RequiresSudo.Binary, strings.Join(p.RequiresSudo.Args, " ")))
		}
		for _, c := range p.AllCommands() {
			args, err := e.commandArgv(c, vals)
			if err != nil {
				if c.Optional {
					continue
				}
				if errors.Is(err, errNotInstalled) {
					return unavailable(err.Error())
				}
				return fail("%v", err)
			}
			res, err := e.Runner.Run(ctx, exec.Invocation{
				Argv: args, Profile: p.Sandbox, Execs: p.SandboxExec,
				Timeout: p.Timeout, MaxBytes: p.MaxBytes, PartialOK: p.PartialOK,
			})
			if err != nil {
				if c.Optional {
					continue
				}
				return fail("%v", err)
			}
			if res.ExitCode != 0 && !allowed(c.AllowExit, res.ExitCode) {
				if c.Optional {
					continue
				}
				return fail("%s exited %d: %s", filepath.Base(args[0]), res.ExitCode, firstLine(res.Stderr))
			}
			truncated = truncated || res.Truncated || res.TimedOut
			outputs[c.ID] = res.Stdout
		}
	}
	parse, ok := parsers.Get(p.Parser)
	if !ok {
		return fail("parser %q is not registered", p.Parser)
	}
	rows, err := parse(parsers.Input{Outputs: outputs, Params: typed(vals), Now: e.now(), Loc: e.Loc, Home: e.Home})
	if err != nil {
		return fail("parse: %v", err)
	}
	t, err := parsers.Build(p.Schema, rows)
	if err != nil {
		return fail("parse: %v", err)
	}
	t.Meta.Truncated = truncated
	t.Meta.CostMS = time.Since(start).Milliseconds()
	t.Meta.Fixture = e.Fixtures != nil
	return t
}

var errNotInstalled = errors.New("not installed")

func (e *Engine) commandArgv(c catalog.Command, vals map[string]argv.Value) ([]string, error) {
	tmpl := append(catalog.Template(nil), c.Argv...)
	if tmpl[0].Text == "{binary}" {
		found := ""
		for _, b := range c.BinaryPaths {
			if st, err := os.Stat(b); err == nil && !st.IsDir() {
				found = b
				break
			}
		}
		if found == "" {
			return nil, fmt.Errorf("%w: none of %s", errNotInstalled, strings.Join(c.BinaryPaths, ", "))
		}
		tmpl[0].Text = found
	} else if _, err := os.Stat(tmpl[0].Text); err != nil {
		return nil, fmt.Errorf("%w: %s", errNotInstalled, tmpl[0].Text)
	}
	return argv.Build(tmpl, vals)
}

func (e *Engine) sudoAllowed(ctx context.Context, p *catalog.Primitive) bool {
	e.mu.Lock()
	ok, checked := e.sudoOK[p.Name]
	e.mu.Unlock()
	if checked {
		return ok
	}
	if runtime.GOOS == "darwin" {
		args := append([]string{"/usr/bin/sudo", "-n", "-l", p.RequiresSudo.Binary}, p.RequiresSudo.Args...)
		res, err := e.Runner.Run(ctx, exec.Invocation{Argv: args, Profile: "read-only-sudo", Execs: []string{p.RequiresSudo.Binary}, Timeout: 5 * time.Second})
		ok = err == nil && res.ExitCode == 0
	}
	e.mu.Lock()
	if e.sudoOK == nil {
		e.sudoOK = map[string]bool{}
	}
	e.sudoOK[p.Name] = ok
	e.mu.Unlock()
	return ok
}

// SudoRules gives, for each sudo primitive, the exact command line its
// NOPASSWD sudoers rule must allow (what Headroom shows the user to add).
func (e *Engine) SudoRules() map[string]string {
	out := map[string]string{}
	for _, p := range e.Catalog.All() {
		if p.RequiresSudo != nil {
			out[p.Name] = strings.Join(append([]string{p.RequiresSudo.Binary}, p.RequiresSudo.Args...), " ")
		}
	}
	return out
}

// RecheckSudo forgets the cached `sudo -n -l` answers, so a rule added
// since sysqd started is noticed without restarting it.
func (e *Engine) RecheckSudo() {
	e.mu.Lock()
	e.sudoOK = nil
	e.mu.Unlock()
}

// SudoStatus reports, for each sudo primitive, whether its rule exists.
func (e *Engine) SudoStatus(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for _, p := range e.Catalog.All() {
		if p.RequiresSudo != nil {
			out[p.Name] = e.Fixtures != nil || e.sudoAllowed(ctx, p)
		}
	}
	return out
}

func allowed(codes []int, c int) bool {
	for _, x := range codes {
		if x == c {
			return true
		}
	}
	return false
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Redact masks secrets in every string cell (recursively) and returns the
// number of masks applied.
func Redact(t *table.Table) int {
	n := 0
	for _, row := range t.Rows {
		for j, v := range row {
			switch x := v.(type) {
			case string:
				r, k := redact.String(x)
				if k > 0 {
					row[j] = r
					n += k
				}
			case *table.Table:
				n += Redact(x)
			}
		}
	}
	return n
}

// ApplyCaps enforces row, byte and nesting caps (design §6.2, §23.6).
func ApplyCaps(t *table.Table, caps policy.Caps) {
	if caps.Rows > 0 && len(t.Rows) > caps.Rows {
		t.Meta.DroppedByLimit += len(t.Rows) - caps.Rows
		t.Rows = t.Rows[:caps.Rows]
		t.Meta.Truncated = true
	}
	if caps.Depth > 0 && capDepth(t, 1, caps.Depth) {
		t.Meta.Truncated = true
	}
	if caps.Bytes > 0 {
		if size := jsonSize(t); size > caps.Bytes {
			lo, hi := 0, len(t.Rows)
			for lo < hi {
				mid := (lo + hi + 1) / 2
				if jsonSize(t.WithRows(t.Rows[:mid])) <= caps.Bytes {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			t.Meta.DroppedByLimit += len(t.Rows) - lo
			t.Rows = t.Rows[:lo]
			t.Meta.Truncated = true
		}
	}
	t.Fix()
}

func capDepth(t *table.Table, depth, max int) bool {
	cut := false
	for _, row := range t.Rows {
		for j, v := range row {
			if sub, ok := v.(*table.Table); ok {
				if depth >= max {
					row[j] = nil
					cut = true
				} else if capDepth(sub, depth+1, max) {
					cut = true
				}
			}
		}
	}
	return cut
}

func jsonSize(t *table.Table) int {
	b, err := json.Marshal(t)
	if err != nil {
		return 0
	}
	return len(b)
}

// Capture runs a primitive's commands live (sandboxed, same argv as a real
// call) and returns raw stdout per command id, for `sysq fixture capture`.
func (e *Engine) Capture(ctx context.Context, name string, raw map[string]any) (map[string][]byte, error) {
	p, ok := e.Catalog.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknown, name)
	}
	if p.Builtin != "" {
		return nil, fmt.Errorf("%s is a builtin; it has no command output", name)
	}
	if p.Network && !e.Policy.NetworkEnabled(p.Name) {
		return nil, fmt.Errorf("%s is a network primitive and is not enabled", name)
	}
	vals, err := e.Validate(p, raw)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, c := range p.AllCommands() {
		args, err := e.commandArgv(c, vals)
		if err != nil {
			if c.Optional {
				continue
			}
			return nil, err
		}
		res, err := e.Runner.Run(ctx, exec.Invocation{Argv: args, Profile: p.Sandbox, Execs: p.SandboxExec, Timeout: p.Timeout, MaxBytes: p.MaxBytes, PartialOK: p.PartialOK})
		if err != nil {
			if c.Optional {
				continue
			}
			return nil, err
		}
		if res.ExitCode != 0 && !allowed(c.AllowExit, res.ExitCode) && !c.Optional {
			return nil, fmt.Errorf("%s exited %d: %s", filepath.Base(args[0]), res.ExitCode, firstLine(res.Stderr))
		}
		out[c.ID] = res.Stdout
	}
	return out, nil
}
