//go:build windows

package tools

import (
	"os"
	"os/exec"
)

// Windows has no process groups. Cancellation and timeouts stop the
// leader only; descendants outlive the command. Tests that require
// group cleanup skip on Windows.
func setNewProcessGroup(cmd *exec.Cmd) {
}

func shellExitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	return state.ExitCode()
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
