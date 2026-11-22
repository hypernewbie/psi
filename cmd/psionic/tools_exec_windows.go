//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// startExecCommand runs the command through cmd.exe in its own process group.
// Only PATH and the configured passEnv entries cross into the child
// environment.
func startExecCommand(ctx context.Context, workspace, command string, limits execLimits) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", command)
	cmd.Dir = workspace
	cmd.Env = execChildEnv(os.Environ(), limits)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	return cmd
}

// stopExecCommand kills the shell and, best effort, its child processes with
// taskkill. cmd.exe does not propagate termination to its children, so a
// grandchild can outlive the kill; the tool's output stays bounded regardless.
func stopExecCommand(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
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
