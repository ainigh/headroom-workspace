package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ainigh/headroom-workspace/sysact/internal/confirm"
)

// The guarantee sysact exists for (implementation.md §4 MUST): execute fails
// without at least one confirmation record signed by the host. Nothing —
// no flag, env var or config — bypasses it. These tests were written before
// the server.
func TestExecuteRequiresConfirmation(t *testing.T) {
	s, ran := testServer(t, false)
	ctx := context.Background()
	plan, err := s.Prepare(ctx, "ai.ollama_unload", map[string]any{"model": "gemma-27b"})
	if err != nil {
		t.Fatal(err)
	}
	good := confirm.Sign(s.key, confirm.Record{PlanID: plan.ID, PlanHash: plan.Hash, ConfirmedAt: time.Now().UTC(), Via: "test", Nonce: "n1"})

	cases := map[string][]confirm.Record{
		"none":       nil,
		"empty list": {},
		"unsigned":   {{PlanID: plan.ID, PlanHash: plan.Hash, ConfirmedAt: time.Now().UTC(), Via: "test", Nonce: "n2"}},
		"wrong key":  {confirm.Sign([]byte("not the host key, not the host key"), confirm.Record{PlanID: plan.ID, PlanHash: plan.Hash, ConfirmedAt: time.Now().UTC(), Via: "test", Nonce: "n3"})},
		"other plan": {confirm.Sign(s.key, confirm.Record{PlanID: "plan-nope", PlanHash: plan.Hash, ConfirmedAt: time.Now().UTC(), Via: "test", Nonce: "n4"})},
		"tampered":   {func() confirm.Record { r := good; r.PlanHash = strings.Repeat("0", 64); return r }()},
		"stale":      {confirm.Sign(s.key, confirm.Record{PlanID: plan.ID, PlanHash: plan.Hash, ConfirmedAt: time.Now().Add(-10 * time.Minute).UTC(), Via: "test", Nonce: "n5"})},
		"future":     {confirm.Sign(s.key, confirm.Record{PlanID: plan.ID, PlanHash: plan.Hash, ConfirmedAt: time.Now().Add(10 * time.Minute).UTC(), Via: "test", Nonce: "n6"})},
		"no nonce":   {confirm.Sign(s.key, confirm.Record{PlanID: plan.ID, PlanHash: plan.Hash, ConfirmedAt: time.Now().UTC(), Via: "test"})},
	}
	for name, recs := range cases {
		if _, err := s.Execute(ctx, plan.ID, recs); err == nil {
			t.Errorf("%s: execute ran", name)
		}
	}
	if *ran != 0 {
		t.Fatalf("a command ran %d times without a valid confirmation", *ran)
	}
	res, err := s.Execute(ctx, plan.ID, []confirm.Record{good})
	if err != nil || *ran != 1 || !res.OK {
		t.Fatalf("valid confirmation refused: %v", err)
	}
	// Plans are single use and confirmations can't be replayed.
	if _, err := s.Execute(ctx, plan.ID, []confirm.Record{good}); err == nil {
		t.Fatal("plan executed twice")
	}
	plan2, _ := s.Prepare(ctx, "ai.ollama_unload", map[string]any{"model": "gemma-27b"})
	replay := good
	replay.PlanID, replay.PlanHash = plan2.ID, plan2.Hash
	replay = confirm.Sign(s.key, confirm.Record{PlanID: plan2.ID, PlanHash: plan2.Hash, ConfirmedAt: good.ConfirmedAt, Via: "test", Nonce: good.Nonce})
	if _, err := s.Execute(ctx, plan2.ID, []confirm.Record{replay}); err == nil {
		t.Fatal("nonce replayed")
	}
	if *ran != 1 {
		t.Fatal("replay ran a command")
	}
}

func TestDryRunNeverExecutes(t *testing.T) {
	s, ran := testServer(t, true)
	ctx := context.Background()
	plan, err := s.Prepare(ctx, "ai.ollama_unload", map[string]any{"model": "gemma-27b"})
	if err != nil {
		t.Fatal(err)
	}
	rec := confirm.Sign(s.key, confirm.Record{PlanID: plan.ID, PlanHash: plan.Hash, ConfirmedAt: time.Now().UTC(), Via: "test", Nonce: "d1"})
	if _, err := s.Execute(ctx, plan.ID, []confirm.Record{rec}); err == nil || *ran != 0 {
		t.Fatal("dry-run executed")
	}
}

// Every catalog entry refuses execution without confirmation and refuses
// protected paths at prepare (implementation.md §7).
func TestEveryActionRefuses(t *testing.T) {
	s, ran := testServer(t, false)
	ctx := context.Background()
	for _, a := range s.catalog.All() {
		params := sampleParams(a)
		plan, err := s.Prepare(ctx, a.Name, params)
		if err != nil {
			t.Errorf("%s: prepare with sample params: %v", a.Name, err)
			continue
		}
		if _, err := s.Execute(ctx, plan.ID, nil); err == nil {
			t.Errorf("%s executed without confirmation", a.Name)
		}
		for _, p := range a.PathParams() {
			for _, bad := range []string{"/", "/System/Library", "/usr/bin/true", "~/.ssh/id_ed25519", "~/Library/Keychains/login.keychain-db", "~", "~/Documents", "/Applications"} {
				pp := copyParams(params)
				pp[p] = bad
				if _, err := s.Prepare(ctx, a.Name, pp); err == nil {
					t.Errorf("%s accepted protected path %s", a.Name, bad)
				}
			}
		}
	}
	if *ran != 0 {
		t.Fatal("something ran")
	}
}

func TestNoBypassKnobs(t *testing.T) {
	t.Setenv("SYSACT_AUTO_APPROVE", "1")
	t.Setenv("SYSACT_NO_CONFIRM", "1")
	s, ran := testServer(t, false)
	plan, _ := s.Prepare(context.Background(), "ai.ollama_unload", map[string]any{"model": "gemma-27b"})
	if _, err := s.Execute(context.Background(), plan.ID, nil); err == nil || *ran != 0 {
		t.Fatal("an environment variable bypassed confirmation")
	}
}
