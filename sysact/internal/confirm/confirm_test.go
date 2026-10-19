package confirm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func record(now time.Time) Record {
	return Record{PlanID: "plan-1", PlanHash: "hash-1", ConfirmedAt: now, Via: "headroom-ui", Nonce: "n-1"}
}

func neverSeen(string) bool { return false }

func TestVerifyAcceptsFreshSignedRecord(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	r := Sign(testKey(), record(now))
	if err := Verify(testKey(), r, "plan-1", "hash-1", now.Add(10*time.Second), neverSeen); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
}

func TestVerifyRejectsUnsignedAndTamperedRecords(t *testing.T) {
	now := time.Now()
	key := testKey()
	cases := map[string]Record{
		"no signature":   record(now),
		"garbage":        func() Record { r := record(now); r.Signature = "zz"; return r }(),
		"wrong key":      Sign(append([]byte{1}, key[1:]...), record(now)),
		"edited plan id": func() Record { r := Sign(key, record(now)); r.PlanID = "plan-2"; return r }(),
		"edited via":     func() Record { r := Sign(key, record(now)); r.Via = "cli"; return r }(),
		"edited nonce":   func() Record { r := Sign(key, record(now)); r.Nonce = "n-2"; return r }(),
		"edited time":    func() Record { r := Sign(key, record(now)); r.ConfirmedAt = now.Add(time.Second); return r }(),
	}
	for name, r := range cases {
		if err := Verify(key, r, "plan-1", "hash-1", now, neverSeen); !errors.Is(err, ErrSignature) {
			t.Errorf("%s: want ErrSignature, got %v", name, err)
		}
	}
}

func TestVerifyRejectsOtherPlan(t *testing.T) {
	now := time.Now()
	r := Sign(testKey(), record(now))
	if err := Verify(testKey(), r, "plan-2", "hash-1", now, neverSeen); !errors.Is(err, ErrMismatch) {
		t.Errorf("other plan id: want ErrMismatch, got %v", err)
	}
	if err := Verify(testKey(), r, "plan-1", "hash-2", now, neverSeen); !errors.Is(err, ErrMismatch) {
		t.Errorf("other plan hash: want ErrMismatch, got %v", err)
	}
}

func TestVerifyRejectsStaleAndFutureRecords(t *testing.T) {
	now := time.Now()
	r := Sign(testKey(), record(now))
	if err := Verify(testKey(), r, "plan-1", "hash-1", now.Add(MaxAge+time.Second), neverSeen); !errors.Is(err, ErrStale) {
		t.Errorf("stale: want ErrStale, got %v", err)
	}
	if err := Verify(testKey(), r, "plan-1", "hash-1", now.Add(-MaxSkew-time.Second), neverSeen); !errors.Is(err, ErrStale) {
		t.Errorf("future: want ErrStale, got %v", err)
	}
	if err := Verify(testKey(), r, "plan-1", "hash-1", now.Add(-MaxSkew+time.Second), neverSeen); err != nil {
		t.Errorf("within skew: unexpected %v", err)
	}
}

func TestVerifyRejectsReplayedNonceWithoutBurningItOnBadRecords(t *testing.T) {
	now := time.Now()
	seen := map[string]bool{}
	track := func(n string) bool {
		if seen[n] {
			return true
		}
		seen[n] = true
		return false
	}
	bad := record(now) // unsigned
	if err := Verify(testKey(), bad, "plan-1", "hash-1", now, track); !errors.Is(err, ErrSignature) {
		t.Fatalf("want ErrSignature, got %v", err)
	}
	if seen["n-1"] {
		t.Fatal("an invalid record must not consume its nonce")
	}
	good := Sign(testKey(), record(now))
	if err := Verify(testKey(), good, "plan-1", "hash-1", now, track); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := Verify(testKey(), good, "plan-1", "hash-1", now, track); !errors.Is(err, ErrReplay) {
		t.Fatalf("second use: want ErrReplay, got %v", err)
	}
}

func TestVerifyNeedsKeyAndNonce(t *testing.T) {
	now := time.Now()
	r := Sign(testKey(), record(now))
	if err := Verify([]byte("short"), r, "plan-1", "hash-1", now, neverSeen); err == nil {
		t.Error("short key accepted")
	}
	r.Nonce = ""
	if err := Verify(testKey(), Sign(testKey(), r), "plan-1", "hash-1", now, neverSeen); err == nil {
		t.Error("empty nonce accepted")
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sysact")
	k1, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 {
		t.Fatalf("key length %d", len(k1))
	}
	st, err := os.Stat(filepath.Join(dir, "host.key"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("host.key mode %o, want 0600", st.Mode().Perm())
	}
	k2, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(k1) != string(k2) {
		t.Error("second load returned a different key")
	}
	if err := os.WriteFile(filepath.Join(dir, "host.key"), []byte("not hex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(dir); err == nil {
		t.Error("malformed key file accepted")
	}
}
