package session

// PLAN3 P3-053: storage fault injection. Every I/O failure point fails stop:
// short writes, sync, close, open, and stat faults leave the disk uncertain
// without repair, retry, or false success, and block further model/tool work.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type faultFile struct {
	*os.File
	writeN   int // bytes to accept before failing (<=0: fail immediately)
	writeErr error
	syncErr  error
	closeErr error
	wrote    int
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.writeErr != nil && f.wrote >= f.writeN {
		return 0, f.writeErr
	}
	allow := len(p)
	if f.writeN > 0 && f.wrote+allow > f.writeN {
		allow = f.writeN - f.wrote
	}
	n, err := f.File.Write(p[:allow])
	f.wrote += n
	if err != nil {
		return n, err
	}
	if allow < len(p) {
		if f.writeErr != nil {
			return n, f.writeErr
		}
		return n, io.ErrShortWrite
	}
	return n, nil
}

func (f *faultFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func (f *faultFile) Close() error {
	// An injected close error still releases the test's real descriptor.
	return errors.Join(f.File.Close(), f.closeErr)
}

func faultStore(t *testing.T, open func(string, int, os.FileMode) (durableFile, error), stat func(string) (os.FileInfo, error)) *Store {
	t.Helper()
	ws := t.TempDir()
	s, err := New(OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hello", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	s.storage = &storageOps{open: open, stat: stat}
	t.Cleanup(func() { s.storage = nil })
	return s
}

func passThrough(t *testing.T) func(string, int, os.FileMode) (durableFile, error) {
	return func(path string, flags int, mode os.FileMode) (durableFile, error) {
		return os.OpenFile(path, flags, mode)
	}
}

func TestCompactionAppendFaultDoesNotExposeCheckpoint(t *testing.T) {
	s := faultStore(t, func(path string, flags int, mode os.FileMode) (durableFile, error) {
		f, err := os.OpenFile(path, flags, mode)
		if err != nil {
			return nil, err
		}
		return &faultFile{File: f, writeN: 4}, nil
	}, nil)
	before, err := os.ReadFile(s.File())
	if err != nil {
		t.Fatal(err)
	}
	parent := s.LeafID()
	if _, err := s.AppendCompaction("synthetic checkpoint", parent, 100, nil); err == nil {
		t.Fatal("compaction append fault was accepted")
	}
	after, err := os.ReadFile(s.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) || s.WriteError() == nil {
		t.Fatal("failed compaction changed the durable prefix or did not block storage")
	}
	for _, entry := range s.Entries() {
		if entry.Kind == "compaction" {
			t.Fatal("failed append exposed a compaction checkpoint in memory")
		}
	}
}

func TestF53FaultMatrix(t *testing.T) {
	mkFault := func(t *testing.T, wrap func(*os.File) *faultFile) func(string, int, os.FileMode) (durableFile, error) {
		return func(path string, flags int, mode os.FileMode) (durableFile, error) {
			f, err := os.OpenFile(path, flags, mode)
			if err != nil {
				return nil, err
			}
			return wrap(f), nil
		}
	}
	for _, tc := range []struct {
		name string
		wrap func(*os.File) *faultFile
	}{
		{"short-write", func(f *os.File) *faultFile { return &faultFile{File: f, writeN: 3} }},
		{"write-error", func(f *os.File) *faultFile {
			return &faultFile{File: f, writeErr: fmt.Errorf("injected write failure")}
		}},
		{"sync-error", func(f *os.File) *faultFile { return &faultFile{File: f, syncErr: fmt.Errorf("injected sync failure")} }},
		{"close-error", func(f *os.File) *faultFile {
			return &faultFile{File: f, closeErr: fmt.Errorf("injected close failure")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := faultStore(t, mkFault(t, tc.wrap), nil)
			before, err := os.ReadFile(s.File())
			if err != nil {
				t.Fatal(err)
			}
			count, leaf := len(s.entries), s.leafID
			if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "next", Timestamp: 2}}); err == nil {
				t.Fatal("fault accepted as success")
			}
			after, err := os.ReadFile(s.File())
			if err != nil {
				t.Fatal(err)
			}
			// A failed append can leave an uncertain tail, never a damaged prefix.
			if !bytes.HasPrefix(after, before) {
				t.Fatal("durable prefix rewritten or truncated after fault")
			}
			if len(s.entries) != count || s.leafID != leaf || s.writeErr == nil {
				t.Fatal("failed append advanced logical state or left writes enabled")
			}
			// Fail-stop: further model/tool work is blocked, not retried.
			if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "retry", Timestamp: 3}}); err == nil {
				t.Fatal("store recovered implicitly after fault")
			}
		})
	}
}

