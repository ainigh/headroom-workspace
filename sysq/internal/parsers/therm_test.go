package parsers

import "testing"

// pmset prints "Thermal warning level set to N." once a level has been
// recorded since boot; 0 is the normal level the Mac returns to after an
// event, so it must not keep the state at "fair".
func TestPmsetThermRecordedNormalLevel(t *testing.T) {
	for _, c := range []struct {
		out   string
		level int64
	}{
		{"Thermal warning level set to 0.\nNote: No performance warning level has been recorded\n", 0},
		{"Performance warning level set to 0.\nNote: No thermal warning level has been recorded\n", 0},
		{"Thermal warning level set to 5.\n", 1},
	} {
		rows, err := parsePmsetTherm(Input{Outputs: map[string][]byte{"main": []byte(c.out)}})
		if err != nil || len(rows) != 1 {
			t.Fatalf("%q: %v %v", c.out, rows, err)
		}
		if rows[0]["level"] != c.level {
			t.Errorf("%q: level %v, want %d", c.out, rows[0]["level"], c.level)
		}
		if c.level == 0 && rows[0]["warning"] != "" {
			t.Errorf("%q: warning %q for the normal level", c.out, rows[0]["warning"])
		}
	}
}
