// Package run executes a prepared command. Argv is passed straight to
// exec; the AppleScript wrappers are fixed text in this file and receive
// their inputs as osascript arguments, never spliced into script source.
package run

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Command is what a plan resolves to.
type Command struct {
	Kind    string   `json:"kind"`
	Argv    []string `json:"argv,omitempty"`
	Target  string   `json:"target,omitempty"`
	Admin   bool     `json:"admin,omitempty"`
	Timeout time.Duration
}

// Display renders the command for the confirmation sheet.
func (c Command) Display() string {
	switch c.Kind {
	case "trash":
		return "Finder: move to Trash " + c.Target
	case "quit_app":
		return "quit app id " + c.Target
	case "quit_app_named":
		return "quit app " + c.Target
	}
	parts := make([]string, len(c.Argv))
	for i, a := range c.Argv {
		if strings.ContainsAny(a, " \t\"'") || a == "" {
			a = fmt.Sprintf("%q", a)
		}
		parts[i] = a
	}
	s := strings.Join(parts, " ")
	if c.Admin {
		s += "   (as administrator — macOS asks for your password)"
	}
	return s
}

// Result is the outcome.
type Result struct {
	OK       bool   `json:"ok"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

// Runner executes commands.
type Runner interface {
	Run(ctx context.Context, c Command) (Result, error)
}

// OS runs commands on this Mac.
type OS struct{}

const osascript = "/usr/bin/osascript"

// Fixed AppleScript programs. Inputs arrive as `argv` items.
var (
	scriptTrash = []string{
		"on run argv",
		"tell application \"Finder\" to delete (POSIX file (item 1 of argv) as alias)",
		"return \"moved to Trash\"",
		"end run",
	}
	scriptQuitID = []string{
		"on run argv",
		"tell application id (item 1 of argv) to quit",
		"return \"quit requested\"",
		"end run",
	}
	scriptQuitName = []string{
		"on run argv",
		"tell application (item 1 of argv) to quit",
		"return \"quit requested\"",
		"end run",
	}
	// Each argv element is quoted by AppleScript itself (quoted form of);
	// macOS shows its own administrator password dialog.
	scriptAdmin = []string{
		"on run argv",
		"set cmd to \"\"",
		"repeat with a in argv",
		"set cmd to cmd & quoted form of (a as text) & \" \"",
		"end repeat",
		"return do shell script cmd with administrator privileges",
		"end run",
	}
)

func osa(lines []string, args ...string) []string {
	out := []string{osascript}
	for _, l := range lines {
		out = append(out, "-e", l)
	}
	out = append(out, "--")
	return append(out, args...)
}

// Argv returns the exact process argv a command runs as.
func Argv(c Command) []string {
	switch c.Kind {
	case "trash":
		return osa(scriptTrash, c.Target)
	case "quit_app":
		return osa(scriptQuitID, c.Target)
	case "quit_app_named":
		return osa(scriptQuitName, c.Target)
	}
	if c.Admin {
		return osa(scriptAdmin, c.Argv...)
	}
	return c.Argv
}

// Run implements Runner.
func (OS) Run(ctx context.Context, c Command) (Result, error) {
	args := Argv(c)
	if len(args) == 0 || !strings.HasPrefix(args[0], "/") {
		return Result{}, fmt.Errorf("refusing to run %v", args)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=en_US.UTF-8"}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	res := Result{OK: err == nil, Output: truncate(out.String(), 4000)}
	if ee, ok := err.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
