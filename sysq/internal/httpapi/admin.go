package httpapi

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Admin operations are for the host app only. They are served on a unix
// socket under sysq's data directory with 0600 permissions, never on TCP.
type Admin struct {
	S *Server
	// OnState is called when the host reports view/power state.
	OnState func(viewOpen bool, onBattery *bool)
	// SaveSchedules validates and applies schedules.yaml content.
	SaveSchedules func(yaml []byte) error
	// Reload re-reads catalog, recipes, policy and config files.
	Reload func() error
}

// Handler returns the admin handler.
func (a *Admin) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/status", func(w http.ResponseWriter, r *http.Request) {
		b := a.S.B()
		view, batt := false, false
		if b.Sampler != nil {
			view, batt = b.Sampler.State()
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": b.Version, "api": a.S.Clients.API(), "view_open": view, "on_battery": batt,
			"clients": len(a.S.Clients.List()), "pending": len(a.S.Clients.PendingList()), "warnings": b.Warnings,
			"sudo": b.Engine.SudoStatus(r.Context()), "enabled_sensitivity": b.Engine.Policy.EnabledSensitivity,
			"network_primitives": b.Engine.Policy.NetworkPrimitives, "fixtures": b.Engine.Fixtures != nil})
	})
	mux.HandleFunc("GET /admin/clients", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"clients": a.S.Clients.List()})
	})
	mux.HandleFunc("GET /admin/pending", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"pending": a.S.Clients.PendingList()})
	})
	mux.HandleFunc("POST /admin/approve", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID, Code, Privacy string
			AllowScripts      bool     `json:"allow_scripts"`
			Origins           []string `json:"origins"`
		}
		if err := decode(r, &body); err != nil {
			fail(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		cl, err := a.S.Clients.Approve(body.ID, body.Code, body.Privacy, body.AllowScripts, body.Origins)
		if err != nil {
			fail(w, http.StatusBadRequest, "approve_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, cl)
	})
	mux.HandleFunc("POST /admin/clients", func(w http.ResponseWriter, r *http.Request) {
		var cl Client
		if err := decode(r, &cl); err != nil {
			fail(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		cl.Host = true
		tok, err := a.S.Clients.Upsert(cl)
		if err != nil {
			fail(w, http.StatusBadRequest, "upsert_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": cl.ID, "token": tok})
	})
	mux.HandleFunc("DELETE /admin/clients/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.S.Clients.Revoke(r.PathValue("id")); err != nil {
			fail(w, http.StatusNotFound, "revoke_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	})
	mux.HandleFunc("PUT /admin/api", func(w http.ResponseWriter, r *http.Request) {
		var api API
		if err := decode(r, &api); err != nil {
			fail(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		if err := a.S.Clients.SetAPI(api); err != nil {
			fail(w, http.StatusBadRequest, "api_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"api": api, "note": "a port change applies after sysqd restarts"})
	})
	mux.HandleFunc("POST /admin/state", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ViewOpen  bool  `json:"view_open"`
			OnBattery *bool `json:"on_battery"`
		}
		if err := decode(r, &body); err != nil {
			fail(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		if a.OnState != nil {
			a.OnState(body.ViewOpen, body.OnBattery)
		}
		writeJSON(w, http.StatusOK, map[string]any{"view_open": body.ViewOpen})
	})
	mux.HandleFunc("PUT /admin/schedules", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		data := make([]byte, 0, 4096)
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			data = append(data, buf[:n]...)
			if err != nil {
				break
			}
		}
		if a.SaveSchedules == nil {
			fail(w, http.StatusNotImplemented, "no_sampler")
			return
		}
		if err := a.SaveSchedules(data); err != nil {
			fail(w, http.StatusBadRequest, "schedules_invalid", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
	})
	mux.HandleFunc("POST /admin/reload", func(w http.ResponseWriter, r *http.Request) {
		if a.Reload == nil {
			fail(w, http.StatusNotImplemented, "no_reload")
			return
		}
		if err := a.Reload(); err != nil {
			fail(w, http.StatusUnprocessableEntity, "reload_partial", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
	})
	return mux
}

// ListenAdmin opens the admin unix socket with owner-only permissions.
func ListenAdmin(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return nil, &net.OpError{Op: "listen", Net: "unix", Err: os.ErrExist}
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
