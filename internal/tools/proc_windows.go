//go:build windows

package tools

import (
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"

	"github.com/hypernewbie/psi/internal/shellutil"
)

type processGroup struct {
	job    windows.Handle
	killed bool
}

func setNewProcessGroup(cmd *exec.Cmd) {}

// attachProcessGroup places the started shell in a Windows Job Object so
// timeout and cancellation can terminate the whole process tree immediately
// without waiting for an external taskkill.exe scan.
func attachProcessGroup(cmd *exec.Cmd) *processGroup {
	pg := &processGroup{}
	if cmd == nil || cmd.Process == nil {
		return pg
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return pg
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return pg
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return pg
	}
	pg.job = job
	return pg
}

func closeProcessGroup(pg *processGroup) {
	if pg != nil && pg.job != 0 {
		_ = windows.CloseHandle(pg.job)
		pg.job = 0
	}
}

func shellExitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	return state.ExitCode()
}

// killProcessGroup terminates the job object (leader and all descendants) on
// Windows, falling back to Pi's System32 taskkill.exe and Process.Kill if no
// job object was attached.
func killProcessGroup(cmd *exec.Cmd, pg *processGroup) {
	if pg != nil {
		if pg.killed {
			return
		}
		pg.killed = true
		if pg.job != 0 {
			if err := windows.TerminateJobObject(pg.job, 1); err == nil {
				return
			}
		}
	}
	if cmd == nil || cmd.Process == nil {
		return
	}
	kill := exec.Command(shellutil.TaskkillPath(), "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
	shellutil.HideWindow(kill)
	if err := kill.Start(); err != nil {
		_ = cmd.Process.Kill()
		return
	}
	_ = kill.Process.Release()
}
