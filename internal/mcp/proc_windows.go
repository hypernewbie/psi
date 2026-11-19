//go:build windows

package mcp

import (
	"os/exec"
)

// prepareCommand has no process-group equivalent on Windows. psi keeps the
// documented leader-only cleanup behavior.
func prepareCommand(cmd *exec.Cmd) {}

func signalCommand(cmd *exec.Cmd, force bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func terminateCommand(cmd *exec.Cmd) { signalCommand(cmd, true) }
