//go:build unix

package tools

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

type processGroup struct{}

// Shell commands run in their own process group so cancellation and
// timeouts reach descendants, not just the leader.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachProcessGroup(*exec.Cmd) *processGroup { return nil }

func closeProcessGroup(*processGroup) {}

func shellExitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}

func killProcessGroup(cmd *exec.Cmd, _ *processGroup) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if runtime.GOOS == "darwin" {
		// macOS signals group members individually after releasing the group lock;
		// stopping the group first reduces the chance of `a; b` starting `b` during
		// cancellation (Pi #11252).
		_ = syscall.Kill(-pid, syscall.SIGSTOP)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
