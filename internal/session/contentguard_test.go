package session

// PLAN3 F04 hardening: the append barrier's stat triple (size, identity,
// mtime) is blind to a same-size external rewrite that lands inside one
// inode-timestamp tick on a coarse-clock filesystem. The byte-level guard
// closes that window. These tests force the guard state directly so they
// behave identically on fine- and coarse-grained clocks.

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// enableGuard mimics what OpenFile does on a coarse-clock filesystem: the
// store tracks the exact file bytes it believes are on disk.
func enableGuard(t *testing.T, s *Store) {
	t.Helper()
	raw, err := os.ReadFile(s.file)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(s.file)
	if err != nil || int64(len(raw)) != st.Size() {
		t.Fatal("baseline must capture the exact file bytes")
	}
	s.contentGuard = true
	s.contentBaseline = raw
}

// rewriteInvisible replaces bbbb with BBBB (same size) and restores the
// pre-rewrite mtime, so size, identity, and mtime all look unchanged to the
// stat triple: the worst case a coarse tick produces.
func rewriteInvisible(t *testing.T, path string) {
	t.Helper()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSecondLineSameID(path)
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
}

// A rewrite invisible to the stat triple must still be refused: the bytes on
// disk no longer match what the store last wrote.
func TestAppendRefusesInvisibleSameSizeRewrite(t *testing.T) {
	path := twoMessageFile(t)
	s := openPersisted(t, path)
	enableGuard(t, s)
	rewriteInvisible(t, path)
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "after"}})
	if err == nil || !strings.Contains(err.Error(), "changed externally") {
		t.Fatalf("stat-invisible rewrite accepted: %v", err)
	}
	if s.LeafID() != "u2" {
		t.Fatalf("leaf moved on refused append: %q", s.LeafID())
	}
	disk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(disk, rewritten) {
		t.Fatalf("refused append wrote beyond the external rewrite: %v", err)
	}
}

// An intact file must pass the guard, and the tracked baseline must keep
// matching the file across successive appends.
func TestAppendSucceedsWithGuardWhenFileUnchanged(t *testing.T) {
	path := twoMessageFile(t)
	s := openPersisted(t, path)
	enableGuard(t, s)

	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "first"}}); err != nil {
		t.Fatalf("unmodified file refused: %v", err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}},
	}}); err != nil {
		t.Fatalf("second append refused after the baseline was extended: %v", err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s.contentBaseline, disk) {
		t.Fatal("baseline no longer matches the file after appends")
	}
}

// Once the file outgrows the size a within-tick rewrite can span, the guard
// stops tracking (bounded memory) and appends continue.
func TestGuardStopsTrackingBeyondLimit(t *testing.T) {
	path := twoMessageFile(t)
	s := openPersisted(t, path)
	enableGuard(t, s)
	s.contentGuardLimit = int64(len(s.contentBaseline)) + 10

	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "growth across the tracking bound"}}); err != nil {
		t.Fatalf("append across the bound failed: %v", err)
	}
	if s.contentGuard || s.contentBaseline != nil {
		t.Fatalf("guard must stop tracking beyond the bound: guard=%v baseline=%d bytes", s.contentGuard, len(s.contentBaseline))
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "still fine"}}); err != nil {
		t.Fatalf("append after untracking failed: %v", err)
	}
}

// A brand-new persisted session starts tracking at first flush and catches a
// stat-invisible rewrite of the file it just created.
func TestNewSessionGuardCatchesInvisibleRewrite(t *testing.T) {
	dir := t.TempDir()
	s, err := New(OpenOptions{Cwd: "/tmp/ws", SessionDir: dir, Persist: true, ID: "guard-new"})
	if err != nil {
		t.Fatal(err)
	}
	s.clockCoarse = true
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("first flush failed: %v", err)
	}
	if !s.contentGuard || s.contentBaseline == nil {
		t.Fatalf("first flush did not start tracking: guard=%v", s.contentGuard)
	}
	disk, err := os.ReadFile(s.file)
	if err != nil || !bytes.Equal(disk, s.contentBaseline) {
		t.Fatalf("baseline does not match the flushed file: %v", err)
	}

	before, err := os.Stat(s.file)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.file)
	for i, b := range data {
		if b == 'h' {
			data[i] = 'H' // hello -> H ello: same size, different bytes
		}
	}
	if err := os.WriteFile(s.file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(s.file, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}},
	}}); err == nil || !strings.Contains(err.Error(), "changed externally") {
		t.Fatalf("stat-invisible rewrite of a new session accepted: %v", err)
	}
}

// A probe that cannot inspect the directory must fail safe: treat the clock
// as coarse so the guard stays on.
func TestClockProbeFailsSafe(t *testing.T) {
	if !probeClockGranularity("/nonexistent/psi-probe-dir") {
		t.Fatal("probe must fail safe (coarse) when the directory is unavailable")
	}
}
