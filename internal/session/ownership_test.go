package session

import (
	"bufio"
	"bytes"
	"errors"
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
	path := filepath.Join(root, "pi-sess-1.jsonl")
	piFixture(t, path)
	alias := filepath.Join(root, "alias.jsonl")
	if err := os.Symlink("pi-sess-1.jsonl", alias); err != nil {
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
	path := filepath.Join(root, "pi-sess-1.jsonl")
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
	path := filepath.Join(root, "pi-sess-1.jsonl")
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
	path := filepath.Join(root, "pi-sess-1.jsonl")
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
	targetPath := filepath.Join(root, "pi-sess-1.jsonl")
	piFixture(t, targetPath)
	symlinkPath := filepath.Join(root, "symlink.jsonl")
	if err := os.Symlink("pi-sess-1.jsonl", symlinkPath); err != nil {
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
	path := filepath.Join(root, "pi-sess-1.jsonl")
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

func TestTryLockCanonicalizesLegacySession(t *testing.T) {
	dir := t.TempDir()
	s, err := New(OpenOptions{Cwd: "/tmp/work", SessionDir: dir, Persist: true, ID: "legacy-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "before rename"}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.File())
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(s.Dir(), "legacy-name.jsonl")
	if err := os.Rename(s.File(), legacy); err != nil {
		t.Fatal(err)
	}

	// The --no-session path never renames.
	ro, err := OpenFile(legacy, "/tmp/work", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.TryLock(); err != nil {
		t.Fatal(err)
	}
	ro.Unlock()
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("non-persisted lock moved the source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ro.Dir(), "legacy-1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("non-persisted lock created a canonical file, stat err = %v", err)
	}

	reopened, err := OpenFile(legacy, "/tmp/work", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.TryLock(); err != nil {
		t.Fatal(err)
	}
	defer reopened.Unlock()
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy path still present, stat err = %v", err)
	}
	canonical := filepath.Join(reopened.Dir(), "legacy-1.jsonl")
	if reopened.File() != canonical {
		t.Fatalf("store file = %s, want %s", reopened.File(), canonical)
	}
	after, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rename changed session bytes")
	}

	// Lock handoff: the source lock is released, the target lock retained.
	srcLock, err := acquireLock(legacy)
	if err != nil {
		t.Fatalf("source lock still held: %v", err)
	}
	srcLock.Release()
	if dstLock, err := acquireLock(canonical); err == nil {
		dstLock.Release()
		t.Fatal("store did not retain the canonical lock")
	}

	if _, err := reopened.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "after rename"}}); err != nil {
		t.Fatal(err)
	}
	reopened.Unlock()
	final, err := OpenFile(canonical, "/tmp/work", true)
	if err != nil {
		t.Fatal(err)
	}
	proj, err := final.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Messages) != 2 {
		t.Fatalf("expected 2 messages after rename, got %d", len(proj.Messages))
	}

	t.Run("Collision", func(t *testing.T) {
		dir := t.TempDir()
		other := t.TempDir()
		owner, err := New(OpenOptions{Cwd: "/tmp/work", SessionDir: dir, Persist: true, ID: "collide-1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "owner"}}); err != nil {
			t.Fatal(err)
		}
		ownerBytes, err := os.ReadFile(owner.File())
		if err != nil {
			t.Fatal(err)
		}
		intruder, err := New(OpenOptions{Cwd: "/tmp/work", SessionDir: other, Persist: true, ID: "collide-1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := intruder.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "intruder"}}); err != nil {
			t.Fatal(err)
		}
		intruderBytes, err := os.ReadFile(intruder.File())
		if err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(owner.Dir(), "legacy-intruder.jsonl")
		if err := os.Rename(intruder.File(), legacy); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenFile(legacy, "/tmp/work", true)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Unlock()
		if err := reopened.TryLock(); err == nil {
			t.Fatal("expected a collision error")
		}
		afterOwner, err := os.ReadFile(owner.File())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ownerBytes, afterOwner) {
			t.Fatal("collision modified the target")
		}
		afterLegacy, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(intruderBytes, afterLegacy) {
			t.Fatal("collision modified the source")
		}
		if reopened.File() != legacy {
			t.Fatalf("store file = %s, want retained source %s", reopened.File(), legacy)
		}
		srcLock, err := acquireLock(legacy)
		if err != nil {
			t.Fatalf("source lock leaked: %v", err)
		}
		srcLock.Release()
		dstLock, err := acquireLock(filepath.Join(owner.Dir(), "collide-1.jsonl"))
		if err != nil {
			t.Fatalf("target lock leaked: %v", err)
		}
		dstLock.Release()
	})
}

func TestTryLockRejectsUnsafeLegacyID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evil.jsonl")
	data := "{\"type\":\"session\",\"version\":3,\"id\":\"../escape\",\"timestamp\":\"2026-01-01T00:00:00.000Z\",\"cwd\":\"/tmp/work\"}\n" +
		"{\"type\":\"message\",\"id\":\"u1\",\"parentId\":null,\"timestamp\":\"2026-01-01T00:00:01.000Z\",\"message\":{\"role\":\"user\",\"content\":\"hi\",\"timestamp\":1}}\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenFile(path, "/tmp/work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	if err := s.TryLock(); err == nil {
		t.Fatal("expected an unsafe-ID error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != data {
		t.Fatal("rejected source was modified")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.Dir()), "escape.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("escape file appeared in the parent directory, stat err = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one directory entry, found %d", len(entries))
	}
}

func TestTryLockLegacyLockExclusion(t *testing.T) {
	setup := func(t *testing.T) (string, *Store) {
		t.Helper()
		dir := t.TempDir()
		s, err := New(OpenOptions{Cwd: "/tmp/work", SessionDir: dir, Persist: true, ID: "lock-excl"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "seed"}}); err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(s.Dir(), "legacy-excl.jsonl")
		if err := os.Rename(s.File(), legacy); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenFile(legacy, "/tmp/work", true)
		if err != nil {
			t.Fatal(err)
		}
		return legacy, reopened
	}
	t.Run("SourceLocked", func(t *testing.T) {
		legacy, reopened := setup(t)
		defer reopened.Unlock()
		before, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatal(err)
		}
		held, err := acquireLock(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err := reopened.TryLock(); err == nil {
			t.Fatal("TryLock succeeded while the source lock was held")
		}
		held.Release()
		after, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("failed activation modified the source")
		}
		if _, err := os.Stat(filepath.Join(reopened.Dir(), "lock-excl.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("canonical target appeared, stat err = %v", err)
		}
		if err := reopened.TryLock(); err != nil {
			t.Fatalf("retry after source release failed: %v", err)
		}
	})
	t.Run("DestinationLocked", func(t *testing.T) {
		legacy, reopened := setup(t)
		defer reopened.Unlock()
		before, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatal(err)
		}
		held, err := acquireLock(filepath.Join(reopened.Dir(), "lock-excl.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if err := reopened.TryLock(); err == nil {
			held.Release()
			t.Fatal("TryLock succeeded while the destination lock was held")
		}
		held.Release()
		after, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("failed activation modified the source")
		}
		if err := reopened.TryLock(); err != nil {
			t.Fatalf("retry after destination release failed: %v", err)
		}
	})
}

