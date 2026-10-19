package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newClients(t *testing.T) (*Clients, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sysq", "clients.yaml")
	c, err := LoadClients(p)
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

func TestTokensAreHashedOnDiskAndAuthenticate(t *testing.T) {
	c, p := newClients(t)
	tok, err := c.Upsert(Client{ID: "headroom-ui", Name: "Headroom", Privacy: "files", Host: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "sysq_") {
		t.Errorf("token %q", tok)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), tok) {
		t.Fatal("raw token written to clients.yaml")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("clients.yaml mode %o", st.Mode().Perm())
	}
	cl, ok := c.Authenticate(tok)
	if !ok || cl.ID != "headroom-ui" || cl.MaxSensitivity() != "personal" {
		t.Fatalf("authenticate: %+v %v", cl, ok)
	}
	if cl.LastSeen.IsZero() {
		t.Error("LastSeen not recorded")
	}
	for _, bad := range []string{"", "sysq_nope", tok + "x", strings.ToUpper(tok)} {
		if _, ok := c.Authenticate(bad); ok {
			t.Errorf("%q authenticated", bad)
		}
	}
	// A reload from disk still authenticates (hash persisted) and lists no token.
	c2, err := LoadClients(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c2.Authenticate(tok); !ok {
		t.Error("token lost across reload")
	}
	if list := c2.List(); len(list) != 1 || list[0].TokenSHA256 == "" {
		t.Errorf("list %+v", list)
	}
	// Upsert on the same id rotates the token.
	tok2, _ := c2.Upsert(Client{ID: "headroom-ui", Name: "Headroom", Privacy: "files"})
	if _, ok := c2.Authenticate(tok); ok || tok2 == tok {
		t.Error("old token still valid after re-registration")
	}
	if err := c2.Revoke("headroom-ui"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c2.Authenticate(tok2); ok {
		t.Error("revoked token still valid")
	}
	if err := c2.Revoke("headroom-ui"); err == nil {
		t.Error("revoking twice should fail")
	}
}

func TestUpsertValidation(t *testing.T) {
	c, _ := newClients(t)
	if _, err := c.Upsert(Client{ID: "Bad ID", Privacy: "files"}); err == nil {
		t.Error("bad id accepted")
	}
	if _, err := c.Upsert(Client{ID: "ok", Privacy: "full"}); err == nil {
		t.Error("privacy 'full' must never be granted to an API client")
	}
	if err := c.SetAPI(API{Enabled: true, Port: 80}); err == nil {
		t.Error("privileged port accepted")
	}
}

func TestPairingFlow(t *testing.T) {
	c, _ := newClients(t)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	p := c.RequestPairing("My Dashboard", "http://localhost:3000")
	if p2 := c.RequestPairing("My Dashboard", "http://localhost:3000"); p2.ID != p.ID {
		t.Error("repeated first contact should return the same pending request")
	}
	if l := c.PendingList(); len(l) != 1 || l[0].Code != p.Code {
		t.Errorf("pending %+v", l)
	}
	if tok, approved, err := c.Collect(p.ID); err != nil || approved || tok != "" {
		t.Errorf("collect before approval: %q %v %v", tok, approved, err)
	}
	if _, err := c.Approve(p.ID, "WRONG-CODE", "aggregates", false, nil); err == nil {
		t.Fatal("wrong pairing code approved")
	}
	if _, err := c.Approve("nope", p.Code, "aggregates", false, nil); err != ErrNotFound {
		t.Errorf("unknown pending: %v", err)
	}
	cl, err := c.Approve(p.ID, p.Code, "consumers", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cl.ID != "my-dashboard" || cl.Origins[0] != "http://localhost:3000" || cl.MaxSensitivity() != "personal" {
		t.Errorf("approved client %+v", cl)
	}
	if len(c.PendingList()) != 0 {
		t.Error("approved request still listed as pending")
	}
	tok, approved, err := c.Collect(p.ID)
	if err != nil || !approved || tok == "" {
		t.Fatalf("collect: %q %v %v", tok, approved, err)
	}
	if _, _, err := c.Collect(p.ID); err == nil {
		t.Error("token collected twice")
	}
	got, ok := c.Authenticate(tok)
	if !ok || got.ID != "my-dashboard" {
		t.Fatal("collected token does not authenticate")
	}
	if !c.Exposed(got, "health.overview") || !c.Exposed(got, "mem.headline") || c.Exposed(got, "fs.largest") {
		t.Error("default exposure globs")
	}

	// A second client with the same name gets a distinct id.
	q := c.RequestPairing("My Dashboard", "")
	cl2, err := c.Approve(q.ID, q.Code, "aggregates", false, nil)
	if err != nil || cl2.ID == cl.ID {
		t.Errorf("duplicate name: %+v %v", cl2, err)
	}

	// Unanswered requests expire after ten minutes.
	stale := c.RequestPairing("Old", "")
	now = now.Add(11 * time.Minute)
	if _, _, err := c.Collect(stale.ID); err != ErrNotFound {
		t.Errorf("stale request should be gone, got %v", err)
	}
}

func TestPendingRequestsAreBounded(t *testing.T) {
	c, _ := newClients(t)
	for i := range 50 {
		c.RequestPairing("spam", strings.Repeat("x", i))
	}
	if n := len(c.PendingList()); n > 20 {
		t.Errorf("%d pending requests kept", n)
	}
}

func TestLoadClientsKeepsEmptyTableOnInvalidFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "clients.yaml")
	os.WriteFile(p, []byte("version: 1\nclients:\n  - {id: x, name: x, privacy: full, token_sha256: ''}\n"), 0o600)
	c, err := LoadClients(p)
	if err == nil {
		t.Fatal("privacy full accepted from disk")
	}
	if c == nil || len(c.List()) != 0 {
		t.Error("invalid file must yield a usable empty table")
	}
	if c.API().Port != DefaultAPI.Port {
		t.Error("default API settings missing")
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{"My Dashboard": "my-dashboard", "  --Grafana!!": "grafana", "": "client", "日本": "client", "a--b": "a-b"}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
