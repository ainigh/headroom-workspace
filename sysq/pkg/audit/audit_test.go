package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAppendsDailyJSONLWithPrivateMode(t *testing.T) {
	l := &Log{Dir: filepath.Join(t.TempDir(), "audit")}
	ts := time.Date(2026, 10, 19, 23, 30, 0, 0, time.Local)
	for i := range 2 {
		if err := l.Write(Record{TS: ts, Component: "sysq", Kind: "recipe", Name: "a", Rows: i}); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(l.Dir, "2026-10-19.jsonl")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %o", st.Mode().Perm())
	}
	data, _ := os.ReadFile(p)
	if n := len(splitLines(data)); n != 2 {
		t.Errorf("%d lines, want 2", n)
	}
	var nilLog *Log
	if err := nilLog.Write(Record{}); err != nil {
		t.Error("nil log must be a no-op")
	}
}

func TestReadCoversTodayInNegativeUTCOffsets(t *testing.T) {
	// A Mac in Los Angeles at 10:00 local: today's file must be read even
	// though the UTC-truncated day boundary is 17:00 local.
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no tz database")
	}
	l := &Log{Dir: t.TempDir()}
	now := time.Now().In(la)
	since := now.Add(-time.Hour)
	rec := Record{TS: now.Add(-time.Minute), Component: "sysq", Kind: "recipe", Name: "x"}
	if err := l.Write(rec); err != nil {
		t.Fatal(err)
	}
	// Write names the file by the local date; emulate the same here.
	got, err := l.Read(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "x" {
		t.Fatalf("Read(since 1h ago) = %+v, want the record written a minute ago", got)
	}
	if got, _ := l.Read(now.Add(time.Minute)); len(got) != 0 {
		t.Errorf("records before since returned: %+v", got)
	}
}

func TestReadSpansDaysAndSkipsBadLines(t *testing.T) {
	l := &Log{Dir: t.TempDir()}
	now := time.Now()
	for _, age := range []time.Duration{72 * time.Hour, 36 * time.Hour, time.Hour} {
		if err := l.Write(Record{TS: now.Add(-age), Kind: "k"}); err != nil {
			t.Fatal(err)
		}
	}
	today := filepath.Join(l.Dir, now.Format("2006-01-02")+".jsonl")
	f, _ := os.OpenFile(today, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("not json\n")
	f.Close()
	got, err := l.Read(now.Add(-48 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d records, want 2 (36h and 1h old)", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].TS.Before(got[i-1].TS) {
			t.Error("records not oldest first")
		}
	}
}
