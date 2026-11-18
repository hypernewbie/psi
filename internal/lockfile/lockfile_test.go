package lockfile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStaleRecoveryLeavesRegularFileUntouched(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "settings.json.lock")
	original := []byte("not a lock directory")
	if err := os.WriteFile(lockPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	recoverStaleLock(lockPath, time.Minute, time.Now())
	data, err := os.ReadFile(lockPath)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("stale recovery changed the regular file: contents=%q error=%v", data, err)
	}
}

func TestAcquireDoesNotAcceptOrRemoveReplacedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("moving an open lock directory is not supported on Windows")
	}
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "auth.json.lock")
	movedPath := filepath.Join(dir, "moved-lock")
	unlock, valid, err := Acquire(context.Background(), lockPath, Options{Heartbeat: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(lockPath, movedPath); err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := os.Symlink(movedPath, lockPath); err != nil {
		unlock()
		t.Fatal(err)
	}
	if valid() {
		t.Fatal("symlinked replacement still reports the original lease as valid")
	}
	unlock()
	info, err := os.Lstat(lockPath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unlock removed or changed the replacement symlink: info=%v error=%v", info, err)
	}
}

func TestAcquireFreshPiLockReturnsBusyWithoutChangingHolder(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "auth.json.lock")
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, valid, err := Acquire(context.Background(), lockPath, Options{
		StaleAfter: time.Minute,
		Heartbeat:  time.Hour,
		Wait:       time.Nanosecond,
	})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Acquire error = %v, want ErrBusy", err)
	}
	if unlock != nil || valid != nil {
		t.Fatal("failed acquisition returned lease functions")
	}
	info, err := os.Stat(lockPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("holder lock changed: info=%v error=%v", info, err)
	}
	entries, err := os.ReadDir(lockPath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("holder lock contents = %v, error = %v", entries, err)
	}
}
