// Package history is sysqd's time-series store (design §25): numeric series
// from sampled recipes, downsampled by age (5 s raw → 1 min → 10 min), whole
// tables for "what was running at 3:14 pm?", and event markers. SQLite in WAL
// mode under ~/Library/Application Support/sysq/history.db.
package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// Tiers, finest first.
const (
	TierRaw = 0 // as sampled
	Tier1m  = 1 // 1-minute averages
	Tier10m = 2 // 10-minute averages
)

// Retention per tier (defaults; schedules may lengthen raw retention).
var DefaultRetention = map[int]time.Duration{
	TierRaw: 2 * time.Hour,
	Tier1m:  48 * time.Hour,
	Tier10m: 30 * 24 * time.Hour,
}

// Store wraps the SQLite database.
type Store struct {
	db  *sql.DB
	mu  sync.Mutex
	now func() time.Time
}

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
CREATE TABLE IF NOT EXISTS points (
  recipe TEXT NOT NULL, label TEXT NOT NULL, col TEXT NOT NULL, key TEXT NOT NULL DEFAULT '',
  tier INTEGER NOT NULL, ts INTEGER NOT NULL, value REAL, n INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (recipe, label, col, key, tier, ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS points_ts ON points (tier, ts);
CREATE TABLE IF NOT EXISTS tables (
  recipe TEXT NOT NULL, label TEXT NOT NULL, ts INTEGER NOT NULL, data TEXT NOT NULL,
  PRIMARY KEY (recipe, label, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS events (
  ts INTEGER NOT NULL, kind TEXT NOT NULL, recipe TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL, severity TEXT NOT NULL DEFAULT 'info'
);
CREATE INDEX IF NOT EXISTS events_ts ON events (ts);
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
`

// Open opens (creating) the store. readOnly opens an existing DB for the CLI.
func Open(path string, readOnly bool) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)"
	if readOnly {
		dsn += "&mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if !readOnly {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return nil, fmt.Errorf("history: %w", err)
		}
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SetClock replaces the clock (tests).
func (s *Store) SetClock(f func() time.Time) { s.now = f }

// Sample is one recipe result to record.
type Sample struct {
	Recipe string
	Label  string
	TS     time.Time
	Table  *table.Table
	// KeepTable stores the whole table (consumers) in addition to numbers.
	KeepTable bool
	// Columns limits recorded numeric columns (empty = all numeric).
	Columns []string
	// Key names a column whose values split series (e.g. pid, name); rows
	// are recorded per key value. Empty: only the first row is recorded.
	Key string
}

// Record stores a sample's numeric columns at the raw tier.
func (s *Store) Record(ctx context.Context, smp Sample) error {
	if smp.Table == nil || smp.Table.Len() == 0 {
		return nil
	}
	ts := smp.TS.UnixMilli()
	want := map[string]bool{}
	for _, c := range smp.Columns {
		want[c] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO points (recipe,label,col,key,tier,ts,value,n) VALUES (?,?,?,?,0,?,?,1)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	rows := smp.Table.Rows
	keyIdx := -1
	if smp.Key != "" {
		keyIdx = smp.Table.ColIndex(smp.Key)
	}
	if keyIdx < 0 {
		rows = rows[:1]
	} else if len(rows) > 50 {
		rows = rows[:50]
	}
	for _, r := range rows {
		key := ""
		if keyIdx >= 0 {
			key = table.Format(r[keyIdx])
		}
		for i, c := range smp.Table.Schema {
			if len(want) > 0 && !want[c.Name] {
				continue
			}
			v, ok := numeric(c, r[i])
			if !ok {
				continue
			}
			if _, err := stmt.ExecContext(ctx, smp.Recipe, smp.Label, c.Name, key, ts, v); err != nil {
				return err
			}
		}
	}
	if smp.KeepTable {
		data, err := json.Marshal(smp.Table)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO tables (recipe,label,ts,data) VALUES (?,?,?,?)`, smp.Recipe, smp.Label, ts, string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// numeric maps a cell to a float for the series (bools 0/1, enums by
// position, timestamps skipped).
func numeric(c table.Col, v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch c.Type {
	case table.Bool:
		if b, ok := v.(bool); ok && b {
			return 1, true
		}
		return 0, true
	case table.Enum:
		for i, x := range c.Values {
			if x == v {
				return float64(i), true
			}
		}
		return 0, false
	case table.TS, table.Str, table.Path, table.JSON, table.TableT:
		return 0, false
	}
	f, ok := table.ToFloat(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// Event records a marker for graphs and the narrative.
type Event struct {
	TS       time.Time `json:"ts"`
	Kind     string    `json:"kind"`
	Recipe   string    `json:"recipe,omitempty"`
	Subject  string    `json:"subject,omitempty"`
	Message  string    `json:"message"`
	Severity string    `json:"severity,omitempty"`
}

// AddEvent stores an event.
func (s *Store) AddEvent(ctx context.Context, e Event) error {
	if e.TS.IsZero() {
		e.TS = s.now()
	}
	if e.Severity == "" {
		e.Severity = "info"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (ts,kind,recipe,subject,message,severity) VALUES (?,?,?,?,?,?)`,
		e.TS.UnixMilli(), e.Kind, e.Recipe, e.Subject, e.Message, e.Severity)
	return err
}

// Downsample rolls raw points into 1-minute and 10-minute averages and drops
// data past each tier's retention. rawKeep may extend raw retention.
func (s *Store) Downsample(ctx context.Context, rawKeep time.Duration) error {
	now := s.now()
	keep := DefaultRetention
	raw := keep[TierRaw]
	if rawKeep > raw {
		raw = rawKeep
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A bucket is recomputed only while every finer point it covers is
	// still stored: from the first whole bucket after the finer tier's
	// retention cutoff up to (not including) the current bucket. A bucket
	// the cutoff falls inside was already rolled up complete on an earlier
	// pass and keeps that value.
	roll := func(from, to int, bucket, fromKeep time.Duration) error {
		b := bucket.Milliseconds()
		newerThan := now.Add(-fromKeep).Truncate(bucket).Add(bucket)
		olderThan := now.Truncate(bucket)
		_, err := s.db.ExecContext(ctx, `
INSERT OR REPLACE INTO points (recipe,label,col,key,tier,ts,value,n)
SELECT recipe,label,col,key,?, (ts / ?) * ?, SUM(value*n)/SUM(n), SUM(n)
FROM points WHERE tier = ? AND ts >= ? AND ts < ?
GROUP BY recipe,label,col,key,(ts / ?)`, to, b, b, from, newerThan.UnixMilli(), olderThan.UnixMilli(), b)
		return err
	}
	if err := roll(TierRaw, Tier1m, time.Minute, raw); err != nil {
		return err
	}
	if err := roll(Tier1m, Tier10m, 10*time.Minute, keep[Tier1m]); err != nil {
		return err
	}
	for tier, d := range map[int]time.Duration{TierRaw: raw, Tier1m: keep[Tier1m], Tier10m: keep[Tier10m]} {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM points WHERE tier = ? AND ts < ?`, tier, now.Add(-d).UnixMilli()); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM tables WHERE ts < ?`, now.Add(-keep[Tier1m]).UnixMilli()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, now.Add(-keep[Tier10m]).UnixMilli())
	return err
}

// tierFor picks the finest tier that covers a range.
func tierFor(rng time.Duration) int {
	switch {
	case rng <= DefaultRetention[TierRaw]:
		return TierRaw
	case rng <= DefaultRetention[Tier1m]:
		return Tier1m
	}
	return Tier10m
}

// Query returns a table with a t column and one column per recorded series
// column of a recipe (or just `column`), over the last rng.
func (s *Store) Query(ctx context.Context, recipe, column string, rng time.Duration, key string) (*table.Table, error) {
	if rng <= 0 {
		rng = time.Hour
	}
	since := s.now().Add(-rng).UnixMilli()
	tier := tierFor(rng)
	q := `SELECT col, key, ts, value FROM points WHERE recipe = ? AND tier = ? AND ts >= ?`
	args := []any{recipe, tier, since}
	if column != "" {
		q += ` AND col = ?`
		args = append(args, column)
	}
	if key != "" {
		q += ` AND key = ?`
		args = append(args, key)
	}
	q += ` ORDER BY ts`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type cell struct {
		col, key string
		ts       int64
		v        sql.NullFloat64
	}
	var cells []cell
	cols := []string{}
	seenCol := map[string]bool{}
	keyed := false
	for rows.Next() {
		var c cell
		if err := rows.Scan(&c.col, &c.key, &c.ts, &c.v); err != nil {
			return nil, err
		}
		if !seenCol[c.col] {
			seenCol[c.col] = true
			cols = append(cols, c.col)
		}
		if c.key != "" {
			keyed = true
		}
		cells = append(cells, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	schema := []table.Col{{Name: "t", Type: table.TS}}
	if keyed {
		schema = append(schema, table.Col{Name: "key", Type: table.Str})
	}
	if column != "" {
		schema = append(schema, table.Col{Name: "value", Type: table.Float})
	} else {
		for _, c := range cols {
			schema = append(schema, table.Col{Name: c, Type: table.Float})
		}
	}
	out := table.New(schema)
	type rk struct {
		ts  int64
		key string
	}
	index := map[rk]int{}
	for _, c := range cells {
		k := rk{c.ts, c.key}
		i, ok := index[k]
		if !ok {
			row := make([]any, len(schema))
			row[0] = time.UnixMilli(c.ts)
			if keyed {
				row[1] = c.key
			}
			out.Rows = append(out.Rows, row)
			i = len(out.Rows) - 1
			index[k] = i
		}
		ci := 1
		if keyed {
			ci = 2
		}
		if column == "" {
			ci = out.ColIndex(c.col)
		}
		if c.v.Valid {
			out.Rows[i][ci] = c.v.Float64
		}
	}
	out.Meta.Source = []string{"history"}
	out.Meta.Sensitivity = "public"
	if keyed {
		out.Meta.Sensitivity = "personal"
	}
	return out.Fix(), nil
}

// Events returns markers in the last rng.
func (s *Store) Events(ctx context.Context, rng time.Duration, kind string) (*table.Table, error) {
	since := s.now().Add(-rng).UnixMilli()
	q := `SELECT ts, kind, recipe, subject, message, severity FROM events WHERE ts >= ?`
	args := []any{since}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY ts`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := table.New(EventSchema)
	for rows.Next() {
		var ts int64
		var e Event
		if err := rows.Scan(&ts, &e.Kind, &e.Recipe, &e.Subject, &e.Message, &e.Severity); err != nil {
			return nil, err
		}
		out.Append(time.UnixMilli(ts), e.Kind, e.Recipe, e.Subject, e.Message, e.Severity)
	}
	out.Meta.Source = []string{"history.events"}
	out.Meta.Sensitivity = "personal"
	return out, rows.Err()
}

// EventSchema is the history.events table.
var EventSchema = []table.Col{
	{Name: "ts", Type: table.TS}, {Name: "kind", Type: table.Str}, {Name: "recipe", Type: table.Str},
	{Name: "subject", Type: table.Str}, {Name: "message", Type: table.Str},
	{Name: "severity", Type: table.Enum, Values: []string{"info", "low", "medium", "high", "critical"}},
}

// TableAt returns the stored table of a recipe nearest to t (within window).
func (s *Store) TableAt(ctx context.Context, recipe string, t time.Time, window time.Duration) (*table.Table, error) {
	row := s.db.QueryRowContext(ctx, `SELECT ts, data FROM tables WHERE recipe = ? AND ts BETWEEN ? AND ?
ORDER BY ABS(ts - ?) LIMIT 1`, recipe, t.Add(-window).UnixMilli(), t.Add(window).UnixMilli(), t.UnixMilli())
	var ts int64
	var data string
	if err := row.Scan(&ts, &data); err != nil {
		if err == sql.ErrNoRows {
			out := table.New(nil)
			out.Meta.Error = fmt.Sprintf("no %s sample near %s", recipe, t.Format(time.Kitchen))
			return out, nil
		}
		return nil, err
	}
	var out table.Table
	if err := json.Unmarshal([]byte(data), &out); err != nil {
		return nil, err
	}
	out.Meta.SnapshotTS = time.UnixMilli(ts)
	return &out, nil
}

// Recipes lists recipes with recorded series.
func (s *Store) Recipes(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT recipe FROM points ORDER BY recipe`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Stats summarizes one series over a range: first, last, min, max, avg and
// the time spent at or above a threshold (for the narrative).
type Stats struct {
	First, Last, Min, Max, Avg float64
	MinAt, MaxAt               time.Time
	N                          int
	Span                       time.Duration
}

// Stats computes series statistics.
func (s *Store) Stats(ctx context.Context, recipe, column string, rng time.Duration) (Stats, error) {
	t, err := s.Query(ctx, recipe, column, rng, "")
	if err != nil || t.Len() == 0 {
		return Stats{}, err
	}
	st := Stats{Min: math.Inf(1), Max: math.Inf(-1)}
	sum := 0.0
	for i, r := range t.Rows {
		v, ok := r[len(r)-1].(float64)
		if !ok {
			continue
		}
		ts := r[0].(time.Time)
		if st.N == 0 {
			st.First = v
		}
		st.Last = v
		if v < st.Min {
			st.Min, st.MinAt = v, ts
		}
		if v > st.Max {
			st.Max, st.MaxAt = v, ts
		}
		sum += v
		st.N++
		if i == t.Len()-1 {
			st.Span = ts.Sub(t.Rows[0][0].(time.Time))
		}
	}
	if st.N > 0 {
		st.Avg = sum / float64(st.N)
	}
	return st, nil
}

// SetMeta / GetMeta store small state (last seen status per group, ...).
func (s *Store) SetMeta(ctx context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta (k, v) VALUES (?, ?)`, k, v)
	return err
}

// GetMeta reads small state.
func (s *Store) GetMeta(ctx context.Context, k string) string {
	var v string
	_ = s.db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	return v
}
