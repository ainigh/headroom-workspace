package exec

import (
	"context"
	"errors"
	"io"
	osexec "os/exec"
	"strings"
	"testing"
	"time"
)

// unsandboxedSpawn lets the runner's own logic (timeouts, caps) be tested on
// machines without sandbox-exec. It is only reachable from this test file.
func unsandboxedSpawn(ctx context.Context, argv, env []string, stdin []byte, stdout, stderr io.Writer) (int, error) {
	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	setProcessGroup(cmd)
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	var ee *osexec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

func testRunner(t *testing.T) *Runner {
	return &Runner{Sandbox: testSandbox(t), Scratch: t.TempDir(), spawn: unsandboxedSpawn}
}

func TestRunnerWrapsWithSandbox(t *testing.T) {
	r := testRunner(t)
	var seen []string
	r.spawn = func(ctx context.Context, argv, env []string, stdin []byte, stdout, stderr io.Writer) (int, error) {
		seen = argv
		return 0, nil
	}
	if _, err := r.Run(t.Context(), Invocation{Argv: []string{"/bin/echo", "a b"}, Profile: "read-only-default"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "/bin/echo" || seen[1] != "a b" {
		t.Fatalf("argv changed: %q", seen)
	}
}

func TestRunnerTimeout(t *testing.T) {
	r := testRunner(t)
	_, err := r.Run(t.Context(), Invocation{Argv: []string{"/bin/sleep", "5"}, Profile: "read-only-default", Timeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want timeout, got %v", err)
	}
	res, err := r.Run(t.Context(), Invocation{Argv: []string{"/bin/sh", "-c", "echo partial; sleep 5"}, Profile: "read-only-default", Timeout: 300 * time.Millisecond, PartialOK: true})
	if err != nil || !res.TimedOut || !strings.Contains(string(res.Stdout), "partial") {
		t.Fatalf("partial: %v %+v", err, res)
	}
}

func TestRunnerCapsOutput(t *testing.T) {
	r := testRunner(t)
	res, err := r.Run(t.Context(), Invocation{Argv: []string{"/usr/bin/yes"}, Profile: "read-only-default", MaxBytes: 1000, PartialOK: true, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Stdout) != 1000 {
		t.Fatalf("cap not applied: %d bytes, truncated=%v", len(res.Stdout), res.Truncated)
	}
	if _, err := r.Run(t.Context(), Invocation{Argv: []string{"/usr/bin/yes"}, Profile: "read-only-default", MaxBytes: 1000, Timeout: 5 * time.Second}); err == nil {
		t.Fatal("over-cap output accepted without PartialOK")
	}
}

func TestRunnerRefusesRelativeArgv0(t *testing.T) {
	r := testRunner(t)
	if _, err := r.Run(t.Context(), Invocation{Argv: []string{"echo"}, Profile: "read-only-default"}); err == nil {
		t.Fatal("relative argv[0] accepted")
	}
}

func TestRunnerMinimalEnv(t *testing.T) {
	r := testRunner(t)
	t.Setenv("SECRET_TOKEN", "x")
	res, err := r.Run(t.Context(), Invocation{Argv: []string{"/usr/bin/env"}, Profile: "read-only-default"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Stdout), "SECRET_TOKEN") {
		t.Fatal("environment leaked into primitive")
	}
}
