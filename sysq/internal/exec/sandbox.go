package exec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SandboxExec is the only way sysq starts a subprocess.
const SandboxExec = "/usr/bin/sandbox-exec"

// ErrNoSandbox is returned when the seatbelt is unavailable. sysq never falls
// back to running unsandboxed (design §23.3).
var ErrNoSandbox = errors.New("sandbox-exec is not available on this system; primitives cannot run (use --fixtures)")

// Sandbox renders seatbelt profiles and wraps argv with sandbox-exec.
type Sandbox struct {
	Profiles fs.FS    // directory of <name>.sb files
	Home     string   // user's home, for excluded roots
	Excluded []string // roots denied even for reading (~/.ssh, keychains, ...)
}

var profileName = regexp.MustCompile(`^[a-z0-9-]+$`)

// Render fills a profile template for one invocation. execs are the binaries
// the profile lets the process exec (the primitive's own binary, plus any
// helpers it declares); scratch is the only writable directory.
func (s *Sandbox) Render(profile string, execs []string, scratch string) (string, error) {
	if !profileName.MatchString(profile) {
		return "", fmt.Errorf("bad sandbox profile name %q", profile)
	}
	raw, err := fs.ReadFile(s.Profiles, profile+".sb")
	if err != nil {
		return "", fmt.Errorf("sandbox profile %q: %w", profile, err)
	}
	if scratch == "" || !filepath.IsAbs(scratch) {
		return "", errors.New("sandbox needs an absolute scratch directory")
	}
	var ex strings.Builder
	for _, e := range execs {
		if !filepath.IsAbs(e) {
			return "", fmt.Errorf("exec %q must be absolute", e)
		}
		if strings.HasSuffix(e, "/") {
			fmt.Fprintf(&ex, "(allow process-exec (subpath %s))\n", sbString(strings.TrimSuffix(e, "/")))
		} else {
			fmt.Fprintf(&ex, "(allow process-exec (literal %s))\n", sbString(e))
			// Most of /usr/bin are symlinks or firmlinks into /System; allow the
			// resolved path too.
			if r, err := filepath.EvalSymlinks(e); err == nil && r != e {
				fmt.Fprintf(&ex, "(allow process-exec (literal %s))\n", sbString(r))
			}
		}
	}
	var deny strings.Builder
	for _, x := range s.Excluded {
		p := x
		if strings.HasPrefix(p, "~") {
			p = s.Home + p[1:]
		}
		p = filepath.Clean(p)
		fmt.Fprintf(&deny, "(deny file-read* (subpath %s))\n", sbString(p))
		if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
			fmt.Fprintf(&deny, "(deny file-read* (subpath %s))\n", sbString(r))
		}
	}
	out := string(raw)
	out = strings.ReplaceAll(out, "__EXEC__", strings.TrimRight(ex.String(), "\n"))
	out = strings.ReplaceAll(out, "__DENY_READ__", strings.TrimRight(deny.String(), "\n"))
	scratchReal := scratch
	if r, err := filepath.EvalSymlinks(scratch); err == nil {
		scratchReal = r
	}
	out = strings.ReplaceAll(out, `(subpath "__SCRATCH__")`, fmt.Sprintf("(subpath %s) (subpath %s)", sbString(scratch), sbString(scratchReal)))
	out = strings.ReplaceAll(out, "__HOME__", strings.Trim(sbString(s.Home), `"`))
	if strings.Contains(out, "__") && regexp.MustCompile(`__[A-Z]+__`).MatchString(out) {
		return "", fmt.Errorf("sandbox profile %q has an unknown placeholder", profile)
	}
	return out, nil
}

// sbString quotes a string for SBPL.
func sbString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// Wrap returns the argv that runs argv under the rendered profile.
func Wrap(profileText string, argv []string) []string {
	return append([]string{SandboxExec, "-p", profileText}, argv...)
}

// Available reports whether sandbox-exec exists here.
func Available() bool {
	st, err := os.Stat(SandboxExec)
	return err == nil && !st.IsDir()
}
