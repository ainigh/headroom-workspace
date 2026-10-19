package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/expr"
	"github.com/ainigh/headroom-workspace/sysq/internal/history"
	"github.com/ainigh/headroom-workspace/sysq/internal/plan"
	"github.com/ainigh/headroom-workspace/sysq/internal/recipes"
	"github.com/ainigh/headroom-workspace/sysq/internal/sampler"
	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
	"github.com/ainigh/headroom-workspace/sysq/pkg/audit"
)

// Backend is everything the API serves from; swapped atomically on reload.
type Backend struct {
	Engine        *engine.Engine
	Recipes       *recipes.Library
	RecipeEnv     recipes.Env
	Store         *history.Store
	Sampler       *sampler.Sampler
	Audit         *audit.Log
	FixtureEngine *engine.Engine
	UserRecipeDir string
	Version       string
	Narrative     func() []history.Series
	Warnings      []string
}

// Server serves /v1 (token-authenticated) and /admin (host socket).
type Server struct {
	Clients *Clients
	backend atomic.Pointer[Backend]
	sem     chan struct{}
	started time.Time
}

// New returns a server.
func New(b *Backend, c *Clients) *Server {
	s := &Server{Clients: c, sem: make(chan struct{}, 4), started: time.Now()}
	s.backend.Store(b)
	return s
}

// SetBackend swaps the backend (hot reload).
func (s *Server) SetBackend(b *Backend) { s.backend.Store(b) }

// B returns the current backend.
func (s *Server) B() *Backend { return s.backend.Load() }

// ---- helpers -----------------------------------------------------------------------

