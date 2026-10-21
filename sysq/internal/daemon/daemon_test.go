package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type env struct {
	t     *testing.T
	d     *Daemon
	base  string
	admin *http.Client
}

func start(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	d, err := Start(ctx, Options{Dir: dir, Fixtures: "builtin", Port: freePort(t), NoSampler: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); d.Wait() })
	sock := filepath.Join(dir, "admin.sock")
	admin := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	return &env{t: t, d: d, base: "http://" + d.Addr(), admin: admin}
}

func (e *env) do(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.base+path, r)
	req.Header.Set("X-Sysq-Client", "test client")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) adminDo(method, path string, body any) map[string]any {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://admin"+path, r)
	resp, err := e.admin.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		e.t.Fatalf("admin %s %s: %d %v", method, path, resp.StatusCode, out)
	}
	return out
}

// pair walks first contact → host approval → token collection.
func (e *env) pair(privacy string, scripts bool) string {
	code, body := e.do("GET", "/v1/status", "", nil)
	if code != 401 || body["error"] != "pairing_required" {
		e.t.Fatalf("first contact: %d %v", code, body)
	}
	p := body["pairing"].(map[string]any)
	if code, b := e.do("GET", "/v1/pair/"+p["id"].(string), "", nil); code != 202 {
		e.t.Fatalf("pending poll: %d %v", code, b)
	}
	e.adminDo("POST", "/admin/approve", map[string]any{"id": p["id"], "code": p["code"], "privacy": privacy, "allow_scripts": scripts})
	code, b := e.do("GET", "/v1/pair/"+p["id"].(string), "", nil)
	if code != 200 || b["token"] == nil {
		e.t.Fatalf("collect: %d %v", code, b)
	}
	if code, _ := e.do("GET", "/v1/pair/"+p["id"].(string), "", nil); code == 200 {
		e.t.Fatal("token handed out twice")
	}
	return b["token"].(string)
}

// M3 done-when: a client can read history and subscribe.
func TestHistoryAndSubscribe(t *testing.T) {
	e := start(t)
	tok := e.pair("consumers", false)
	// Record a few samples (fixture clock is fixed, so step it).
	for i := 0; i < 3; i++ {
		e.d.Sampler.SampleNow(context.Background(), "mem.headline")
		e.d.Sampler.SampleNow(context.Background(), "proc.top.cpu")
		time.Sleep(10 * time.Millisecond)
	}
	code, body := e.do("GET", "/v1/history/mem.headline?range=1h", tok, nil)
	if code != 200 {
		t.Fatalf("history: %d %v", code, body)
	}
	tbl := body["items"].([]any)[0].(map[string]any)["table"].(map[string]any)
	if len(tbl["rows"].([]any)) == 0 {
		t.Fatal("no history rows")
	}
	if cols := tbl["schema"].([]any); cols[0].(map[string]any)["name"] != "t" {
		t.Fatalf("schema %v", cols)
	}
	// SSE subscription
	req, _ := http.NewRequest("GET", e.base+"/v1/subscribe/mem.headline?every=1s&token="+tok, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	go func() {
		for i := 0; i < 20 && ctx.Err() == nil; i++ {
			e.d.Sampler.Tick(context.Background(), time.Now().Add(time.Duration(i)*time.Hour))
			time.Sleep(200 * time.Millisecond)
		}
	}()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	got := false
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") && strings.Contains(sc.Text(), "available") {
			got = true
			break
		}
	}
	if !got {
		t.Fatal("no SSE update")
	}
	code, body = e.do("GET", "/v1/narrative?range=2h", tok, nil)
	if code != 200 || body["text"] == "" {
		t.Fatalf("narrative %d %v", code, body)
	}
}

func TestClientLimits(t *testing.T) {
	e := start(t)
	tok := e.pair("aggregates", false)
	if code, _ := e.do("POST", "/v1/run/proc.top", tok, nil); code != 403 {
		t.Fatalf("unexposed primitive ran: %d", code)
	}
	if code, b := e.do("POST", "/v1/run/mem.headline", tok, nil); code != 200 {
		t.Fatalf("exposed headline failed: %d %v", code, b)
	}
	if code, _ := e.do("POST", "/v1/run", tok, map[string]any{"script": "emit sys.memory"}); code != 403 {
		t.Fatal("script ran without allow_scripts")
	}
	host := e.adminDo("POST", "/admin/clients", map[string]any{"id": "agg", "name": "agg", "privacy": "aggregates", "exposed": []string{"*"}})["token"].(string)
	if code, _ := e.do("POST", "/v1/run/proc.top", host, nil); code != 403 {
		t.Fatal("personal primitive ran for an aggregates client")
	}
	// A browser origin not on the client's list is refused.
	req, _ := http.NewRequest("GET", e.base+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", "https://evil.example")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if code, _ := e.do("GET", "/v1/status", "sysq_wrong", nil); code != 401 {
		t.Fatal("bad token accepted")
	}
}

func TestHostClientsAndScripts(t *testing.T) {
	e := start(t)
	out := e.adminDo("POST", "/admin/clients", map[string]any{"id": "headroom-assistant", "name": "Headroom assistant", "privacy": "consumers", "allow_scripts": true, "exposed": []string{"*"}})
	tok := out["token"].(string)
	code, body := e.do("POST", "/v1/run", tok, map[string]any{"script": `proc.top(by: "cpu") | filter cpu > 5 | limit 3`})
	if code != 200 {
		t.Fatalf("script: %d %v", code, body)
	}
	if !strings.Contains(body["lowered"].(string), ".filter(expr(") {
		t.Fatal("lowered form not returned")
	}
	code, body = e.do("POST", "/v1/explain", tok, map[string]any{"script": `log.system | filter process == "kernel"`})
	if code != 200 || len(body["pushdown"].([]any)) == 0 {
		t.Fatalf("explain: %d %v", code, body)
	}
	// consumers privacy hides full paths
	code, body = e.do("POST", "/v1/run/proc.list", tok, nil)
	if code != 200 || strings.Contains(mustJSON(body), "/Applications/Google Chrome.app") {
		t.Fatalf("paths leaked at consumers level: %d", code)
	}
	e.adminDo("DELETE", "/admin/clients/headroom-assistant", nil)
	if code, _ := e.do("GET", "/v1/status", tok, nil); code != 401 {
		t.Fatal("revoked token still works")
	}
}

func TestBindsLoopbackOnly(t *testing.T) {
	e := start(t)
	host, _, _ := net.SplitHostPort(e.d.Addr())
	if host != "127.0.0.1" {
		t.Fatalf("listening on %s", host)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The admin status names each sudo primitive's exact command, so the host
// app can show the sudoers line to add; recheck=1 is accepted.
func TestAdminStatusSudoRules(t *testing.T) {
	e := start(t)
	out := e.adminDo("GET", "/admin/status?recheck=1", nil)
	rules, ok := out["sudo_rules"].(map[string]any)
	if !ok || len(rules) == 0 {
		t.Fatalf("no sudo_rules: %v", out["sudo_rules"])
	}
	sudo, _ := out["sudo"].(map[string]any)
	for name, cmd := range rules {
		if _, ok := sudo[name]; !ok {
			t.Errorf("%s has a rule but no status", name)
		}
		if !strings.HasPrefix(cmd.(string), "/") {
			t.Errorf("%s: rule %q isn't an absolute command", name, cmd)
		}
	}
}
