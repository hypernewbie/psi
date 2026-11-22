//go:build linux || darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// prepareExecCommand runs the command through the system shell in its own
// process group. Only PATH and the configured passEnv entries cross into the
// child environment.
func prepareExecCommand(ctx context.Context, workspace, command string, limits execLimits) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = workspace
	cmd.Env = execChildEnv(os.Environ(), limits)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// newExecKiller returns the function that kills the command and every
// process it started.
func newExecKiller(cmd *exec.Cmd) func() {
	return func() { killProcessGroup(cmd) }
}

// killProcessGroup kills the command and every process it started.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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
