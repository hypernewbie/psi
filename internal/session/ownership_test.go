package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if !bytes.Equal(after, prefix) || info.Mode().Perm() != 0400 || s.LeafID() != leaf || s.WriteError() == nil {
		t.Fatal("permission failure changed bytes, mode, or logical state")
	}
}
