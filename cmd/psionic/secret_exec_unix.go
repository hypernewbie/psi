//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func validateSecretExecCommand(command string, trustedDirs []string) error {
	if !filepath.IsAbs(command) || filepath.Clean(command) != command {
		return fmt.Errorf("command path must be absolute and clean")
	}
	info, err := os.Lstat(command)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("command is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("command is not trusted")
	}
	resolved, err := filepath.EvalSymlinks(command)
	if err != nil {
		return fmt.Errorf("command path is unavailable")
	}
	for _, dir := range trustedDirs {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			return fmt.Errorf("trusted directory is invalid")
		}
	}
	if len(trustedDirs) > 0 {
		trusted := false
		for _, dir := range trustedDirs {
			resolvedDir, err := filepath.EvalSymlinks(dir)
			if err == nil && pathWithin(resolvedDir, resolved) {
				trusted = true
				break
			}
		}
		if !trusted {
			return fmt.Errorf("command is outside trusted directories")
		}
	}
	return nil
}

func startSecretExec(ctx context.Context, command string, args, env []string, request []byte) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = filepath.Dir(command)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(request)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func stopSecretExec(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(secretExecStopGracePeriod)
	defer timer.Stop()
	<-timer.C
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
