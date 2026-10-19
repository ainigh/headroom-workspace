// Package server is sysact's confirmed mutation runner (design §27):
// prepare(name, params) validates one catalog action and returns a plan with
// its effect statement; execute(plan_id, confirmations) runs it only with a
// host-signed confirmation of that exact plan. Unix socket, JSON lines.
package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ainigh/headroom-workspace/sysact/internal/actions"
	"github.com/ainigh/headroom-workspace/sysact/internal/confirm"
	"github.com/ainigh/headroom-workspace/sysact/internal/protect"
	"github.com/ainigh/headroom-workspace/sysact/internal/run"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
	"github.com/ainigh/headroom-workspace/sysq/pkg/audit"
)

// PlanTTL is how long a prepared plan stays executable.
var PlanTTL = 5 * time.Minute

// Plan is a validated, not-yet-run action.
type Plan struct {
	ID            string            `json:"plan_id"`
	Hash          string            `json:"plan_hash"`
	Name          string            `json:"name"`
	Doc           string            `json:"doc"`
	Tier          string            `json:"tier"`
	Params        map[string]string `json:"params"`
	Effect        string            `json:"effect"`
	UndoHint      string            `json:"undo_hint,omitempty"`
	RequiresAdmin bool              `json:"requires_admin"`
	Command       string            `json:"command"`
	ExpiresAt     time.Time         `json:"expires_at"`
	Warnings      []string          `json:"warnings,omitempty"`
	DryRun        bool              `json:"dry_run,omitempty"`

	cmd run.Command
}

// Outcome is what execute returns.
type Outcome struct {
	PlanID string `json:"plan_id"`
	Name   string `json:"name"`
	run.Result
}

// ProcessInfo is what lookup: process resolves.
type ProcessInfo struct {
	Name, User string
}

// Server holds the catalog, plans and the host key.
type Server struct {
	catalog *actions.Catalog
	protect *protect.Rules
	key     []byte
	dryRun  bool
	audit   *audit.Log
	runner  run.Runner
	lookup  func(ctx context.Context, pid int64) (ProcessInfo, error)
	home    string
	now     func() time.Time
	exists  func(path string) bool

	mu     sync.Mutex
	plans  map[string]*Plan
	nonces map[string]time.Time
}

// Options configure a server.
type Options struct {
	Catalog *actions.Catalog
	Protect *protect.Rules
	Key     []byte
	DryRun  bool
	Audit   *audit.Log
	Home    string
}

