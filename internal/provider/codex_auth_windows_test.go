//go:build windows

package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A concurrent Windows reader can keep a removed directory pending deletion.
// Until its last handle closes, Mkdir returns access denied, not already exists.
func pendingAuthLock(t *testing.T) (string, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json.lock")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(release)
	if err := windows.RemoveDirectory(name); err != nil {
		t.Fatal(err)
	}
	err = os.Mkdir(path, 0700)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("delete-pending directory did not produce access denied: %v", err)
	}
	return path, release
}

func TestAuthLockWaitsForWindowsPendingDeletion(t *testing.T) {
	path, release := pendingAuthLock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		unlock, valid, err := acquireAuthLock(ctx, path, 0, 0)
		if err == nil {
			if !valid() {
				err = errors.New("successor auth lease is invalid")
			}
			unlock()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("lock acquisition returned before deletion completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("lock acquisition did not recover after deletion completed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("successor auth lease was not removed: %v", err)
	}
}

func TestAuthLockCancelsDuringWindowsPendingDeletion(t *testing.T) {
	path, _ := pendingAuthLock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	unlock, _, err := acquireAuthLock(ctx, path, 0, 0)
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting for deletion ignored cancellation: %v", err)
	}
}
