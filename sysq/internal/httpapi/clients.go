// Package httpapi is sysqd's loopback HTTP layer (design §26): describe,
// run_recipe, run (per client), explain, history, SSE subscriptions and
// alerts, behind per-client bearer tokens issued through host-approved
// pairing. It can only reach sysq, so it is read-only by construction.
package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ainigh/headroom-workspace/sysq/internal/yamlcfg"
)

//go:embed clients.schema.json
var clientsSchemaText string

var clientsSchema = yamlcfg.MustCompile("clients.schema.json", clientsSchemaText)

// Privacy levels a host grants (design §28). "full" is human-only and never
// granted to an API client.
var privacyToSensitivity = map[string]string{"aggregates": "public", "consumers": "personal", "files": "personal"}

// Client is one paired API client.
type Client struct {
	ID           string    `yaml:"id" json:"id"`
	Name         string    `yaml:"name" json:"name"`
	TokenSHA256  string    `yaml:"token_sha256" json:"-"`
	Privacy      string    `yaml:"privacy" json:"privacy"` // aggregates | consumers | files
	AllowScripts bool      `yaml:"allow_scripts" json:"allow_scripts"`
	AllowPropose bool      `yaml:"allow_propose" json:"allow_propose"`
	Origins      []string  `yaml:"origins,omitempty" json:"origins,omitempty"`
	Exposed      []string  `yaml:"exposed,omitempty" json:"exposed,omitempty"` // recipe globs; empty = API default
	Created      time.Time `yaml:"created" json:"created"`
	LastSeen     time.Time `yaml:"last_seen,omitempty" json:"last_seen,omitempty"`
	Host         bool      `yaml:"host,omitempty" json:"host,omitempty"` // created by the host app itself
}

// MaxSensitivity derives the client's sensitivity ceiling from its privacy level.
func (c *Client) MaxSensitivity() string {
	if s, ok := privacyToSensitivity[c.Privacy]; ok {
		return s
	}
	return "public"
}

// API holds global API settings (written by the host from its api.yaml).
type API struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Port    int      `yaml:"port" json:"port"`
	Exposed []string `yaml:"exposed" json:"exposed"` // default exposure for paired clients
}

// File is clients.yaml.
type File struct {
	Version int      `yaml:"version"`
	API     API      `yaml:"api"`
	Clients []Client `yaml:"clients"`
}

// Pending is a first-contact request waiting for host approval.
type Pending struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Origin    string    `json:"origin,omitempty"`
	Requested time.Time `json:"requested"`
	token     string    // issued on approval, handed over once
	approved  bool
	delivered bool
}

// Clients is the client table with persistence.
type Clients struct {
	path    string
	mu      sync.Mutex
	file    File
	pending map[string]*Pending
	now     func() time.Time
}

// DefaultAPI is used when clients.yaml has no api section.
var DefaultAPI = API{Enabled: true, Port: 11435, Exposed: []string{"health.overview", "headroom.check", "*.headline", "*.detail"}}

// LoadClients reads clients.yaml (missing = empty table).
func LoadClients(p string) (*Clients, error) {
	c := &Clients{path: p, pending: map[string]*Pending{}, now: time.Now}
	c.file = File{Version: 1, API: DefaultAPI}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	if err := clientsSchema.Decode(data, &f); err != nil {
		return c, fmt.Errorf("clients.yaml: %w (kept the previous table)", err)
	}
	if f.API.Port == 0 {
		f.API.Port = DefaultAPI.Port
	}
	if f.API.Exposed == nil {
		f.API.Exposed = DefaultAPI.Exposed
	}
	for _, cl := range f.Clients {
		if _, ok := privacyToSensitivity[cl.Privacy]; !ok {
			return c, fmt.Errorf("clients.yaml: client %s has privacy %q (aggregates, consumers, files)", cl.ID, cl.Privacy)
		}
	}
	c.file = f
	return c, nil
}

func (c *Clients) save() error {
	if c.path == "" {
		return nil
	}
	data, err := yaml.Marshal(c.file)
	if err != nil {
		return err
	}
	header := []byte("# sysqd API clients. Tokens are stored as SHA-256 hashes. Managed by the host app's pairing UI.\n")
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(header, data...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "sysq_" + base64.RawURLEncoding.EncodeToString(b)
}

func newCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 9)
	for i, x := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(x)%len(alphabet)])
	}
	return string(out)
}

// Authenticate finds the client for a bearer token.
func (c *Clients) Authenticate(token string) (*Client, bool) {
	if token == "" {
		return nil, false
	}
	h := hashToken(token)
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.file.Clients {
		cl := &c.file.Clients[i]
		if subtle.ConstantTimeCompare([]byte(cl.TokenSHA256), []byte(h)) == 1 {
			cl.LastSeen = c.now()
			cp := *cl
			return &cp, true
		}
	}
	return nil, false
}

// API returns global API settings.
func (c *Clients) API() API {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.file.API
}