func TestTryLockRejectsLegacySourceChanges(t *testing.T) {
	t.Run("Replacement", func(t *testing.T) {
		dir := t.TempDir()
		s, err := New(OpenOptions{Cwd: "/tmp/work", SessionDir: dir, Persist: true, ID: "replace-1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "original"}}); err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(s.Dir(), "legacy-replace.jsonl")
		if err := os.Rename(s.File(), legacy); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenFile(legacy, "/tmp/work", true)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Unlock()
		staging := filepath.Join(s.Dir(), "staging.jsonl")
		if err := os.WriteFile(staging, []byte("replacement-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(legacy); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(staging, legacy); err != nil {
			t.Fatal(err)
		}
		if err := reopened.TryLock(); err == nil {
			t.Fatal("TryLock accepted a replaced source")
		}
		after, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != "replacement-bytes" {
			t.Fatal("rejected replacement was modified")
		}
		if _, err := os.Stat(filepath.Join(reopened.Dir(), "replace-1.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("canonical target appeared, stat err = %v", err)
		}
	})
	t.Run("NewHardLink", func(t *testing.T) {
		dir := t.TempDir()
		s, err := New(OpenOptions{Cwd: "/tmp/work", SessionDir: dir, Persist: true, ID: "link-1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "seed"}}); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(s.File())
		if err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(s.Dir(), "legacy-link.jsonl")
		if err := os.Rename(s.File(), legacy); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenFile(legacy, "/tmp/work", true)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Unlock()
		link := filepath.Join(s.Dir(), "alias-link.jsonl")
		if err := os.Link(legacy, link); err != nil {
			t.Fatal(err)
		}
		if err := reopened.TryLock(); err == nil {
			t.Fatal("TryLock accepted a newly linked source")
		}
		if _, err := os.Stat(filepath.Join(reopened.Dir(), "link-1.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("canonical target appeared, stat err = %v", err)
		}
		for _, p := range []string{legacy, link} {
			content, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, content) {
				t.Fatalf("rejected link path %s was modified", p)
			}
		}
	})
}

func TestTryLockKeepsSymlinkedSessionName(t *testing.T) {
	dir := t.TempDir()
	s, err := New(OpenOptions{Cwd: "/tmp/work", SessionDir: dir, Persist: true, ID: "linked-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.File())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(s.Dir(), "real-legacy.jsonl")
	if err := os.Rename(s.File(), real); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(s.Dir(), "alias.jsonl")
	if err := os.Symlink("real-legacy.jsonl", alias); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFile(alias, "/tmp/work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Unlock()
	if err := reopened.TryLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(reopened.Dir(), "linked-1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("symlinked session was renamed, stat err = %v", err)
	}
	after, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("symlinked session bytes changed")
	}
	viaAlias, err := os.ReadFile(alias)
	if err != nil {
		t.Fatalf("alias stopped resolving: %v", err)
	}
	if !bytes.Equal(before, viaAlias) {
		t.Fatal("alias no longer points at the session")
	}
}

func TestTryLockKeepsReservedDeviceName(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy-con.jsonl")
	data := "{\"type\":\"session\",\"version\":3,\"id\":\"con\",\"timestamp\":\"2026-01-01T00:00:00.000Z\",\"cwd\":\"/tmp/work\"}\n" +
		"{\"type\":\"message\",\"id\":\"u1\",\"parentId\":null,\"timestamp\":\"2026-01-01T00:00:01.000Z\",\"message\":{\"role\":\"user\",\"content\":\"hi\",\"timestamp\":1}}\n"
	if err := os.WriteFile(legacy, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenFile(legacy, "/tmp/work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	if err := s.TryLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "con.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("reserved device name was created, stat err = %v", err)
	}
	after, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != data {
		t.Fatal("reserved-name session was modified")
	}
}

func TestTryLockSyncFailureRetainsCanonicalRecovery(t *testing.T) {
	injected := errors.New("injected sync failure")
	setup := func(t *testing.T) *Store {
		t.Helper()
		dir := t.TempDir()
		s, err := New(OpenOptions{Cwd: "/tmp/work", SessionDir: dir, Persist: true, ID: "sync-fail"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "seed"}}); err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(s.Dir(), "legacy-sync.jsonl")
		if err := os.Rename(s.File(), legacy); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenFile(legacy, "/tmp/work", true)
		if err != nil {
			t.Fatal(err)
		}
		return reopened
	}
	t.Run("PlainFailure", func(t *testing.T) {
		reopened := setup(t)
		defer reopened.Unlock()
		legacy := reopened.File()
		reopened.storage = &storageOps{syncDir: func(string) error { return injected }}
		if err := reopened.TryLock(); !errors.Is(err, injected) {
			t.Fatalf("TryLock error = %v, want injected failure", err)
		}
		canonical := filepath.Join(reopened.Dir(), "sync-fail.jsonl")
		if reopened.File() != canonical {
			t.Fatalf("store file = %s, want %s", reopened.File(), canonical)
		}
		srcLock, err := acquireLock(legacy)
		if err != nil {
			t.Fatalf("source lock leaked: %v", err)
		}
		srcLock.Release()
		dstLock, err := acquireLock(canonical)
		if err != nil {
			t.Fatalf("target lock leaked: %v", err)
		}
		dstLock.Release()
		reopened.storage = nil
		if err := reopened.TryLock(); err != nil {
			t.Fatalf("explicit retry failed: %v", err)
		}
		if _, err := reopened.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "after retry"}}); err != nil {
			t.Fatalf("append after retry failed: %v", err)
		}
	})
	t.Run("ExternalGrowthDuringFailure", func(t *testing.T) {
		reopened := setup(t)
		defer reopened.Unlock()
		reopened.storage = &storageOps{syncDir: func(string) error {
			f, err := os.OpenFile(filepath.Join(reopened.Dir(), "sync-fail.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, werr := f.WriteString("{\"type\":\"external-append\"}\n")
			cerr := f.Close()
			if werr != nil {
				return werr
			}
			if cerr != nil {
				return cerr
			}
			return injected
		}}
		if err := reopened.TryLock(); !errors.Is(err, injected) {
			t.Fatalf("TryLock error = %v, want injected failure", err)
		}
		reopened.storage = nil
		if err := reopened.TryLock(); err != nil {
			t.Fatalf("explicit retry failed: %v", err)
		}
		_, err := reopened.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "late"}})
		if err == nil || !strings.Contains(err.Error(), "changed externally") {
			t.Fatalf("append accepted external growth, err = %v", err)
		}
	})
}
