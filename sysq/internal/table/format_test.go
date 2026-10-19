package table

import (
	"testing"
	"time"
)

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:                     "0 B",
		999:                   "999 B",
		1000:                  "1.0 KB",
		1536:                  "1.5 KB",
		99_950:                "100 KB",
		999_950:               "1.0 MB", // rounds up into the next unit, never "1000.0 KB"
		999_500_000:           "1.0 GB",
		123_456_789_000:       "123 GB",
		-2_500_000:            "-2.5 MB",
		5_000_000_000_000_000: "5.0 PB",
	}
	for n, want := range cases {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[float64]string{
		0.25:    "250ms",
		40:      "40s",
		130:     "2m 10s",
		7800:    "2h 10m",
		90_000:  "25h 0m", // hours up to 48h
		200_000: "2d 7h",
		-40:     "-40s",
	}
	for sec, want := range cases {
		if got := HumanDuration(sec); got != want {
			t.Errorf("HumanDuration(%v) = %q, want %q", sec, got, want)
		}
	}
}

func TestHumanizeByColumnType(t *testing.T) {
	ts := time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC)
	cases := []struct {
		col  Col
		v    any
		want string
	}{
		{Col{Type: Bytes}, int64(2_000_000), "2.0 MB"},
		{Col{Type: Bytes, Unit: "bytes"}, int64(10), "10 B"},
		{Col{Type: Duration}, 65.0, "1m 5s"},
		{Col{Type: Float, Unit: "%"}, 12.34, "12.3 %"},
		{Col{Type: Int, Unit: "MHz"}, int64(2400), "2400 MHz"},
		{Col{Type: Str, Unit: "ignored"}, "x", "x"},
		{Col{Type: TS}, ts, ts.Local().Format("2006-01-02 15:04:05")},
		{Col{Type: Int}, nil, "—"},
	}
	for _, c := range cases {
		if got := Humanize(c.col, c.v); got != c.want {
			t.Errorf("Humanize(%+v, %v) = %q, want %q", c.col, c.v, got, c.want)
		}
	}
}

func TestFormat(t *testing.T) {
	sub := New([]Col{{Name: "a", Type: Int}})
	sub.Append(int64(1))
	cases := []struct {
		v    any
		want string
	}{
		{nil, ""},
		{"s", "s"},
		{3.0, "3"},
		{3.14159, "3.14"},
		{int64(7), "7"},
		{true, "true"},
		{sub, "<table 1 rows>"},
		{map[string]any{"k": 1}, `{"k":1}`},
	}
	for _, c := range cases {
		if got := Format(c.v); got != c.want {
			t.Errorf("Format(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func rowsTable(n int) *Table {
	t := New([]Col{{Name: "i", Type: Int}})
	for i := range n {
		t.Append(int64(i))
	}
	return t
}

func TestLimitTailSampleArePureAndCountDrops(t *testing.T) {
	src := rowsTable(10)
	head := src.Limit(3, 2)
	if head.Len() != 3 || head.Rows[0][0] != int64(2) || head.Meta.DroppedByLimit != 5 {
		t.Errorf("Limit(3,2): %v dropped=%d", head.Rows, head.Meta.DroppedByLimit)
	}
	if src.Limit(5, 50).Len() != 0 || src.Limit(-1, -1).Len() != 0 {
		t.Error("out-of-range limit should yield an empty table, not panic")
	}
	tail := src.Tail(2)
	if tail.Len() != 2 || tail.Rows[1][0] != int64(9) || tail.Meta.DroppedByLimit != 8 {
		t.Errorf("Tail(2): %v dropped=%d", tail.Rows, tail.Meta.DroppedByLimit)
	}
	if src.Tail(99).Len() != 10 || src.Tail(-1).Len() != 0 {
		t.Error("Tail bounds")
	}
	s1, s2 := src.Sample(4, 7), src.Sample(4, 7)
	if s1.Len() != 4 || s1.Meta.DroppedByLimit != 6 {
		t.Errorf("Sample: %v dropped=%d", s1.Rows, s1.Meta.DroppedByLimit)
	}
	for i := range s1.Rows {
		if s1.Rows[i][0] != s2.Rows[i][0] {
			t.Fatal("Sample is not deterministic for a seed")
		}
		if i > 0 && s1.Rows[i][0].(int64) < s1.Rows[i-1][0].(int64) {
			t.Fatal("Sample must keep source order")
		}
	}
	if src.Len() != 10 || src.Meta.DroppedByLimit != 0 {
		t.Error("operators mutated their input")
	}
}