type apiError struct {
	Error   string `json:"error"`
	Detail  string `json:"detail,omitempty"`
	Pairing any    `json:"pairing,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string, detail ...string) {
	e := apiError{Error: msg}
	if len(detail) > 0 {
		e.Detail = detail[0]
	}
	writeJSON(w, code, e)
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

type ctxKey struct{}

func clientOf(r *http.Request) *Client {
	c, _ := r.Context().Value(ctxKey{}).(*Client)
	return c
}

// ---- routing -----------------------------------------------------------------------

// Handler returns the /v1 API handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/recipes", s.listRecipes)
	mux.HandleFunc("GET /v1/recipes/{name}", s.describe)
	mux.HandleFunc("POST /v1/run/{name}", s.runRecipe)
	mux.HandleFunc("POST /v1/run", s.runScript)
	mux.HandleFunc("POST /v1/explain", s.explain)
	mux.HandleFunc("GET /v1/history/{name}", s.history)
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("GET /v1/narrative", s.narrative)
	mux.HandleFunc("GET /v1/consumers_at/{name}", s.consumersAt)
	mux.HandleFunc("GET /v1/attention", s.attention)
	mux.HandleFunc("GET /v1/subscribe/{name}", s.subscribe)
	mux.HandleFunc("POST /v1/alerts", s.alerts)
	mux.HandleFunc("POST /v1/propose", s.propose)
	return s.auth(mux)
}

// auth enforces tokens, origins and first-contact pairing.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		origin := r.Header.Get("Origin")
		// Pairing status needs no token: the pending id is the secret.
		if strings.HasPrefix(r.URL.Path, "/v1/pair/") && r.Method == http.MethodGet {
			s.collect(w, r)
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token") // EventSource cannot send headers
		}
		if r.Method == http.MethodOptions {
			s.preflight(w, r)
			return
		}
		cl, ok := s.Clients.Authenticate(tok)
		if !ok {
			if !s.Clients.API().Enabled {
				fail(w, http.StatusServiceUnavailable, "api_disabled")
				return
			}
			name := r.Header.Get("X-Sysq-Client")
			if name == "" {
				name = r.Header.Get("User-Agent")
			}
			if name == "" {
				name = "unknown client"
			}
			if len(name) > 80 {
				name = name[:80]
			}
			p := s.Clients.RequestPairing(name, origin)
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			writeJSON(w, http.StatusUnauthorized, apiError{
				Error:  "pairing_required",
				Detail: "Approve this client in the host app (Headroom → Settings → API), then GET the poll URL once to collect your token.",
				Pairing: map[string]string{
					"id": p.ID, "code": p.Code, "poll": "/v1/pair/" + p.ID, "name": p.Name,
				},
			})
			return
		}
		if !OriginAllowed(cl, origin) {
			fail(w, http.StatusForbidden, "origin_not_allowed", origin)
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, cl)))
	})
}

func (s *Server) preflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	allowed := false
	for _, c := range s.Clients.List() {
		if OriginAllowed(&c, origin) && origin != "" {
			allowed = true
		}
	}
	if !allowed {
		fail(w, http.StatusForbidden, "origin_not_allowed", origin)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Sysq-Client")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) collect(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/pair/")
	tok, approved, err := s.Clients.Collect(id)
	switch {
	case errors.Is(err, ErrNotFound):
		fail(w, http.StatusNotFound, "unknown_or_expired_pairing")
	case err != nil:
		fail(w, http.StatusGone, err.Error())
	case !approved:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "token": tok})
	}
}

func (s *Server) opts(cl *Client) engine.CallOpts {
	return engine.CallOpts{MaxSensitivity: cl.MaxSensitivity(), Ledger: &engine.Ledger{Budget: s.B().Engine.Policy.CostBudget}}
}

func (s *Server) acquire(ctx context.Context) (func(), error) {
	select {
	case s.sem <- struct{}{}:
		return func() { <-s.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("busy")
	}
}

func (s *Server) audit(cl *Client, kind, name, src string, params map[string]any, l *engine.Ledger, start time.Time, items []script.Item, err error) {
	b := s.B()
	_, _, prims := l.Snapshot()
	rec := audit.Record{Component: "sysq", Caller: "http:" + cl.ID, Kind: kind, Name: name, Script: src, Params: params,
		Primitives: prims, DurationMS: time.Since(start).Milliseconds()}
	for _, it := range items {
		rec.Rows += it.Table.Len()
		data, _ := json.Marshal(it.Table)
		rec.Bytes += len(data)
		rec.Sensitivity = table.MaxSensitivity(rec.Sensitivity, it.Table.Meta.Sensitivity)
		rec.Truncated = rec.Truncated || it.Table.Meta.Truncated
	}
	if err != nil {
		rec.Error = err.Error()
	}
	if b.Audit != nil {
		_ = b.Audit.Write(rec)
	}
}

var absPath = regexp.MustCompile(`(?:/[^/\s:'"]+)+/([^/\s:'"]+)`)

// filterPrivacy applies the client's privacy level beyond sensitivity: at
// "consumers" level, paths (in path columns and inside text such as command
// lines) are reduced to their last element.
func filterPrivacy(cl *Client, items []script.Item) []script.Item {
	if cl.Privacy != "consumers" {
		return items
	}
	out := make([]script.Item, len(items))
	for i, it := range items {
		t := it.Table.Clone()
		t.Rows = make([][]any, len(it.Table.Rows))
		for r, row := range it.Table.Rows {
			nr := append([]any(nil), row...)
			for c, col := range t.Schema {
				p, ok := nr[c].(string)
				if !ok || p == "" {
					continue
				}
				switch col.Type {
				case table.Path:
					nr[c] = "…/" + p[strings.LastIndexByte(p, '/')+1:]
				case table.Str:
					nr[c] = absPath.ReplaceAllString(p, "…/$1")
				}
			}
			t.Rows[r] = nr
		}
		out[i] = script.Item{Label: it.Label, Table: t}
	}
	return out
}

type runResponse struct {
	Items []script.Item `json:"items"`
	Meta  struct {
		Cost       int      `json:"cost_units"`
		Calls      int      `json:"calls"`
		Primitives []string `json:"primitives"`
		DurationMS int64    `json:"duration_ms"`
	} `json:"meta"`
	Error   string `json:"error,omitempty"`
	Lowered string `json:"lowered,omitempty"`
}

func respond(w http.ResponseWriter, items []script.Item, l *engine.Ledger, start time.Time, err error, lowered string) {
	var resp runResponse
	resp.Items = items
	if resp.Items == nil {
		resp.Items = []script.Item{}
	}
	resp.Meta.Cost, resp.Meta.Calls, resp.Meta.Primitives = l.Snapshot()
	resp.Meta.DurationMS = time.Since(start).Milliseconds()
	resp.Lowered = lowered
	code := http.StatusOK
	if err != nil {
		resp.Error = err.Error()
		code = http.StatusUnprocessableEntity
		if errors.Is(err, engine.ErrSensitivity) || strings.Contains(err.Error(), "sensitivity") {
			code = http.StatusForbidden
		}
		if errors.Is(err, argv.ErrInvalid) || errors.Is(err, engine.ErrUnknown) {
			code = http.StatusBadRequest
		}
	}
	writeJSON(w, code, resp)
}

// ---- handlers ----------------------------------------------------------------------

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	b := s.B()
	cl := clientOf(r)
	view, batt := false, false
	if b.Sampler != nil {
		view, batt = b.Sampler.State()
	}
	nrec := 0
	if b.Recipes != nil {
		nrec = len(b.Recipes.Names())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":             b.Version,
		"catalog":             map[string]any{"primitives": len(b.Engine.Catalog.Names()), "version": catalogVersion(b)},
		"recipes":             nrec,
		"enabled_sensitivity": b.Engine.Policy.EnabledSensitivity,
		"network_primitives":  b.Engine.Policy.NetworkPrimitives,
		"sudo":                b.Engine.SudoStatus(r.Context()),
		"history":             b.Store != nil,
		"fixtures":            b.Engine.Fixtures != nil,
		"view_open":           view,
		"on_battery":          batt,
		"uptime_seconds":      int(time.Since(s.started).Seconds()),
		"client":              map[string]any{"id": cl.ID, "privacy": cl.Privacy, "max_sensitivity": cl.MaxSensitivity(), "allow_scripts": cl.AllowScripts},
		"warnings":            b.Warnings,
	})
}

func catalogVersion(b *Backend) string {
	h := 0
	for _, n := range b.Engine.Catalog.Names() {
		for _, c := range n {
			h = h*31 + int(c)
		}
	}
	if b.Recipes != nil {
		for _, n := range b.Recipes.Names() {
			for _, c := range n {
				h = h*31 + int(c)
			}
		}
	}
	return fmt.Sprintf("%08x", uint32(h))
}

type entry struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Doc         string `json:"doc"`
	Cost        string `json:"cost"`
	Sensitivity string `json:"sensitivity"`
	Available   bool   `json:"available"`
	Exposed     bool   `json:"exposed"`
	Reason      string `json:"reason,omitempty"`
}

