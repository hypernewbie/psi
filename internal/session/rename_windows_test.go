//go:build windows

package session

import (
	"os"
	"path/filepath"
	"testing"
)

// A session file named after a Win32 device name (con.jsonl) resolves to a
// device, so a legacy session with such an ID keeps its current name.
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
