//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

// Shell commands run in their own process group so cancellation and
// timeouts reach descendants, not just the leader.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