// New returns a server that runs commands on this Mac.
func New(o Options) *Server {
	return &Server{catalog: o.Catalog, protect: o.Protect, key: o.Key, dryRun: o.DryRun, audit: o.Audit,
		runner: run.OS{}, lookup: psLookup, home: o.Home, now: time.Now, exists: fileExists,
		plans: map[string]*Plan{}, nonces: map[string]time.Time{}}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func psLookup(ctx context.Context, pid int64) (ProcessInfo, error) {
	out, err := exec.CommandContext(ctx, "/bin/ps", "-o", "user=", "-o", "comm=", "-p", strconv.FormatInt(pid, 10)).Output()
	if err != nil {
		return ProcessInfo{}, fmt.Errorf("no process %d", pid)
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) < 2 {
		return ProcessInfo{}, fmt.Errorf("no process %d", pid)
	}
	return ProcessInfo{User: f[0], Name: filepath.Base(strings.Join(f[1:], " "))}, nil
}

// Catalog returns the actions.
func (s *Server) Catalog() []*actions.Action { return s.catalog.All() }

func randomID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

type pathPolicy struct{ r *protect.Rules }

func (p pathPolicy) CheckPath(path string) (string, error) { return p.r.Check(path) }

// Prepare validates an action request and stores a plan.
func (s *Server) Prepare(ctx context.Context, name string, raw map[string]any) (*Plan, error) {
	p, err := s.prepare(ctx, name, raw)
	rec := audit.Record{Component: "sysact", Caller: "host", Kind: "action.prepare", Name: name, Params: raw}
	if err != nil {
		rec.Error = err.Error()
	} else {
		rec.Extra = map[string]any{"plan_id": p.ID, "command": p.Command, "tier": p.Tier}
	}
	if s.audit != nil {
		_ = s.audit.Write(rec)
	}
	return p, err
}

func (s *Server) prepare(ctx context.Context, name string, raw map[string]any) (*Plan, error) {
	a, ok := s.catalog.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown action %q (see the catalog)", name)
	}
	vals, err := argv.Validate(a.Params, raw, pathPolicy{s.protect})
	if err != nil {
		return nil, err
	}
	texts := map[string]string{}
	for k, v := range vals {
		if v.Set {
			texts[k] = v.Text
		}
	}
	fill := func(tmpl string) string {
		out := tmpl
		for k, v := range texts {
			out = strings.ReplaceAll(out, "{"+k+"}", v)
		}
		return out
	}
	extra := map[string]string{}
	var warnings []string
	if a.Lookup == "process" {
		pid, _ := vals["pid"].Typed.(int64)
		if pid <= 1 || pid == int64(os.Getpid()) {
			return nil, fmt.Errorf("pid %d may not be targeted", pid)
		}
		info, err := s.lookup(ctx, pid)
		if err != nil {
			return nil, err
		}
		if u, err := user.Current(); err == nil && info.User != u.Username {
			return nil, fmt.Errorf("pid %d belongs to %s; sysact only acts on your own processes", pid, info.User)
		}
		for _, sys := range []string{"launchd", "WindowServer", "loginwindow", "kernel_task", "sysqd", "sysact"} {
			if info.Name == sys {
				return nil, fmt.Errorf("%s may not be force-quit", info.Name)
			}
		}
		extra["process"], extra["owner"] = info.Name, info.User
		warnings = append(warnings, "Unsaved work in "+info.Name+" will be lost.")
	}
	cmd := run.Command{Kind: a.Kind, Admin: a.RequiresAdmin, Timeout: a.Timeout}
	switch a.Kind {
	case actions.KindArgv:
		tmpl := append(actions.Template(nil), a.Argv...)
		if tmpl[0].Text == "{binary}" {
			found := ""
			for _, b := range a.BinaryPaths {
				if s.exists(b) {
					found = b
					break
				}
			}
			if found == "" {
				return nil, fmt.Errorf("%s is not installed (looked in %s)", a.Name, strings.Join(a.BinaryPaths, ", "))
			}
			tmpl[0].Text = found
		}
		args, err := argv.Build(tmpl, vals)
		if err != nil {
			return nil, err
		}
		cmd.Argv = args
	default:
		target := fill(a.Target)
		if a.Kind == actions.KindTrash {
			clean, err := s.protect.Check(target)
			if err != nil {
				return nil, err
			}
			if len(a.Within) > 0 {
				inside := false
				for _, w := range a.Within {
					root, _ := filepath.EvalSymlinks(expand(w, s.home))
					if root == "" {
						root = expand(w, s.home)
					}
					real, _ := filepath.EvalSymlinks(clean)
					if real == "" {
						real = clean
					}
					if strings.HasPrefix(real, root+"/") {
						inside = true
					}
				}
				if !inside {
					return nil, fmt.Errorf("%s is outside %s", target, strings.Join(a.Within, ", "))
				}
			}
			if a.MustExist {
				if _, err := os.Lstat(clean); err != nil {
					return nil, fmt.Errorf("%s does not exist", target)
				}
			}
			target = clean
		}
		if strings.ContainsRune(target, 0) || strings.HasPrefix(target, "-") {
			return nil, fmt.Errorf("bad target")
		}
		cmd.Target = target
	}
	effect := fill(a.Effect)
	for k, v := range extra {
		effect = strings.ReplaceAll(effect, "{"+k+"}", v)
	}
	if a.RequiresAdmin {
		warnings = append(warnings, "macOS will ask for an administrator password.")
	}
	p := &Plan{
		ID: randomID("plan-"), Name: a.Name, Doc: a.Doc, Tier: a.Tier, Params: texts, Effect: effect,
		UndoHint: fill(a.UndoHint), RequiresAdmin: a.RequiresAdmin, Command: cmd.Display(),
		ExpiresAt: s.now().Add(PlanTTL), Warnings: warnings, DryRun: s.dryRun, cmd: cmd,
	}
	p.Hash = hashPlan(p)
	s.mu.Lock()
	s.gc()
	s.plans[p.ID] = p
	s.mu.Unlock()
	return p, nil
}

