package engine

import (
	"testing"

	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/exec"
)

// A partial_ok command killed at its timeout or output cap exits -1
// (signalled) with no runner error; its partial output is a result, not a
// failure ("find exited -1" dropped fs.large's rows).
func TestExitFailedKeepsPartialOutput(t *testing.T) {
	c := catalog.Command{AllowExit: []int{1}}
	for _, x := range []struct {
		name string
		res  exec.Result
		fail bool
	}{
		{"ok", exec.Result{ExitCode: 0}, false},
		{"allowed exit", exec.Result{ExitCode: 1}, false},
		{"real failure", exec.Result{ExitCode: 2}, true},
		{"killed at timeout", exec.Result{ExitCode: -1, TimedOut: true}, false},
		{"killed at output cap", exec.Result{ExitCode: -1, Truncated: true}, false},
		{"killed otherwise", exec.Result{ExitCode: -1}, true},
	} {
		if got := exitFailed(c, x.res); got != x.fail {
			t.Errorf("%s: exitFailed = %v, want %v", x.name, got, x.fail)
		}
	}
}