func (s *Server) listRecipes(w http.ResponseWriter, r *http.Request) {
	b := s.B()
	cl := clientOf(r)
	var out []entry
	max := table.SensitivityRank(cl.MaxSensitivity())
	for _, p := range b.Engine.Catalog.All() {
		e := entry{Name: p.Name, Kind: "primitive", Doc: p.Doc, Cost: p.Cost, Sensitivity: p.Sensitivity, Exposed: s.Clients.Exposed(cl, p.Name)}
		e.Available = table.SensitivityRank(p.Sensitivity) <= max && b.Engine.Policy.SensitivityEnabled(p.Sensitivity)
		if !e.Available {
			e.Reason = "sensitivity " + p.Sensitivity + " is above this client's level"
		}
		out = append(out, e)
	}
	if b.Recipes != nil {
		for _, rc := range b.Recipes.All() {
			e := entry{Name: rc.Name, Kind: "recipe", Doc: rc.Doc, Cost: rc.Cost, Sensitivity: rc.Sensitivity, Exposed: s.Clients.Exposed(cl, rc.Name)}
			e.Available = table.SensitivityRank(rc.Sensitivity) <= max && rc.Unavailable == ""
			if rc.Unavailable != "" {
				e.Reason = rc.Unavailable
			} else if !e.Available {
				e.Reason = "sensitivity " + rc.Sensitivity + " is above this client's level"
			}
			out = append(out, e)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) describe(w http.ResponseWriter, r *http.Request) {
	b := s.B()
	name := r.PathValue("name")
	if b.Recipes != nil {
		if rc, ok := b.Recipes.Get(name); ok {
			v := rc.Describe()
			if sch, err := b.Recipes.Schema(b.FixtureEngine, name); err == nil {
				v["output"] = sch
			}
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	p, ok := b.Engine.Catalog.Get(name)
	if !ok {
		fail(w, http.StatusNotFound, "unknown", name)
		return
	}
	params := map[string]any{}
	for k, sp := range p.Params {
		params[k] = sp
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": p.Name, "kind": "primitive", "doc": p.Doc, "cost": p.Cost,
		"sensitivity": p.Sensitivity, "schema": p.Schema, "params": params, "param_order": p.ParamOrder})
}

// RunName runs a recipe or primitive (shared by the API and the sampler).
func RunName(ctx context.Context, b *Backend, name string, params map[string]any, opts engine.CallOpts) ([]script.Item, error) {
	if b.Recipes != nil {
		if _, ok := b.Recipes.Get(name); ok {
			return b.Recipes.Run(ctx, b.RecipeEnv, name, params, opts)
		}
	}
	t, err := b.Engine.Primitive(ctx, name, params, opts)
	if err != nil {
		return nil, err
	}
	return []script.Item{{Table: t}}, nil
}

func (s *Server) runRecipe(w http.ResponseWriter, r *http.Request) {
	cl := clientOf(r)
	name := r.PathValue("name")
	if !s.Clients.Exposed(cl, name) {
		fail(w, http.StatusForbidden, "not_exposed", name+" is not exposed to this client")
		return
	}
	var body map[string]any
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if p, ok := body["params"].(map[string]any); ok && len(body) == 1 {
		body = p
	}
	release, err := s.acquire(r.Context())
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "busy")
		return
	}
	defer release()
	start := time.Now()
	opts := s.opts(cl)
	items, err := RunName(r.Context(), s.B(), name, body, opts)
	items = filterPrivacy(cl, items)
	s.audit(cl, "recipe", name, "", body, opts.Ledger, start, items, err)
	respond(w, items, opts.Ledger, start, err, "")
}

func (s *Server) runScript(w http.ResponseWriter, r *http.Request) {
	cl := clientOf(r)
	if !cl.AllowScripts {
		fail(w, http.StatusForbidden, "scripts_disabled", "this client may run named recipes only (allow_scripts is off)")
		return
	}
	var body struct {
		Script string `json:"script"`
		Budget int    `json:"budget"`
		Steps  uint64 `json:"steps"`
	}
	if err := decode(r, &body); err != nil || strings.TrimSpace(body.Script) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "body: {\"script\": \"...\"}")
		return
	}
	release, err := s.acquire(r.Context())
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "busy")
		return
	}
	defer release()
	b := s.B()
	start := time.Now()
	opts := s.opts(cl)
	if body.Budget > 0 && body.Budget < opts.Ledger.Budget {
		opts.Ledger.Budget = body.Budget
	}
	so := b.Recipes.Options(b.RecipeEnv, opts)
	if body.Steps > 0 && body.Steps < b.Engine.Policy.StepBudget {
		so.Steps = body.Steps
	}
	res, err := script.Run(r.Context(), body.Script, so)
	var items []script.Item
	lowered := ""
	if res != nil {
		items, lowered = res.Items, res.Lowered
	}
	items = filterPrivacy(cl, items)
	s.audit(cl, "script", "", body.Script, nil, opts.Ledger, start, items, err)
	respond(w, items, opts.Ledger, start, err, lowered)
}