func expand(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func hashPlan(p *Plan) string {
	keys := make([]string, 0, len(p.Params))
	for k := range p.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("sysact-plan-v1\n" + p.ID + "\n" + p.Name + "\n")
	for _, k := range keys {
		b.WriteString(k + "=" + p.Params[k] + "\n")
	}
	argvJSON, _ := json.Marshal(run.Argv(p.cmd))
	b.Write(argvJSON)
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
}

func (s *Server) gc() {
	now := s.now()
	for id, p := range s.plans {
		if now.After(p.ExpiresAt) {
			delete(s.plans, id)
		}
	}
	for n, t := range s.nonces {
		if now.Sub(t) > 10*time.Minute {
			delete(s.nonces, n)
		}
	}
}

// PlanByID returns a pending plan.
func (s *Server) PlanByID(id string) (*Plan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if ok && s.now().After(p.ExpiresAt) {
		return nil, false
	}
	return p, ok
}

// ErrDryRun is returned by execute in --dry-run mode.
var ErrDryRun = errors.New("sysact is in dry-run mode: plans are returned, execution is refused")

// Execute runs a plan that carries a valid host confirmation.
func (s *Server) Execute(ctx context.Context, planID string, recs []confirm.Record) (*Outcome, error) {
	out, plan, err := s.execute(ctx, planID, recs)
	rec := audit.Record{Component: "sysact", Caller: "host", Kind: "action.execute"}
	if plan != nil {
		rec.Name = plan.Name
		rec.Extra = map[string]any{"plan_id": plan.ID, "command": plan.Command, "tier": plan.Tier, "params": plan.Params}
	}
	vias := []string{}
	for _, r := range recs {
		vias = append(vias, r.Via)
	}
	if rec.Extra == nil {
		rec.Extra = map[string]any{}
	}
	rec.Extra["confirmed_via"] = vias
	if err != nil {
		rec.Error = err.Error()
	} else {
		rec.Extra["ok"] = out.OK
		rec.Extra["exit_code"] = out.ExitCode
	}
	if s.audit != nil {
		_ = s.audit.Write(rec)
	}
	return out, err
}

func (s *Server) execute(ctx context.Context, planID string, recs []confirm.Record) (*Outcome, *Plan, error) {
	if s.dryRun {
		return nil, nil, ErrDryRun
	}
	if len(recs) == 0 {
		return nil, nil, confirm.ErrNone
	}
	s.mu.Lock()
	plan, ok := s.plans[planID]
	if !ok || s.now().After(plan.ExpiresAt) {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("no pending plan %s (plans expire after %s and run once)", planID, PlanTTL)
	}
	now := s.now()
	for _, r := range recs {
		err := confirm.Verify(s.key, r, plan.ID, plan.Hash, now, func(n string) bool {
			_, used := s.nonces[n]
			return used
		})
		if err != nil {
			s.mu.Unlock()
			return nil, plan, fmt.Errorf("refused: %w", err)
		}
	}
	for _, r := range recs {
		s.nonces[r.Nonce] = now
	}
	delete(s.plans, planID) // single use, before running
	s.mu.Unlock()
	res, err := s.runner.Run(ctx, plan.cmd)
	if err != nil {
		return nil, plan, err
	}
	return &Outcome{PlanID: plan.ID, Name: plan.Name, Result: res}, plan, nil
}

// ---- socket protocol ---------------------------------------------------------------

type request struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	ID     int    `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  *rpcEr `json:"error,omitempty"`
}

type rpcEr struct {
	Message string `json:"message"`
}

type catalogEntry struct {
	Name          string               `json:"name"`
	Doc           string               `json:"doc"`
	Tier          string               `json:"tier"`
	RequiresAdmin bool                 `json:"requires_admin"`
	Params        map[string]argv.Spec `json:"params"`
	ParamOrder    []string             `json:"param_order"`
	Effect        string               `json:"effect"`
	UndoHint      string               `json:"undo_hint,omitempty"`
}

// Handle answers one request.
func (s *Server) Handle(ctx context.Context, req request) response {
	resp := response{ID: req.ID}
	fail := func(err error) response {
		resp.Error = &rpcEr{Message: err.Error()}
		return resp
	}
	switch req.Method {
	case "status":
		resp.Result = map[string]any{"dry_run": s.dryRun, "actions": len(s.catalog.All()), "pid": os.Getpid()}
	case "catalog":
		var out []catalogEntry
		for _, a := range s.catalog.All() {
			out = append(out, catalogEntry{a.Name, a.Doc, a.Tier, a.RequiresAdmin, a.Params, a.ParamOrder, a.Effect, a.UndoHint})
		}
		resp.Result = map[string]any{"actions": out}
	case "prepare":
		var p struct {
			Name   string         `json:"name"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(err)
		}
		plan, err := s.Prepare(ctx, p.Name, p.Params)
		if err != nil {
			return fail(err)
		}
		resp.Result = plan
	case "execute":
		var p struct {
			PlanID        string           `json:"plan_id"`
			Confirmations []confirm.Record `json:"confirmations"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(err)
		}
		out, err := s.Execute(ctx, p.PlanID, p.Confirmations)
		if err != nil {
			return fail(err)
		}
		resp.Result = out
	default:
		return fail(fmt.Errorf("unknown method %q (status, catalog, prepare, execute)", req.Method))
	}
	return resp
}

// Serve answers JSON-line requests on a unix socket until ctx is done.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	go func() { <-ctx.Done(); l.Close() }()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.conn(ctx, c)
	}
}

func (s *Server) conn(ctx context.Context, c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), 256<<10)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(response{Error: &rpcEr{Message: "bad request: " + err.Error()}})
			continue
		}
		_ = c.SetWriteDeadline(time.Now().Add(10 * time.Minute))
		_ = enc.Encode(s.Handle(ctx, req))
	}
}

// Listen opens the owner-only unix socket.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return nil, fmt.Errorf("another sysact is listening on %s", path)
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
