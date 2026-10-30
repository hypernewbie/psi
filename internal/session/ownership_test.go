package session

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionSymlinkUsesTheSameWriterLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "original.jsonl")
	piFixture(t, path)
	alias := filepath.Join(root, "alias.jsonl")
	if err := os.Symlink("original.jsonl", alias); err != nil {
		t.Fatal(err)
	}
	first, err := OpenFile(path, "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.TryLock(); err != nil {
		t.Fatal(err)
	}
	defer first.Unlock()
	second, err := OpenFile(alias, "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Unlock()
	if first.File() != second.File() {
		t.Fatal("symlink did not resolve to the canonical session path")
	}
	if err := second.TryLock(); err == nil {
		t.Fatal("alias bypassed writer ownership")
	}
	prefix, _ := os.ReadFile(path)
	first.Unlock()
	if err := second.TryLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.AppendModelChange("anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.HasPrefix(after, prefix) {
		t.Fatal("symlink continuation replaced original bytes")
	}
	if info, err := os.Lstat(alias); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("continuation replaced the symlink")
	}
}

func TestHardLinkedSessionRefusesPersistentContinuation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "original.jsonl")
	piFixture(t, path)
	alias := filepath.Join(root, "hardlink.jsonl")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	prefix, _ := os.ReadFile(path)
	if _, err := OpenFile(alias, "/tmp/ws", true); err == nil || !strings.Contains(err.Error(), "multiple hard links") {
		t.Fatalf("unsafe linked writer accepted: %v", err)
	}
	if _, err := OpenFile(alias, "/tmp/ws", false); err != nil {
		t.Fatal("read-only linked history refused", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, prefix) {
		t.Fatal("refusal changed linked bytes")
	}
}

func TestReplacementBetweenGenerationCheckAndOpenIsRefused(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	piFixture(t, path)
	s, err := OpenFile(path, "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := os.ReadFile(path)
	leaf, count := s.LeafID(), s.EntryCount()
	s.storage = &storageOps{open: func(path string, flags int, mode os.FileMode) (durableFile, error) {
		if err := os.WriteFile(path+".new", prefix, mode); err != nil {
			return nil, err
		}
		if err := os.Rename(path+".new", path); err != nil {
			return nil, err
		}
		return os.OpenFile(path, flags, mode)
	}}
	if _, err := s.AppendModelChange("anthropic", "m"); err == nil {
		t.Fatal("replacement raced the pre-open generation check")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, prefix) || s.LeafID() != leaf || s.EntryCount() != count {
		t.Fatal("refused replacement received a logical or physical append")
	}
}

func TestReadOnlySessionAppendPreservesBytesAndMode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary file permission checks")
	}
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	piFixture(t, path)
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	s, err := OpenFile(path, "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := os.ReadFile(path)
	leaf := s.LeafID()
	if _, err := s.AppendModelChange("anthropic", "m"); err == nil {
		t.Fatal("read-only file accepted an append")
	}
	after, _ := os.ReadFile(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix mode bits: read-only files stat as 0444.
	wantMode := os.FileMode(0400)
	if runtime.GOOS == "windows" {
		wantMode = 0444
	}
	if !bytes.Equal(after, prefix) || info.Mode().Perm() != wantMode || s.LeafID() != leaf || s.WriteError() == nil {
		t.Fatal("permission failure changed bytes, mode, or logical state")
	}
}

// Subprocess helper for cross-process advisory lock tests.
func TestSessionLockHelperProcess(t *testing.T) {
	if os.Getenv("PSI_SESSION_LOCK_HELPER") != "1" {
		return
	}
	sessionFile := os.Getenv("PSI_SESSION_LOCK_FILE")
	if sessionFile == "" {
		fmt.Fprintln(os.Stderr, "missing PSI_SESSION_LOCK_FILE")
		os.Exit(2)
	}
	s, err := OpenFile(sessionFile, "/tmp", true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "OpenFile failed: %v\n", err)
		os.Exit(1)
	}
	if err := s.TryLock(); err != nil {
		fmt.Fprintf(os.Stderr, "TryLock failed: %v\n", err)
		os.Exit(1)
	}
	// Signal to parent that the lock is held.
	fmt.Println("LOCKED")
	_ = os.Stdout.Sync()

	// Keep lock held until parent kills us or EOF on stdin.
	var buf [1]byte
	_, _ = os.Stdin.Read(buf[:])
	os.Exit(0)
}

