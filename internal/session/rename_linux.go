//go:build linux

package session

import "golang.org/x/sys/unix"

// renameNoReplace moves oldPath to newPath without replacing an existing
// target. A collision returns an error and leaves both files intact.
func renameNoReplace(oldPath, newPath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
}
