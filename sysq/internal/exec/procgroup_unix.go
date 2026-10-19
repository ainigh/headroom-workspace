//go:build unix

package exec

import (
	osexec "os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own group so a timeout kills
// helpers it spawned too.
func setProcessGroup(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
