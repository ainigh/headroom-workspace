// Package exec runs catalog argv under the seatbelt with timeouts and output
// caps. There is no code path here that starts a process without
// sandbox-exec.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"time"
)

// Invocation is one sandboxed command.
type Invocation struct {
	Argv        []string
	Profile     string        // profile name in Sandbox.Profiles
	ProfileText string        // pre-rendered profile (tests); overrides Profile
	Execs       []string      // extra binaries/helper dirs the profile may exec
	Timeout     time.Duration // default 10s
	MaxBytes    int64         // stdout cap; default 4 MiB
	PartialOK   bool          // on timeout/cap, return what was read
	Stdin       []byte
}

// Result is what a command produced.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	TimedOut  bool
	Truncated bool
	Duration  time.Duration
}

// Runner executes invocations.
type Runner struct {
	Sandbox *Sandbox
	Scratch string // parent of per-run scratch directories

	// spawn replaces process start in this package's tests only.
	spawn func(ctx context.Context, argv []string, env []string, stdin []byte, stdout, stderr io.Writer) (int, error)
}

// ErrTimeout is returned when a command exceeded its timeout and partial
// output was not acceptable.
var ErrTimeout = errors.New("timed out")

// Run executes one invocation under the sandbox.
func (r *Runner) Run(ctx context.Context, inv Invocation) (Result, error) {
	if len(inv.Argv) == 0 || !filepath.IsAbs(inv.Argv[0]) {
		return Result{}, fmt.Errorf("argv[0] must be an absolute path: %v", inv.Argv)
	}
	if r.spawn == nil && !Available() {
		return Result{}, ErrNoSandbox
	}
	if r.Sandbox == nil {
		return Result{}, ErrNoSandbox
	}
	base := r.Scratch
	if base == "" {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return Result{}, err
	}
	scratch, err := os.MkdirTemp(base, "run-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(scratch)

	profile := inv.ProfileText
	if profile == "" {
		execs := append([]string{inv.Argv[0]}, inv.Execs...)
		profile, err = r.Sandbox.Render(inv.Profile, execs, scratch)
		if err != nil {
			return Result{}, err
		}
	}
	argv := Wrap(profile, inv.Argv)

	timeout := inv.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	maxBytes := inv.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 4 << 20
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	home, _ := os.UserHomeDir()
	env := []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + home,
		"TMPDIR=" + scratch,
		"LANG=C", "LC_ALL=C",
		"TERM=dumb", "COLUMNS=1000",
	}
	out := &capped{max: maxBytes, cancel: cancel}
	errW := &capped{max: 256 << 10}

	start := time.Now()
	var code int
	if r.spawn != nil {
		code, err = r.spawn(ctx, argv[3:], env, inv.Stdin, out, errW)
	} else {
		code, err = start1(ctx, argv, env, inv.Stdin, out, errW)
	}
	res := Result{
		Stdout:    out.buf.Bytes(),
		Stderr:    errW.buf.Bytes(),
		ExitCode:  code,
		Truncated: out.over,
		Duration:  time.Since(start),
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && !out.over {
		res.TimedOut = true
		if inv.PartialOK {
			return res, nil
		}
		return res, fmt.Errorf("%s: %w after %s", filepath.Base(inv.Argv[0]), ErrTimeout, timeout)
	}
	if out.over {
		if inv.PartialOK {
			return res, nil
		}
		return res, fmt.Errorf("%s: output exceeded %d bytes", filepath.Base(inv.Argv[0]), maxBytes)
	}
	if err != nil {
		return res, fmt.Errorf("%s: %w", filepath.Base(inv.Argv[0]), err)
	}
	return res, nil
}

func start1(ctx context.Context, argv, env []string, stdin []byte, stdout, stderr io.Writer) (int, error) {
	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = "/"
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ee := (*osexec.ExitError)(nil); errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// capped is a writer that stops the command once max bytes arrive.
type capped struct {
	buf    bytes.Buffer
	max    int64
	over   bool
	cancel context.CancelFunc
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.max - int64(c.buf.Len())
	if room <= 0 {
		c.over = true
		if c.cancel != nil {
			c.cancel()
		}
		return len(p), nil
	}
	if int64(len(p)) > room {
		c.buf.Write(p[:room])
		c.over = true
		if c.cancel != nil {
			c.cancel()
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}
