//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func windowsSecretACLCommandSafe(acl windowsSecretACL) bool {
	if !acl.local || !acl.present || !acl.complete || acl.owner == "" || acl.user == "" {
		return false
	}
	trusted := func(sid string) bool {
		return strings.EqualFold(sid, acl.user) || strings.EqualFold(sid, windowsSystemSID) ||
			strings.EqualFold(sid, windowsAdministratorsSID) || (acl.appContainer != "" && strings.EqualFold(sid, acl.appContainer))
	}
	if !trusted(acl.owner) {
		return false
	}
	for _, ace := range acl.entries {
		if ace.sid == "" || ace.flags & ^uint8(windowsACEKnownFlags) != 0 || (ace.kind != windowsACEAllow && ace.kind != windowsACEDeny) {
			return false
		}
		if ace.kind == windowsACEAllow && ace.flags&windowsACEInheritOnly == 0 && !trusted(ace.sid) && ace.mask&windowsSecretWriteRights != 0 {
			return false
		}
	}
	return true
}

func windowsExecCommandFileInfo(handle windows.Handle) bool {
	kind, err := windows.GetFileType(handle)
	if err != nil || kind != windows.FILE_TYPE_DISK {
		return false
	}
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &info) != nil {
		return false
	}
	return info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) == 0
}

func validateSecretExecCommand(command string, trustedDirs []string) error {
	if !filepath.IsAbs(command) || filepath.Clean(command) != command {
		return fmt.Errorf("command path must be absolute and clean")
	}
	if _, ok := windowsSecretPathVolume(command); !ok {
		return fmt.Errorf("command path is not local")
	}
	before, err := os.Lstat(command)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("command is unavailable")
	}
	name, err := windows.UTF16PtrFromString(command)
	if err != nil {
		return fmt.Errorf("command is unavailable")
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("command is unavailable")
	}
	defer windows.CloseHandle(handle)
	if !windowsExecCommandFileInfo(handle) || !windowsLocalSecretPath(command, handle) {
		return fmt.Errorf("command is unavailable")
	}
	opened, err := os.Stat(command)
	if err != nil || !os.SameFile(before, opened) {
		return fmt.Errorf("command changed during validation")
	}
	acl, err := windowsDescriptorFacts(handle)
	if err != nil || !windowsSecretACLCommandSafe(acl) {
		return fmt.Errorf("command permissions are unavailable or unsafe")
	}
	resolved, err := filepath.EvalSymlinks(command)
	if err != nil {
		return fmt.Errorf("command is unavailable")
	}
	if len(trustedDirs) > 0 {
		within := false
		for _, directory := range trustedDirs {
			if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
				return fmt.Errorf("trusted directory is invalid")
			}
			resolvedDirectory, err := filepath.EvalSymlinks(directory)
			if err == nil && pathWithinWindows(resolvedDirectory, resolved) {
				within = true
				break
			}
		}
		if !within {
			return fmt.Errorf("command is outside trusted directories")
		}
	}
	return nil
}

func pathWithinWindows(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func startSecretExec(ctx context.Context, command string, args, env []string, request []byte) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = filepath.Dir(command)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(request)
	return cmd
}

func stopSecretExec(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		_ = cmd.Process.Kill()
		return
	}
	systemRoot := filepath.Dir(systemDirectory)
	taskkill := filepath.Join(systemDirectory, "taskkill.exe")
	pid := fmt.Sprintf("%d", cmd.Process.Pid)
	terminateTree := func(force bool) {
		args := []string{"/PID", pid, "/T"}
		if force {
			args = append(args, "/F")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stop := exec.CommandContext(ctx, taskkill, args...)
		stop.Dir = systemDirectory
		stop.Env = []string{"SystemRoot=" + systemRoot}
		stop.Stdout, stop.Stderr = io.Discard, io.Discard
		_ = stop.Run()
	}
	terminateTree(false)
	time.Sleep(secretExecStopGracePeriod)
	terminateTree(true)
	_ = cmd.Process.Kill()
}