func TestF53OpenStatFaults(t *testing.T) {
	s := faultStore(t,
		func(string, int, os.FileMode) (durableFile, error) { return nil, fmt.Errorf("injected open failure") },
		nil)
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "x", Timestamp: 9}}); err == nil {
		t.Fatal("open fault accepted")
	}
	s2 := faultStore(t, passThrough(t),
		func(string) (os.FileInfo, error) { return nil, fmt.Errorf("injected stat failure") })
	if _, err := s2.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "x", Timestamp: 9}}); err == nil {
		t.Fatal("stat fault accepted")
	}
}

// Invariant: Simulating crashes by truncating session files at every single byte offset
// across the last entries must never panic, must skip or reject torn lines with structured
// errors, and any cleanly opened prefix must maintain graph consistency.
func TestTruncatedFileSweep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "full_session.jsonl")
	s, err := New(OpenOptions{Cwd: "/tmp", SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	s.file = path
	s.flushed = false

	// Create a session with 4 distinct entries
	u1, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "turn 1 prompt", Timestamp: 1}})
	if err != nil {
		t.Fatal(err)
	}
	a1, err := s.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "turn 1 reply"}}},
		API: "anthropic-messages", Provider: "anthropic", Model: "m1", Usage: Usage{TotalTokens: 10}, Timestamp: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "turn 2 prompt", Timestamp: 3}})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "turn 2 reply"}}},
		API: "anthropic-messages", Provider: "anthropic", Model: "m1", Usage: Usage{TotalTokens: 20}, Timestamp: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = u1
	_ = a1
	_ = u2
	_ = a2

	fullBytes, err := os.ReadFile(s.File())
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSuffix(string(fullBytes), "\n"), "\n")
	if len(lines) != 5 { // 1 header + 4 entries
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}

	// Sweep truncations across the last 3 entries (from end of line 1 to end of file).
	headerAndFirstLen := len(lines[0]) + 1 + len(lines[1]) + 1
	sweepDir := t.TempDir()

	for cut := headerAndFirstLen; cut <= len(fullBytes); cut++ {
		truncated := fullBytes[:cut]
		testFile := filepath.Join(sweepDir, fmt.Sprintf("trunc_%d.jsonl", cut))
		if err := os.WriteFile(testFile, truncated, 0o644); err != nil {
			t.Fatal(err)
		}

		// 1. ReadFileEntries must NEVER panic
		hdr, entries, _, readErr := ReadFileEntries(testFile)
		if readErr != nil {
			var pe *ParseError
			if !errors.As(readErr, &pe) && !strings.Contains(readErr.Error(), "record exceeds") {
				t.Fatalf("cut %d: unexpected ReadFileEntries error type: %v", cut, readErr)
			}
		} else {
			if hdr == nil {
				t.Fatalf("cut %d: ReadFileEntries succeeded but header is nil", cut)
			}
			if len(entries) < 1 {
				t.Fatalf("cut %d: ReadFileEntries should have at least 1 entry", cut)
			}
		}

		// 2. OpenFile must NEVER panic
		opened, openErr := OpenFile(testFile, "/tmp", true)
		if openErr != nil {
			// Structured error check: either parse error, truncated record error, or version error
			msg := openErr.Error()
			isStructured := strings.Contains(msg, "truncated final record") ||
				strings.Contains(msg, "ParseError") ||
				strings.Contains(msg, "invalid") ||
				strings.Contains(msg, "syntax") ||
				strings.Contains(msg, "header") ||
				strings.Contains(msg, "unexpected end")
			var pe *ParseError
			if !isStructured && !errors.As(openErr, &pe) {
				t.Fatalf("cut %d: OpenFile returned unstructured error: %v", cut, openErr)
			}
		} else {
			// If it opened, every complete entry must form a valid path
			path, err := opened.ActivePath()
			if err != nil {
				t.Fatalf("cut %d: opened session has invalid ActivePath: %v", cut, err)
			}
			if len(path) == 0 {
				t.Fatalf("cut %d: opened session has empty ActivePath", cut)
			}
			// Verify leaf ID points to an existing entry
			if opened.GetEntry(opened.LeafID()) == nil {
				t.Fatalf("cut %d: LeafID %q not found in entries", cut, opened.LeafID())
			}
		}
	}
}

