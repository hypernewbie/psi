//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func secretStoreDatabasePrivate(path string, before os.FileInfo) bool {
	_, localPath := windowsSecretPathVolume(path)
	if before == nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || !localPath {
		return false
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	info, ok := windowsSecretFileInfo(handle)
	if !ok || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	opened, err := os.Stat(path)
	if err != nil || !os.SameFile(before, opened) || !windowsLocalSecretPath(path, handle) {
		return false
	}
	acl, err := windowsDescriptorFacts(handle)
	return err == nil && windowsSecretACLPrivate(acl)
}
