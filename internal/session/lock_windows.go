//go:build windows

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockExclusive(f *os.File) error {
	var ov windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ov)
}

func unlockExclusive(f *os.File) error {
	var ov windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ov)
}

// probeSharedLock reports whether another process holds the advisory lock
// for the session at path. A missing lock file counts as unlocked.
func probeSharedLock(path string) (bool, error) {
	f, err := os.Open(path + ".lock")
	if err != nil {
		return false, err
	}
	defer f.Close()
	var ov windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ov)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func hardLinkCount(path string) (int, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return 0, err
	}
	return int(info.NumberOfLinks), nil
}