func TestMissingTrailingNewlines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no_trailing_nl.jsonl")

	lines := []string{
		`{"type":"session","version":3,"id":"s-no-nl","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"first line","timestamp":1}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"second line"}],"api":"a","provider":"p","model":"m","usage":{"totalTokens":5},"stopReason":"stop","timestamp":2}}`,
	}
	// Join with \n but omit trailing \n
	rawNoTrailingNL := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(rawNoTrailingNL), 0o644); err != nil {
		t.Fatal(err)
	}

	// HasIncompleteTail should recognize this complete JSON record as complete (returns false)
	incomplete, err := HasIncompleteTail(path)
	if err != nil {
		t.Fatalf("HasIncompleteTail error: %v", err)
	}
	if incomplete {
		t.Fatalf("HasIncompleteTail reported incomplete for complete record without newline")
	}

	// OpenFile should succeed
	store, err := OpenFile(path, "/tmp", true)
	if err != nil {
		t.Fatalf("OpenFile failed on file without trailing newline: %v", err)
	}
	if len(store.Entries()) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(store.Entries()))
	}

	// Subsequent append must automatically insert newline separator and not clobber
	newID, err := store.AppendMessage(AgentMessage{
		Role: "user",
		User: &UserMessage{Role: "user", Content: "third line", Timestamp: 3},
	})
	if err != nil {
		t.Fatalf("AppendMessage failed: %v", err)
	}
	if newID == "" {
		t.Fatal("empty newID")
	}

	// Re-read file and verify JSONL validity of all lines
	rawAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterLines := strings.Split(strings.TrimSpace(string(rawAfter)), "\n")
	if len(afterLines) != 4 { // header + 3 entries
		t.Fatalf("expected 4 lines after append, got %d. Content:\n%s", len(afterLines), string(rawAfter))
	}
	for i, l := range afterLines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %d is not valid JSON after append: %v (line: %q)", i, err, l)
		}
	}
}

func TestCRLFAndExtraWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crlf_and_spaces.jsonl")

	records := []string{
		`{"type":"session","version":3,"id":"s-crlf","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp"}`,
		`  {"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"turn 1","timestamp":1}}  `,
		"", // blank line
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"reply 1"}],"api":"a","provider":"p","model":"m","usage":{"totalTokens":5},"stopReason":"stop","timestamp":2}}`,
		"   ", // whitespace-only line
	}
	// Join with \r\n (CRLF)
	content := strings.Join(records, "\r\n") + "\r\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// ReadFileEntries should handle CRLF and blank lines cleanly
	hdr, entries, _, err := ReadFileEntries(path)
	if err != nil {
		t.Fatalf("ReadFileEntries failed on CRLF: %v", err)
	}
	if hdr == nil || hdr.ID != "s-crlf" {
		t.Fatalf("header corrupted: %+v", hdr)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID() != "u1" || entries[1].ID() != "a1" {
		t.Fatalf("unexpected entry IDs: %s, %s", entries[0].ID(), entries[1].ID())
	}

	// OpenFile should also succeed
	store, err := OpenFile(path, "/tmp", true)
	if err != nil {
		t.Fatalf("OpenFile failed on CRLF session: %v", err)
	}
	pathEntries, err := store.ActivePath()
	if err != nil {
		t.Fatalf("ActivePath failed: %v", err)
	}
	if len(pathEntries) != 2 {
		t.Fatalf("expected 2 path entries, got %d", len(pathEntries))
	}

	// Verify no \r leaked into message content
	if pathEntries[0].Message.Message.User.Content != "turn 1" {
		t.Fatalf("user content corrupted by CRLF: %q", pathEntries[0].Message.Message.User.Content)
	}
}

func TestHasIncompleteTailMatrix(t *testing.T) {
	dir := t.TempDir()

	testCases := []struct {
		name           string
		content        string
		wantIncomplete bool
	}{
		{"empty file", "", false},
		{"single line with newline", `{"type":"session","version":3,"id":"s1","timestamp":"t","cwd":"/tmp"}` + "\n", false},
		{"single line without newline complete JSON", `{"type":"session","version":3,"id":"s1","timestamp":"t","cwd":"/tmp"}`, false},
		{"single line torn JSON", `{"type":"session","version":3,"id":"s1"`, true},
		{"multi-line valid with newline", `{"type":"session","version":3,"id":"s1","timestamp":"t","cwd":"/tmp"}` + "\n" + `{"type":"message","id":"m1"}` + "\n", false},
		{"multi-line valid without newline", `{"type":"session","version":3,"id":"s1","timestamp":"t","cwd":"/tmp"}` + "\n" + `{"type":"message","id":"m1"}`, false},
		{"multi-line torn without newline", `{"type":"session","version":3,"id":"s1","timestamp":"t","cwd":"/tmp"}` + "\n" + `{"type":"message","id":`, true},
		{"whitespace trailing", `{"type":"session","version":3,"id":"s1","timestamp":"t","cwd":"/tmp"}` + "\n   \n", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, "tail_test_"+strings.ReplaceAll(tc.name, " ", "_")+".jsonl")
			if err := os.WriteFile(p, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := HasIncompleteTail(p)
			if err != nil {
				t.Fatalf("HasIncompleteTail error: %v", err)
			}
			if got != tc.wantIncomplete {
				t.Errorf("HasIncompleteTail(%q) = %v, want %v", tc.content, got, tc.wantIncomplete)
			}
		})
	}
}
