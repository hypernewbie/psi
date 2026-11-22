//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func secretRegularSingleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && stat.Nlink == 1
}

// privateSecretIdentity verifies the opened regular file, not just its path.
func privateSecretIdentity(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && secretRegularSingleLink(info) && info.Mode().Perm()&0o077 == 0 && stat.Uid == uint32(os.Geteuid())
}

// readPrivateSecretFile transfers ownership of the returned buffer to its
// caller. On timeout, the reader retains ownership until it can wipe the data.
func readPrivateSecretFile(path string, maxBytes int64, timeout time.Duration) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("secret file is unavailable")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("secret file is unavailable")
	}
	file := os.NewFile(uintptr(fd), "secret file")
	if file == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("secret file is unavailable")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !secretRegularSingleLink(opened) || opened.Size() > maxBytes {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, current) || !secretRegularSingleLink(current) {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil || !os.SameFile(opened, resolvedInfo) || !secretRegularSingleLink(resolvedInfo) || !privateSecretIdentity(opened) {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	return readSecretHandle(file, maxBytes, timeout, func() bool {
		final, err := file.Stat()
		return err == nil && os.SameFile(opened, final) && secretRegularSingleLink(final)
	})
}
