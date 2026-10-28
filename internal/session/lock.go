package session

import (
	"fmt"
	"os"
	"path/filepath"
)

func canonicalSessionPath(path string) (string, error) {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// checkSessionLinks refuses a session file with multiple hard links.
// Continuation then needs a separate copy.
func checkSessionLinks(path string) error {
	n, err := hardLinkCount(path)
	if err != nil {
		return err
	}
	if n > 1 {
		return fmt.Errorf("session has multiple hard links; continue a separate copy instead")
	}
	return nil
}

// sessionLock is an advisory exclusive lock for one psi writer.
// Pi does not honor this lock; concurrent Pi+psi writes are out of scope
// (documented in COMPATIBILITY.md). Two psi processes must not mutate one
// session concurrently.
type sessionLock struct {
	f *os.File
}

// acquireLock opens (creating) path+".lock" and takes an exclusive,
// non-blocking lock.
func acquireLock(sessionFile string) (*sessionLock, error) {
	if sessionFile == "" {
		return nil, nil
	}
	lockPath := sessionFile + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open lock %s: %w", lockPath, err)
	}
	if err := lockExclusive(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("session is locked by another psi process: %s", sessionFile)
	}
	return &sessionLock{f: f}, nil
}

func (l *sessionLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlockExclusive(l.f)
	_ = l.f.Close()
}