type resolver struct{ b *Backend }

func (r resolver) Primitive(n string) (*catalog.Primitive, bool) { return r.b.Engine.Catalog.Get(n) }
func (r resolver) Recipe(n string) (plan.RecipeFacts, bool) {
	if r.b.Recipes == nil {
		return plan.RecipeFacts{}, false
	}
	x, ok := r.b.Recipes.Get(n)
	if !ok {
		return plan.RecipeFacts{}, false
	}
	return plan.RecipeFacts{Cost: x.Cost, Sensitivity: x.Sensitivity, Primitives: x.Primitives, Units: x.Units, Network: x.Network}, true
}

func (s *Server) explain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Script string `json:"script"`
		Recipe string `json:"recipe"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	b := s.B()
	plan.SetNamespaces(b.Engine.Catalog.Namespaces())
	if body.Recipe != "" {
		rc, ok := b.Recipes.Get(body.Recipe)
		if !ok {
			fail(w, http.StatusNotFound, "unknown", body.Recipe)
			return
		}
		p, err := plan.Analyze(rc.Lowered, resolver{b})
		if err != nil {
			fail(w, http.StatusUnprocessableEntity, "explain_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)
		return
	}
	p, err := plan.Explain(body.Script, resolver{b})
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "explain_failed", err.Error())
		return
	}
	cl := clientOf(r)
	if table.SensitivityRank(p.Sensitivity) > table.SensitivityRank(cl.MaxSensitivity()) {
		p.Unknown = append(p.Unknown, "sensitivity "+p.Sensitivity+" exceeds this client's level; running it would fail")
	}
	writeJSON(w, http.StatusOK, p)
}

func rangeParam(r *http.Request, def time.Duration) time.Duration {
	if v := r.URL.Query().Get("range"); v != "" {
		if sec, err := argv.ParseDuration(v); err == nil && sec > 0 {
			return time.Duration(sec * float64(time.Second))
		}
	}
	return def
}

func (s *Server) needStore(w http.ResponseWriter) *history.Store {
	b := s.B()
	if b.Store == nil {
		fail(w, http.StatusServiceUnavailable, "no_history", "history is recorded by sysqd's sampler")
		return nil
	}
	return b.Store
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	st := s.needStore(w)
	if st == nil {
		return
	}
	cl := clientOf(r)
	name := r.PathValue("name")
	key := r.URL.Query().Get("key")
	t, err := st.Query(r.Context(), name, r.URL.Query().Get("column"), rangeParam(r, time.Hour), key)
	if err != nil {
		fail(w, http.StatusInternalServerError, "history_failed", err.Error())
		return
	}
	if table.SensitivityRank(t.Meta.Sensitivity) > table.SensitivityRank(cl.MaxSensitivity()) {
		fail(w, http.StatusForbidden, "sensitivity", "this series is "+t.Meta.Sensitivity)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": []script.Item{{Label: name, Table: t}}})
}

func (s *Server) personal(w http.ResponseWriter, cl *Client) bool {
	if table.SensitivityRank(cl.MaxSensitivity()) < table.SensitivityRank("personal") {
		fail(w, http.StatusForbidden, "sensitivity", "needs the consumers privacy level")
		return false
	}
	return true
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	st := s.needStore(w)
	if st == nil || !s.personal(w, clientOf(r)) {
		return
	}
	t, err := st.Events(r.Context(), rangeParam(r, 2*time.Hour), r.URL.Query().Get("kind"))
	if err != nil {
		fail(w, http.StatusInternalServerError, "events_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": []script.Item{{Label: "events", Table: t}}})
}

func (s *Server) narrative(w http.ResponseWriter, r *http.Request) {
	st := s.needStore(w)
	if st == nil || !s.personal(w, clientOf(r)) {
		return
	}
	b := s.B()
	var series []history.Series
	if b.Narrative != nil {
		series = b.Narrative()
	}
	t, err := st.Narrative(r.Context(), rangeParam(r, 2*time.Hour), series, b.Engine.Loc)
	if err != nil {
		fail(w, http.StatusInternalServerError, "narrative_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": history.Text(t), "items": []script.Item{{Label: "narrative", Table: t}}})
}

func (s *Server) consumersAt(w http.ResponseWriter, r *http.Request) {
	st := s.needStore(w)
	if st == nil || !s.personal(w, clientOf(r)) {
		return
	}
	at, err := history.ParseAt(r.URL.Query().Get("at"), time.Now(), s.B().Engine.Loc)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_at", err.Error())
		return
	}
	t, err := st.TableAt(r.Context(), r.PathValue("name"), at, 2*time.Minute)
	if err != nil {
		fail(w, http.StatusInternalServerError, "history_failed", err.Error())
		return
	}
	items := filterPrivacy(clientOf(r), []script.Item{{Label: r.PathValue("name"), Table: t}})
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) attention(w http.ResponseWriter, r *http.Request) {
	cl := clientOf(r)
	b := s.B()
	release, err := s.acquire(r.Context())
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "busy")
		return
	}
	defer release()
	opts := s.opts(cl)
	opts.Ledger.Budget *= 4
	t, err := b.Recipes.RunAttention(r.Context(), b.RecipeEnv, r.URL.Query().Get("min"), opts)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "attention_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": filterPrivacy(cl, []script.Item{{Label: "attention", Table: t}})})
}

func (s *Server) propose(w http.ResponseWriter, r *http.Request) {
	cl := clientOf(r)
	if !cl.AllowPropose && !cl.AllowScripts {
		fail(w, http.StatusForbidden, "propose_disabled")
		return
	}
	var raw map[string]any
	if err := decode(r, &raw); err != nil {
		fail(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	p := recipes.Proposal{Extra: map[string]any{}}
	for k, v := range raw {
		switch k {
		case "name":
			p.Name, _ = v.(string)
		case "doc":
			p.Doc, _ = v.(string)
		case "script":
			p.Script, _ = v.(string)
		case "example":
			p.Example, _ = v.(string)
		case "tags":
			if l, ok := v.([]any); ok {
				for _, t := range l {
					if s, ok := t.(string); ok {
						p.Tags = append(p.Tags, s)
					}
				}
			}
		default:
			p.Extra[k] = v
		}
	}
	b := s.B()
	file, err := b.Recipes.Propose(b.UserRecipeDir, p)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "proposal_refused", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "proposed", "file": file, "review": "sysq recipe review " + p.Name})
}

// ---- SSE ---------------------------------------------------------------------------

type sse struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sse, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sse{w, f}, true
}

func (e *sse) send(event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	e.f.Flush()
	return nil
}

func (e *sse) ping() error {
	if _, err := fmt.Fprint(e.w, ": ping\n\n"); err != nil {
		return err
	}
	e.f.Flush()
	return nil
}

// checkSubscribable verifies exposure and sensitivity before streaming.
func (s *Server) checkSubscribable(cl *Client, name string) error {
	b := s.B()
	if !s.Clients.Exposed(cl, name) {
		return fmt.Errorf("%s is not exposed to this client", name)
	}
	sens := ""
	if b.Recipes != nil {
		if rc, ok := b.Recipes.Get(name); ok {
			sens = rc.Sensitivity
		}
	}
	if sens == "" {
		p, ok := b.Engine.Catalog.Get(name)
		if !ok {
			return fmt.Errorf("unknown %s", name)
		}
		sens = p.Sensitivity
	}
	if table.SensitivityRank(sens) > table.SensitivityRank(cl.MaxSensitivity()) {
		return fmt.Errorf("%s is %s; this client's level is %s", name, sens, cl.MaxSensitivity())
	}
	return nil
}

func everyParam(r *http.Request) time.Duration {
	every := 5 * time.Second
	if v := r.URL.Query().Get("every"); v != "" {
		if sec, err := argv.ParseDuration(v); err == nil && sec >= 1 {
			every = time.Duration(sec * float64(time.Second))
		}
	}
	return every
}

func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	cl := clientOf(r)
	name := r.PathValue("name")
	if err := s.checkSubscribable(cl, name); err != nil {
		fail(w, http.StatusForbidden, "not_subscribable", err.Error())
		return
	}
	var params map[string]any
	if p := r.URL.Query().Get("params"); p != "" {
		if err := json.Unmarshal([]byte(p), &params); err != nil {
			fail(w, http.StatusBadRequest, "bad_params", err.Error())
			return
		}
	}
	b := s.B()
	if b.Sampler == nil {
		fail(w, http.StatusServiceUnavailable, "no_sampler")
		return
	}
	stream, ok := newSSE(w)
	if !ok {
		fail(w, http.StatusInternalServerError, "no_streaming")
		return
	}
	ch, cancel := b.Sampler.Subscribe(name, params, everyParam(r))
	defer cancel()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if stream.ping() != nil {
				return
			}
		case up := <-ch:
			up.Items = filterPrivacy(cl, up.Items)
			if stream.send("update", up) != nil {
				return
			}
		}
	}
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	cl := clientOf(r)
	var body struct {
		Recipe string         `json:"recipe"`
		Params map[string]any `json:"params"`
		When   string         `json:"when"`
		Every  string         `json:"every"`
	}
	if err := decode(r, &body); err != nil || body.Recipe == "" || body.When == "" {
		fail(w, http.StatusBadRequest, "bad_request", `body: {"recipe": "...", "when": "pressure_pct > 80", "every": "10s"}`)
		return
	}
	pred, err := expr.Parse(body.When)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_when", err.Error())
		return
	}
	if err := s.checkSubscribable(cl, body.Recipe); err != nil {
		fail(w, http.StatusForbidden, "not_subscribable", err.Error())
		return
	}
	every := 10 * time.Second
	if sec, err := argv.ParseDuration(body.Every); err == nil && sec >= 1 {
		every = time.Duration(sec * float64(time.Second))
	}
	b := s.B()
	if b.Sampler == nil {
		fail(w, http.StatusServiceUnavailable, "no_sampler")
		return
	}
	stream, ok := newSSE(w)
	if !ok {
		return
	}
	ch, cancel := b.Sampler.Subscribe(body.Recipe, body.Params, every)
	defer cancel()
	firing := false
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if stream.ping() != nil {
				return
			}
		case up := <-ch:
			if up.Error != "" || len(up.Items) == 0 {
				continue
			}
			t := up.Items[0].Table
			matched, err := expr.Filter(t, pred, &expr.Env{Now: time.Now(), Home: b.Engine.Home})
			if err != nil {
				_ = stream.send("error", map[string]string{"error": err.Error()})
				return
			}
			now := matched.Len() > 0
			if now != firing {
				firing = now
				kind := "cleared"
				if now {
					kind = "alert"
				}
				items := filterPrivacy(cl, []script.Item{{Label: body.Recipe, Table: matched}})
				if stream.send(kind, map[string]any{"ts": up.TS, "when": body.When, "items": items}) != nil {
					return
				}
			}
		}
	}
}

// ListenLoopback listens on 127.0.0.1 only (design §26 MUST).
func ListenLoopback(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// Serve runs an HTTP server on a listener until ctx is done.
func Serve(ctx context.Context, l net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	err := srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		log.Printf("sysqd: serve: %v", err)
	}
	return err
}
