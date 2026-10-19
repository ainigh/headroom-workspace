package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ainigh/headroom-workspace/sysact/internal/confirm"
)

func TestSocketProtocol(t *testing.T) {
	s, ran := testServer(t, false)
	sock := filepath.Join(t.TempDir(), "s.sock")
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx, l)
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rd := bufio.NewScanner(c)
	call := func(id int, method string, params any) map[string]any {
		p, _ := json.Marshal(params)
		req, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": json.RawMessage(p)})
		c.Write(append(req, '\n'))
		if !rd.Scan() {
			t.Fatal("no response")
		}
		var resp map[string]any
		json.Unmarshal(rd.Bytes(), &resp)
		return resp
	}
	if r := call(1, "catalog", nil); r["error"] != nil {
		t.Fatal(r)
	}
	r := call(2, "prepare", map[string]any{"name": "app.quit", "params": map[string]any{"bundle_id": "com.example.app"}})
	plan := r["result"].(map[string]any)
	if !strings.Contains(plan["effect"].(string), "com.example.app") {
		t.Fatalf("effect: %v", plan)
	}
	if r := call(3, "execute", map[string]any{"plan_id": plan["plan_id"]}); r["error"] == nil || *ran != 0 {
		t.Fatal("executed without confirmation over the socket")
	}
	rec := confirm.Sign(s.key, confirm.Record{PlanID: plan["plan_id"].(string), PlanHash: plan["plan_hash"].(string), ConfirmedAt: time.Now().UTC().Truncate(time.Second), Via: "test", Nonce: "sock-1"})
	if r := call(4, "execute", map[string]any{"plan_id": plan["plan_id"], "confirmations": []confirm.Record{rec}}); r["error"] != nil || *ran != 1 {
		t.Fatalf("confirmed execute failed: %v", r)
	}
	if r := call(5, "shell", map[string]any{"cmd": "rm -rf ~"}); r["error"] == nil {
		t.Fatal("unknown method accepted")
	}
}
