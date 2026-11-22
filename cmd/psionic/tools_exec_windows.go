//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// execWaitDelay bounds how long the wait for a killed command may block on
// pipes that a surviving grandchild still holds.
const execWaitDelay = 2 * time.Second

// prepareExecCommand runs the command through cmd.exe in its own process
// group. Only PATH and the configured passEnv entries cross into the child
// environment.
func prepareExecCommand(ctx context.Context, workspace, command string, limits execLimits) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", command)
	cmd.Dir = workspace
	cmd.Env = execChildEnv(os.Environ(), limits)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	// A killed command can leave a grandchild holding the output pipes.
	// The wait is bounded so a turn cannot hang on them.
	cmd.WaitDelay = execWaitDelay
	return cmd
}

// newExecKiller returns the function that kills the command and every
// process it started.
func newExecKiller(cmd *exec.Cmd) func() {
	return newJobKiller(cmd)
}

// newJobKiller puts the command in a job object whose children die with it.
// Windows has no process group that can be signalled, so the job object is
// the equivalent. When the job cannot be created, the fallback kills the
// tree with taskkill.
func newJobKiller(cmd *exec.Cmd) func() {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return taskkillKiller(cmd)
	}
	defer windows.CloseHandle(handle)
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return taskkillKiller(cmd)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		windows.CloseHandle(job)
		return taskkillKiller(cmd)
	}
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		windows.CloseHandle(job)
		return taskkillKiller(cmd)
	}
	return func() {
		// Terminating the job ends the command and every child it started.
		_ = windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
	}
}

// taskkillKiller kills the command and its children with taskkill. It is the
// fallback when the job object is unavailable.
func taskkillKiller(cmd *exec.Cmd) func() {
	return func() {
		if cmd.Process == nil {
			return
		}
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}

// execChildEnv keeps PATH plus the explicitly passed names. Everything else
// from this process's environment stays out of the child.
func execChildEnv(environ []string, limits execLimits) []string {
	out := []string{}
	for _, item := range environ {
		name, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if name == "PATH" || limits.passEnv[name] {
			out = append(out, item)
		}
	}
	return out
}
