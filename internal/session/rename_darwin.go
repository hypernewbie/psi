//go:build darwin

package session

import "golang.org/x/sys/unix"

// renameNoReplace moves oldPath to newPath without replacing an existing
// target. A collision returns an error and leaves both files intact.
func renameNoReplace(oldPath, newPath string) error {
	return unix.RenamexNp(oldPath, newPath, unix.RENAME_EXCL)
}
