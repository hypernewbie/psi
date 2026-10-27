package session

// Phase C regressions for PLAN3 F04: lazy session reads must never mix file
// generations. A mid-walk rewrite, truncate, append, unlink, or rename fails
// the walk (Timeline reports incomplete output, never success); identity and
// byte-hash checks reject equal-size rewrites before model input is built.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// rewriteSecondLine replaces bbbb with BBBB (same length, same ID): the byte
// hash must reject it even though offsets, lengths, and identity all match.
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

func TestF04MidWalkRewriteSameLengthSameID(t *testing.T) {
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
			fired := false
			s.LoadHook = func(m RecordMeta) {
				if m.ID == "u2" && !fired {
					fired = true
					mutate.fn(path)
				}
			}
			if _, err := s.Projection(); err == nil || !strings.Contains(err.Error(), "changed externally") {
				t.Fatalf("Projection accepted mixed generation: %v", err)
			}
			if _, err := s.ActivePath(); err == nil || !strings.Contains(err.Error(), "changed externally") {
				t.Fatalf("ActivePath accepted mixed generation: %v", err)
			}
			if _, err := s.Recall(); err == nil || !strings.Contains(err.Error(), "changed externally") {
				t.Fatalf("Recall accepted mixed generation: %v", err)
			}
			var items []TranscriptItem
			complete, err := s.Timeline(true, 0, func(it TranscriptItem) bool { items = append(items, it); return true })
			if err == nil || complete {
				t.Fatalf("Timeline reported success on mixed generation: complete=%v err=%v", complete, err)
			}
			if !strings.Contains(err.Error(), "changed externally") {
				t.Fatalf("wrong Timeline error: %v", err)
			}
			// Failed activation keeps the old runtime: entries and leaf intact.
			if s.LeafID() != "u2" {
				t.Fatalf("leaf moved on failed walk: %q", s.LeafID())
			}
		})
	}
}

func TestF04MidWalkStructuralMutations(t *testing.T) {
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
			fired := false
			s.LoadHook = func(m RecordMeta) {
				if !fired {
					fired = true
					mutate.fn(path)
				}
			}
			if _, err := s.Projection(); err == nil {
				t.Fatal("Projection accepted structural mutation mid-walk")
			}
			var n int
			complete, err := s.Timeline(true, 0, func(TranscriptItem) bool { n++; return true })
			if err == nil || complete {
				t.Fatalf("Timeline success on %s: complete=%v err=%v", mutate.name, complete, err)
			}
		})
	}
}

func TestF04ChangeBetweenIndexAndActivation(t *testing.T) {
	path := twoMessageFile(t)
	s := openPersisted(t, path)
	// External rewrite after open, before any read: pre-walk check refuses.
	rewriteSecondLineSameID(path)
	if _, err := s.Projection(); err == nil {
		t.Fatal("Projection accepted pre-walk rewrite")
	}
}

func TestF04LoadRecordIdentityUnit(t *testing.T) {
	path := twoMessageFile(t)
	idx, err := BuildIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta RecordMeta
	for _, m := range idx.Records {
		if m.ID == "u2" {
			meta = m
		}
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := loadRecord(f, meta); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	// Zero-hash legacy meta still enforces identity.
	bare := meta
	bare.Hash = 0
	bare.ID = "uX"
	if _, err := loadRecord(f, bare); err == nil {
		t.Fatal("identity change accepted with zero hash")
	}
}
