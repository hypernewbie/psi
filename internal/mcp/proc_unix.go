//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// prepareCommand starts the child in its own process group so cleanup can
// signal the owned group and not the psi process.
func prepareCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func signalCommand(cmd *exec.Cmd, force bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	// The negative PID targets the owned process group.
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

func terminateCommand(cmd *exec.Cmd) { signalCommand(cmd, true) }
