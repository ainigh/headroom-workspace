// Package confirm defines the confirmation record a host signs when a person
// approves a plan (implementation.md §4). sysact executes nothing without at
// least one valid record; there is no switch that turns this off.
//
// Signature: hex(HMAC-SHA256(key, Canonical(record))), where key is the
// host key sysact creates in its data directory (host.key, 0600) and the
// host app reads. Canonical is:
//
//	"sysact-confirm-v1\n" + plan_id + "\n" + plan_hash + "\n" +
//	confirmed_at (RFC 3339, UTC, seconds) + "\n" + nonce + "\n" + via
package confirm

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Record is one confirmation.
type Record struct {
	PlanID      string    `json:"plan_id"`
	PlanHash    string    `json:"plan_hash"`
	ConfirmedAt time.Time `json:"confirmed_at"`
	Via         string    `json:"via"`   // e.g. "headroom-ui", "headroom-assistant"
	Nonce       string    `json:"nonce"` // unique per confirmation
	Signature   string    `json:"signature"`
}

// Canonical returns the signed text.
func Canonical(r Record) string {
	return strings.Join([]string{"sysact-confirm-v1", r.PlanID, r.PlanHash,
		r.ConfirmedAt.UTC().Format(time.RFC3339), r.Nonce, r.Via}, "\n")
}

// Sign returns r with its signature set (hosts and tests).
func Sign(key []byte, r Record) Record {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(Canonical(r)))
	r.Signature = hex.EncodeToString(m.Sum(nil))
	return r
}

// Rules bound how old a confirmation may be.
var (
	MaxAge  = 2 * time.Minute
	MaxSkew = 30 * time.Second
)

// Errors.
var (
	ErrNone      = errors.New("execute needs at least one confirmation record signed by the host")
	ErrSignature = errors.New("confirmation signature is invalid")
	ErrStale     = errors.New("confirmation is too old or dated in the future")
	ErrMismatch  = errors.New("confirmation is for a different plan")
	ErrReplay    = errors.New("confirmation nonce was already used")
)

// Verify checks one record against a plan. seen reports and records nonces.
func Verify(key []byte, r Record, planID, planHash string, now time.Time, seen func(nonce string) bool) error {
	if len(key) < 32 {
		return fmt.Errorf("host key missing or too short")
	}
	if r.Nonce == "" || len(r.Nonce) > 128 {
		return fmt.Errorf("confirmation needs a nonce")
	}
	want := Sign(key, r).Signature
	got, err := hex.DecodeString(r.Signature)
	if err != nil {
		return ErrSignature
	}
	w, _ := hex.DecodeString(want)
	if !hmac.Equal(got, w) {
		return ErrSignature
	}
	if r.PlanID != planID || r.PlanHash != planHash {
		return ErrMismatch
	}
	if now.Sub(r.ConfirmedAt) > MaxAge || r.ConfirmedAt.Sub(now) > MaxSkew {
		return ErrStale
	}
	if seen(r.Nonce) {
		return ErrReplay
	}
	return nil
}

// LoadOrCreateKey returns the host key, creating it (0600) on first use.
func LoadOrCreateKey(dir string) ([]byte, error) {
	p := filepath.Join(dir, "host.key")
	if data, err := os.ReadFile(p); err == nil {
		k, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(k) < 32 {
			return nil, fmt.Errorf("%s is malformed", p)
		}
		return k, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(hex.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}