// SetAPI replaces global API settings (host only).
func (c *Clients) SetAPI(a API) error {
	if a.Port < 1024 || a.Port > 65535 {
		return fmt.Errorf("port must be 1024–65535")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.file.API = a
	return c.save()
}

// List returns clients (without tokens).
func (c *Clients) List() []Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]Client(nil), c.file.Clients...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// Upsert creates or updates a client and returns a fresh token (host only:
// used for the host's own UI and assistant clients).
func (c *Clients) Upsert(cl Client) (string, error) {
	if !idRe.MatchString(cl.ID) {
		return "", fmt.Errorf("bad client id %q", cl.ID)
	}
	if _, ok := privacyToSensitivity[cl.Privacy]; !ok {
		return "", fmt.Errorf("privacy must be aggregates, consumers or files")
	}
	tok := newToken()
	cl.TokenSHA256 = hashToken(tok)
	c.mu.Lock()
	defer c.mu.Unlock()
	cl.Created = c.now()
	for i := range c.file.Clients {
		if c.file.Clients[i].ID == cl.ID {
			cl.Created = c.file.Clients[i].Created
			c.file.Clients[i] = cl
			return tok, c.save()
		}
	}
	c.file.Clients = append(c.file.Clients, cl)
	return tok, c.save()
}

// Revoke deletes a client.
func (c *Clients) Revoke(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.file.Clients {
		if c.file.Clients[i].ID == id {
			c.file.Clients = append(c.file.Clients[:i], c.file.Clients[i+1:]...)
			return c.save()
		}
	}
	return fmt.Errorf("no client %s", id)
}

// RequestPairing records a first-contact request (or returns the existing
// one for the same name+origin) and returns it.
func (c *Clients) RequestPairing(name, origin string) *Pending {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	for _, p := range c.pending {
		if p.Name == name && p.Origin == origin && !p.approved {
			return p
		}
	}
	if len(c.pending) >= 20 {
		// Drop the oldest: first contact can't be used to exhaust memory.
		var oldest *Pending
		for _, p := range c.pending {
			if oldest == nil || p.Requested.Before(oldest.Requested) {
				oldest = p
			}
		}
		delete(c.pending, oldest.ID)
	}
	p := &Pending{ID: strings.ToLower(strings.ReplaceAll(newCode(), "-", "")), Code: newCode(), Name: name, Origin: origin, Requested: c.now()}
	c.pending[p.ID] = p
	return p
}

func (c *Clients) expire() {
	for id, p := range c.pending {
		if c.now().Sub(p.Requested) > 10*time.Minute {
			delete(c.pending, id)
		}
	}
}

// PendingList returns requests waiting for approval.
func (c *Clients) PendingList() []Pending {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	var out []Pending
	for _, p := range c.pending {
		if !p.approved {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Requested.Before(out[j].Requested) })
	return out
}

// ErrNotFound is returned for unknown pairing ids.
var ErrNotFound = errors.New("not found")

// Approve turns a pending request into a client (host only).
func (c *Clients) Approve(pendingID, code, privacy string, allowScripts bool, origins []string) (*Client, error) {
	if _, ok := privacyToSensitivity[privacy]; !ok {
		return nil, fmt.Errorf("privacy must be aggregates, consumers or files")
	}
	c.mu.Lock()
	p, ok := c.pending[pendingID]
	c.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	if subtle.ConstantTimeCompare([]byte(p.Code), []byte(code)) != 1 {
		return nil, fmt.Errorf("pairing code does not match")
	}
	id := slug(p.Name)
	if p.Origin != "" && len(origins) == 0 {
		origins = []string{p.Origin}
	}
	cl := Client{ID: id, Name: p.Name, Privacy: privacy, AllowScripts: allowScripts, Origins: origins}
	c.mu.Lock()
	for _, existing := range c.file.Clients {
		if existing.ID == cl.ID {
			cl.ID = fmt.Sprintf("%s-%d", id, c.now().Unix()%10000)
		}
	}
	c.mu.Unlock()
	tok, err := c.Upsert(cl)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	p.approved, p.token = true, tok
	c.mu.Unlock()
	return &cl, nil
}

// Collect returns the token for an approved request, once.
func (c *Clients) Collect(pendingID string) (token string, approved bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	p, ok := c.pending[pendingID]
	if !ok {
		return "", false, ErrNotFound
	}
	if !p.approved {
		return "", false, nil
	}
	if p.delivered {
		return "", true, fmt.Errorf("token already collected")
	}
	p.delivered = true
	tok := p.token
	p.token = ""
	delete(c.pending, pendingID)
	return tok, true, nil
}

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "client"
	}
	return out
}

// Exposed reports whether a client may call name via /v1/run/{name}.
func (c *Clients) Exposed(cl *Client, name string) bool {
	globs := cl.Exposed
	if len(globs) == 0 {
		globs = c.API().Exposed
	}
	for _, g := range globs {
		if ok, _ := path.Match(g, name); ok || g == name {
			return true
		}
	}
	return false
}

// OriginAllowed checks a browser Origin against a client's allowlist.
func OriginAllowed(cl *Client, origin string) bool {
	if origin == "" {
		return true
	}
	for _, o := range cl.Origins {
		if o == origin {
			return true
		}
	}
	return false
}

// Replace takes the client table and API settings from another (reload);
// pending pairings are kept.
func (c *Clients) Replace(o *Clients) {
	o.mu.Lock()
	f := o.file
	o.mu.Unlock()
	c.mu.Lock()
	c.file = f
	c.mu.Unlock()
}
