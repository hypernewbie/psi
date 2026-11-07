package session

// Phase C regressions for PLAN3 F04: a session is loaded once, like Pi's
// session manager. Every read serves that one generation, and a file that
// changes under a live store never receives new entries.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func twoMessageFile(t *testing.T) (path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "session.jsonl")
	header := `{"type":"session","version":3,"id":"h1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/ws"}` + "\n"
	u1 := `{"type":"message","id":"u1","timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"aaaa","timestamp":1}}` + "\n"
	u2 := `{"type":"message","id":"u2","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"user","content":"bbbb","timestamp":2}}` + "\n"
	if err := os.WriteFile(path, []byte(header+u1+u2), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func openPersisted(t *testing.T, path string) *Store {
	t.Helper()
	s, err := OpenFile(path, "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// rewriteSecondLine replaces bbbb with BBBB (same length, same ID): the
// rewrite keeps the record shape but changes the bytes on disk.
func rewriteSecondLineSameID(path string) {
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.Contains(l, `"id":"u2"`) {
			lines[i] = strings.Replace(l, "bbbb", "BBBB", 1)
		}
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// rewriteSecondLineNewID swaps the ID without changing the line length.
func rewriteSecondLineNewID(path string) {
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.Contains(l, `"id":"u2"`) {
			lines[i] = strings.Replace(l, `"id":"u2"`, `"id":"uX"`, 1)
		}
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// A rewrite after the session was loaded never reaches the model input: the
// loaded generation is served, and the changed file takes no new entries.
func TestF04ExternalRewriteAfterLoad(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(path string)
	}{
		{"rewrite-same-id", rewriteSecondLineSameID},
		{"rewrite-new-id", rewriteSecondLineNewID},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			path := twoMessageFile(t)
			s := openPersisted(t, path)
			mutate.fn(path)
			// The rewrite is same-length, so size and inode stay equal. Force
			// an mtime distinct from the load-time stat so the change is
			// observable on any filesystem, including coarse-clock kernels
			// where the rewrite can land in one mtime tick.
			changed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := os.Chtimes(path, changed, changed); err != nil {
				t.Fatal(err)
			}

			active, err := s.ActivePath()
			if err != nil {
				t.Fatalf("ActivePath: %v", err)
			}
			if len(active) != 2 {
				t.Fatalf("active path = %d entries, want 2", len(active))
			}
			if got, _ := active[1].Message.Message.User.Content.(string); got != "bbbb" {
				t.Fatalf("read served the rewritten file: %q", got)
			}
			if _, err := s.Projection(); err != nil {
				t.Fatalf("Projection: %v", err)
			}
			if _, err := s.Recall(); err != nil {
				t.Fatalf("Recall: %v", err)
			}
			var items []TranscriptItem
			complete, err := s.Timeline(true, 0, func(it TranscriptItem) bool { items = append(items, it); return true })
			if err != nil || !complete {
				t.Fatalf("Timeline complete=%v err=%v", complete, err)
			}
			// The changed file never receives new entries.
			_, err = s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "after"}})
			if err == nil {
				t.Fatal("append to a changed file was accepted")
			}
			if s.LeafID() != "u2" {
				t.Fatalf("leaf moved on refused append: %q", s.LeafID())
			}
		})
	}
}

// A structural change after the session was loaded has the same effect: the
// loaded generation is served and the file takes no new entries.
func TestF04ExternalStructuralMutations(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(path string)
	}{
		{"truncate", func(p string) { _ = os.Truncate(p, 0) }},
		{"append", func(p string) {
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
			defer f.Close()
			_, _ = f.WriteString("{\"type\":\"label\",\"id\":\"zz\",\"timestamp\":\"2026-01-01T00:00:09.000Z\"}\n")
		}},
		{"unlink", func(p string) { _ = os.Remove(p) }},
		{"rename", func(p string) { _ = os.Rename(p, p+".moved") }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			path := twoMessageFile(t)
			s := openPersisted(t, path)
			mutate.fn(path)

			active, err := s.ActivePath()
			if err != nil {
				t.Fatalf("ActivePath: %v", err)
			}
			if len(active) != 2 {
				t.Fatalf("active path = %d entries, want 2", len(active))
			}
			if _, err := s.Projection(); err != nil {
				t.Fatalf("Projection: %v", err)
			}
			var n int
			complete, err := s.Timeline(true, 0, func(TranscriptItem) bool { n++; return true })
			if err != nil || !complete {
				t.Fatalf("Timeline complete=%v err=%v", complete, err)
			}
			if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "after"}}); err == nil {
				t.Fatalf("append to a %s file was accepted", mutate.name)
			}
		})
	}
}

// The session file is read once at open: the loaded generation is the only
// generation the store serves.
func TestF04SingleReadGeneration(t *testing.T) {
	path := twoMessageFile(t)
	s := openPersisted(t, path)
	before, err := s.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	// A rewrite after the load cannot slip a second generation into the
	// store: the loaded entries stay in memory.
	if err := os.WriteFile(path, []byte("{\"type\":\"message\",\"id\":\"u9\",\"timestamp\":\"2026-01-01T00:00:09.000Z\",\"message\":{\"role\":\"user\",\"content\":\"rewritten\",\"timestamp\":9}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := s.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("active path = %d entries, want %d", len(after), len(before))
	}
	if after[len(after)-1].ID() != "u2" {
		t.Fatalf("leaf = %q, want the loaded leaf", after[len(after)-1].ID())
	}
}
