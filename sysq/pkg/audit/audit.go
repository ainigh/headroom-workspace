// Package audit writes append-only JSONL audit records (implementation.md
// §3.8). sysq and sysact share the format; each writes its own directory.
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one audited request.
type Record struct {
	TS          time.Time      `json:"ts"`
	Component   string         `json:"component"` // sysq | sysact
	Caller      string         `json:"caller"`
	Kind        string         `json:"kind"` // primitive | recipe | script | action.prepare | action.execute | ...
	Name        string         `json:"name,omitempty"`
	Script      string         `json:"script,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	Primitives  []string       `json:"primitives,omitempty"`
	DurationMS  int64          `json:"duration_ms"`
	Rows        int            `json:"rows"`
	Bytes       int            `json:"bytes"`
	Sensitivity string         `json:"sensitivity,omitempty"`
	Truncated   bool           `json:"truncated,omitempty"`
	Error       string         `json:"error,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// Log appends records to <dir>/YYYY-MM-DD.jsonl.
type Log struct {
	Dir string
	mu  sync.Mutex
}

// Write appends one record. Failures are returned but never block callers.
func (l *Log) Write(r Record) error {
	if l == nil || l.Dir == "" {
		return nil
	}
	if r.TS.IsZero() {
		r.TS = time.Now()
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(l.Dir, r.TS.Format("2006-01-02")+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Read returns records newer than since, oldest first.
func (l *Log) Read(since time.Time) ([]Record, error) {
	var out []Record
	for d := since.Truncate(24 * time.Hour); !d.After(time.Now()); d = d.Add(24 * time.Hour) {
		data, err := os.ReadFile(filepath.Join(l.Dir, d.Format("2006-01-02")+".jsonl"))
		if err != nil {
			continue
		}
		for _, line := range splitLines(data) {
			var r Record
			if json.Unmarshal(line, &r) == nil && !r.TS.Before(since) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
