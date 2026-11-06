//go:build windows

package session

import "golang.org/x/sys/windows"

// renameNoReplace moves oldPath to newPath without replacing an existing
// target. A collision returns an error and leaves both files intact.
func renameNoReplace(oldPath, newPath string) error {
	from, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	return windows.MoveFile(from, to)
}
