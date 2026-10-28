//go:build unix

package session

import (
	"os"
	"syscall"
)

func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// probeSharedLock reports whether another process holds the advisory lock
// for the session at path. A missing lock file counts as unlocked.
func probeSharedLock(path string) (bool, error) {
	lock, err := os.Open(path + ".lock")
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return true, nil
	}
	return false, nil
}

func hardLinkCount(path string) (int, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	return int(st.Nlink), nil
}