func TestTryLockAndUnlockBasics(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	piFixture(t, path)

	store, err := OpenFile(path, "/tmp", true)
	if err != nil {
		t.Fatal(err)
	}

	// 1. First TryLock succeeds
	if err := store.TryLock(); err != nil {
		t.Fatalf("TryLock failed: %v", err)
	}

	// 2. Second TryLock on same store returns "already locked"
	if err := store.TryLock(); err == nil || !strings.Contains(err.Error(), "already locked") {
		t.Fatalf("expected 'already locked' error, got: %v", err)
	}

	// 3. TryLock on second store fails with locked by another process
	store2, err := OpenFile(path, "/tmp", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store2.TryLock(); err == nil || !strings.Contains(err.Error(), "locked by another psi process") {
		t.Fatalf("expected 'locked by another psi process', got: %v", err)
	}

	// 4. Unlock releases the lock
	store.Unlock()

	// 5. Multiple Unlock calls are safe (idempotent, no panic)
	store.Unlock()
	store.Unlock()

	// 6. Now store2 can acquire lock
	if err := store2.TryLock(); err != nil {
		t.Fatalf("store2 failed to acquire lock after store unlocked: %v", err)
	}
	store2.Unlock()

	// 7. In-memory store (Persist=false) TryLock is a no-op returning nil
	memStore, err := New(OpenOptions{Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := memStore.TryLock(); err != nil {
		t.Fatalf("in-memory TryLock should return nil, got %v", err)
	}
	memStore.Unlock()
}

func TestConcurrentTryLockUnlockAppend(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	piFixture(t, path)

	store, err := OpenFile(path, "/tmp", true)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var stop atomic.Bool

	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for !stop.Load() {
				if id%2 == 0 {
					if err := store.TryLock(); err == nil {
						_, _ = store.AppendMessage(AgentMessage{
							Role: "user",
							User: &UserMessage{Role: "user", Content: "concurrent append", Timestamp: 1},
						})
						store.Unlock()
					}
				} else {
					store.Unlock()
				}
			}
		}(i)
	}

	time.Sleep(200 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	store.Unlock()
}

func TestCrossProcessAdvisoryLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	piFixture(t, path)

	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLockHelperProcess$")
	cmd.Env = append(os.Environ(), "PSI_SESSION_LOCK_HELPER=1", "PSI_SESSION_LOCK_FILE="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "LOCKED" {
		t.Fatalf("expected LOCKED from child process, got %q (err: %v)", line, err)
	}

	// Parent attempts TryLock -> must fail with clear lock error
	parentStore, err := OpenFile(path, "/tmp", true)
	if err != nil {
		t.Fatal(err)
	}
	err = parentStore.TryLock()
	if err == nil {
		parentStore.Unlock()
		t.Fatal("parent acquired lock while child was holding it")
	}
	if !strings.Contains(err.Error(), "session is locked by another psi process") {
		t.Fatalf("unexpected lock error message: %v", err)
	}

	// Kill child process
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	// Now parent must be able to acquire lock cleanly
	if err := parentStore.TryLock(); err != nil {
		t.Fatalf("parent failed to acquire lock after child process was killed: %v", err)
	}
	defer parentStore.Unlock()

	// Verify append succeeds now that lock is held
	if _, err := parentStore.AppendModelChange("anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatalf("append failed after lock acquisition: %v", err)
	}
}

func TestCrossProcessAdvisoryLockWithSymlink(t *testing.T) {
	root := t.TempDir()
	targetPath := filepath.Join(root, "canonical.jsonl")
	piFixture(t, targetPath)
	symlinkPath := filepath.Join(root, "symlink.jsonl")
	if err := os.Symlink("canonical.jsonl", symlinkPath); err != nil {
		t.Fatal(err)
	}

	// Child locks the canonical path
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLockHelperProcess$")
	cmd.Env = append(os.Environ(), "PSI_SESSION_LOCK_HELPER=1", "PSI_SESSION_LOCK_FILE="+targetPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "LOCKED" {
		t.Fatalf("expected LOCKED from child, got %q (err: %v)", line, err)
	}

	// Parent opens symlink path and tries TryLock
	parentStore, err := OpenFile(symlinkPath, "/tmp", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := parentStore.TryLock(); err == nil {
		parentStore.Unlock()
		t.Fatal("parent acquired lock via symlink while child held canonical lock")
	}

	// Kill child
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	// Parent acquires via symlink
	if err := parentStore.TryLock(); err != nil {
		t.Fatalf("parent failed to acquire lock via symlink after child died: %v", err)
	}
	parentStore.Unlock()
}

func TestStaleLockFileAcquisition(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	piFixture(t, path)

	// Create a leftover .lock file on disk (from a crashed/dead process).
	lockPath := path + ".lock"
	if err := os.WriteFile(lockPath, []byte("stale-pid-12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := OpenFile(path, "/tmp", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TryLock(); err != nil {
		t.Fatalf("expected successful lock acquisition despite existing .lock file: %v", err)
	}
	store.Unlock()
}
