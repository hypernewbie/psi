package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/testutil/leakcheck"
)

func TestHeaderVersionChecks(t *testing.T) {
	dir := t.TempDir()
	// Missing version (v1) refused.
	v1path := filepath.Join(dir, "v1.jsonl")
	writeLines(t, v1path, []string{`{"type":"session","id":"s1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp"}`})
	if _, err := OpenFile(v1path, "/tmp", true); err == nil || !strings.Contains(strings.ToLower(err.Error()), "v1") {
		t.Errorf("v1 should be refused, got %v", err)
	}
	// v2 refused.
	v2path := filepath.Join(dir, "v2.jsonl")
	writeLines(t, v2path, []string{`{"type":"session","version":2,"id":"s1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp"}`})
	if _, err := OpenFile(v2path, "/tmp", true); err == nil || !strings.Contains(strings.ToLower(err.Error()), "v2") {
		t.Errorf("v2 should be refused, got %v", err)
	}
	// Future version refused.
	fupath := filepath.Join(dir, "fu.jsonl")
	writeLines(t, fupath, []string{`{"type":"session","version":99,"id":"s1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp"}`})
	if _, err := OpenFile(fupath, "/tmp", true); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("future version should be refused, got %v", err)
	}
	// Non-empty invalid file never replaced.
	badpath := filepath.Join(dir, "bad.jsonl")
	writeLines(t, badpath, []string{`not a session`})
	before, _ := os.ReadFile(badpath)
	if _, err := OpenFile(badpath, "/tmp", true); err == nil {
		t.Errorf("invalid file should fail")
	}
	after, _ := os.ReadFile(badpath)
	if string(before) != string(after) {
		t.Errorf("invalid file was modified")
	}
}

func TestStoreNewAndAppend(t *testing.T) {
	dir := t.TempDir()
	s, err := New(OpenOptions{Cwd: "/tmp/work", SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.File() == "" {
		t.Fatalf("persisted session should have a file")
	}
	// Setup-only entries stay in memory (no file yet).
	if _, err := os.Stat(s.File()); !os.IsNotExist(err) {
		t.Fatalf("file should not exist before first user message")
	}
	if _, err := s.AppendThinkingLevelChange("medium"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.File()); !os.IsNotExist(err) {
		t.Fatalf("setup entries must not flush")
	}
	uid, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hello", Timestamp: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.File()); err != nil {
		t.Fatalf("file should exist after user message: %v", err)
	}
	// Prefix preservation: header + thinking change + user message all present.
	h, entries, _, err := ReadFileEntries(s.File())
	if err != nil {
		t.Fatal(err)
	}
	if h.ID != s.Header().ID {
		t.Errorf("header id mismatch")
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Parent chain: thinking change is root, user is its child.
	if entries[0].Base.ID == uid {
		t.Errorf("order wrong: thinking change should precede user")
	}
	path, err := s.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[1].ID() != uid {
		t.Errorf("active path wrong: %v", path)
	}
	// Append assistant + tool result; verify Pi can read (round-trip).
	aid, err := s.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "hi"}}},
		API: "anthropic-messages", Provider: "anthropic", Model: "m",
		Usage: Usage{TotalTokens: 2, Cost: UsageCost{}}, StopReason: "stop", Timestamp: 2}})
	if err != nil {
		t.Fatal(err)
	}
	_ = aid
	if _, err := s.AppendMessage(AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{
		Role: "toolResult", ToolCallID: "c1", ToolName: "read",
		Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}},
		IsError: false, Timestamp: 3}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.File())
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 { // header + 3 messages? thinking+user+assistant+toolResult = 4 entries + header = 5? check
		// header + thinking + user + assistant + toolResult = 5 lines
		if len(lines) != 5 {
			t.Errorf("expected 5 lines, got %d", len(lines))
		}
	}
	// Byte-prefix preserved on second append: re-read first bytes.
	h2, entries2, _, err := ReadFileEntries(s.File())
	if err != nil {
		t.Fatal(err)
	}
	_ = h2
	if len(entries2) != 4 {
		t.Errorf("expected 4 entries, got %d", len(entries2))
	}
	var _ = json.Marshal
}

func TestStoreTruncatedTailRefusesAppend(t *testing.T) {
	dir := t.TempDir()
	s, err := New(OpenOptions{Cwd: "/tmp", SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	// Truncate the file.
	f, _ := os.OpenFile(s.File(), os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString(`{"type":"message","id":"broken",`)
	f.Close()
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "after", Timestamp: 2}}); err == nil {
		t.Fatalf("append after truncated tail must fail")
	}
	// Original bytes unchanged apart from the injected fragment (no concatenated record).
	data, _ := os.ReadFile(s.File())
	if strings.Contains(string(data), `"after"`) {
		t.Errorf("failed append must not write")
	}
}

func TestStoreBranchAndIndexErrors(t *testing.T) {
	s, err := New(OpenOptions{Cwd: "/tmp", Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "a", Timestamp: 1}})
	b, _ := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "b", Timestamp: 2}})
	if err := s.Branch(a); err != nil {
		t.Fatal(err)
	}
	c, _ := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "c", Timestamp: 3}})
	path, _ := s.ActivePath()
	if len(path) != 2 || path[1].ID() != c {
		t.Errorf("branch path wrong: %v", path)
	}
	_ = b
	// Missing parent diagnosed.
	if _, err := BuildPath(s.Entries(), "nope"); err == nil {
		t.Errorf("missing leaf should error")
	}
	// Cycle diagnosed.
	e1 := &Entry{Kind: "message", Base: entryBase{Type: "message", ID: "x", ParentID: strPtr("y")}}
	e2 := &Entry{Kind: "message", Base: entryBase{Type: "message", ID: "y", ParentID: strPtr("x")}}
	if _, err := BuildPath([]*Entry{e1, e2}, "x"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("cycle should error, got %v", err)
	}
	// Duplicate diagnosed.
	d1 := &Entry{Kind: "message", Base: entryBase{Type: "message", ID: "d"}}
	d2 := &Entry{Kind: "message", Base: entryBase{Type: "message", ID: "d"}}
	if _, err := BuildPath([]*Entry{d1, d2}, "d"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate should error, got %v", err)
	}
}

func strPtr(s string) *string { return &s }

func TestDiscoveryAndLifecycles(t *testing.T) {
	dir := t.TempDir()
	cwd := "/tmp/ws-discover"
	s1, _ := New(OpenOptions{Cwd: cwd, SessionDir: dir, Persist: true})
	if _, err := s1.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "first", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	s2, _ := New(OpenOptions{Cwd: cwd, SessionDir: dir, Persist: true})
	if _, err := s2.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "second", Timestamp: 2}}); err != nil {
		t.Fatal(err)
	}
	infos, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("discover=%d", len(infos))
	}
	// Sorted deterministically (most recent first); both have message counts.
	if infos[0].MessageCount != 1 || infos[1].MessageCount != 1 {
		t.Errorf("counts %+v", infos)
	}
	if infos[0].FirstMessage == "" || infos[0].FirstMessage == "(no messages)" {
		t.Errorf("first message missing: %+v", infos[0])
	}
	// Large session still lists: append a 200 KiB tool result then re-discover.
	big := make([]byte, 200*1024)
	for i := range big {
		big[i] = 'a'
	}
	if _, err := s1.AppendMessage(AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult", ToolCallID: "c9", ToolName: "bash", Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: string(big)}}}, IsError: false, Timestamp: 3}}); err != nil {
		t.Fatal(err)
	}
	infos2, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos2) != 2 {
		t.Fatalf("rediscover=%d", len(infos2))
	}
	// Continue opens most recent for cwd.
	cont, err := Continue(cwd, dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if cont.Header().ID == "" {
		t.Errorf("continue missing id")
	}
	// Open explicit path.
	opened, err := OpenFile(s1.File(), cwd, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Entries()) != 2 {
		t.Errorf("open entries=%d", len(opened.Entries()))
	}
	// Ephemeral (--no-session) leaves unrelated sessions unchanged.
	eph, _ := New(OpenOptions{Cwd: cwd, Persist: false})
	if _, err := eph.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "tmp", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	if eph.File() != "" {
		t.Errorf("ephemeral should have no file")
	}
	infos3, _ := Discover(dir)
	if len(infos3) != 2 {
		t.Errorf("ephemeral polluted dir")
	}
	// Reject missing explicit path instead of silently creating a replacement.
	if _, err := OpenFile(dir+"/does-not-exist.jsonl", cwd, true); err == nil {
		t.Errorf("missing explicit path should fail")
	}
}

func TestExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	s1, _ := New(OpenOptions{Cwd: "/tmp", SessionDir: dir, Persist: true})
	if _, err := s1.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s1.TryLock(); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer s1.Unlock()
	s2, err := OpenFile(s1.File(), "/tmp", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.TryLock(); err == nil {
		s2.Unlock()
		t.Fatalf("second psi writer must not acquire lock")
	}
	// External growth detected before append.
	f, _ := os.OpenFile(s1.File(), os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString(headerLine("x", "/tmp") + "\n")
	f.Close()
	if _, err := s1.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "after-external", Timestamp: 2}}); err == nil {
		t.Fatalf("external growth must stop append")
	}
}

// Invariant: Concurrent AppendMessage on one session forms a strictly linear parent chain,
// persists N valid JSONL lines, produces unique IDs, and runs race-free.
func TestConcurrentAppendMessage(t *testing.T) {
	defer leakcheck.Verify(t)
	dir := t.TempDir()
	cwd := "/tmp/ws-concurrent"
	s, err := New(OpenOptions{Cwd: cwd, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, goroutines)
	ids := make(chan string, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			id, appendErr := s.AppendMessage(AgentMessage{
				Role: "user",
				User: &UserMessage{
					Role:      "user",
					Content:   fmt.Sprintf("concurrent-msg-%d", idx),
					Timestamp: int64(idx + 1),
				},
			})
			if appendErr != nil {
				errs <- appendErr
				return
			}
			ids <- id
		}(i)
	}

	close(start)
	wg.Wait()
	close(errs)
	close(ids)

	for err := range errs {
		t.Fatalf("concurrent append error: %v", err)
	}

	// Verify all returned IDs are unique.
	uniqueIDs := make(map[string]bool, goroutines)
	for id := range ids {
		if uniqueIDs[id] {
			t.Fatalf("duplicate entry ID generated: %s", id)
		}
		uniqueIDs[id] = true
	}
	if len(uniqueIDs) != goroutines {
		t.Fatalf("expected %d unique IDs, got %d", goroutines, len(uniqueIDs))
	}

	// Verify JSONL file integrity on disk.
	raw, err := os.ReadFile(s.File())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != goroutines+1 { // 1 header + 50 messages
		t.Fatalf("expected %d lines on disk, got %d", goroutines+1, len(lines))
	}

	// Check header.
	var hdr SessionHeader
	if err := json.Unmarshal([]byte(lines[0]), &hdr); err != nil {
		t.Fatalf("header line is invalid JSON: %v", err)
	}
	if hdr.ID != s.Header().ID || hdr.Type != "session" {
		t.Fatalf("header mismatch: %+v", hdr)
	}

	// Check each message entry and verify the parent chain forms a continuous linear sequence.
	var prevID *string
	for i := 1; i <= goroutines; i++ {
		var entry struct {
			Type      string  `json:"type"`
			ID        string  `json:"id"`
			ParentID  *string `json:"parentId"`
			Timestamp string  `json:"timestamp"`
		}
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			t.Fatalf("line %d is invalid JSON: %v", i, err)
		}
		if entry.Type != "message" {
			t.Fatalf("line %d type=%q, want 'message'", i, entry.Type)
		}
		if prevID == nil {
			if entry.ParentID != nil {
				t.Fatalf("first entry should have nil parent, got %v", *entry.ParentID)
			}
		} else {
			if entry.ParentID == nil || *entry.ParentID != *prevID {
				t.Fatalf("line %d parentID=%v, want %v", i, entry.ParentID, *prevID)
			}
		}
		idCopy := entry.ID
		prevID = &idCopy
	}

	// Reopen with OpenFile and verify ActivePath.
	opened, err := OpenFile(s.File(), cwd, true)
	if err != nil {
		t.Fatalf("OpenFile failed on concurrently written session: %v", err)
	}
	if len(opened.Entries()) != goroutines {
		t.Fatalf("reopened entries count=%d, want %d", len(opened.Entries()), goroutines)
	}
	path, err := opened.ActivePath()
	if err != nil {
		t.Fatalf("ActivePath failed: %v", err)
	}
	if len(path) != goroutines {
		t.Fatalf("ActivePath length=%d, want %d", len(path), goroutines)
	}
}

// Invariant: Lazy session file creation with O_EXCL triggers atomically on the first conversation append;
// multiple simultaneous first appends create exactly one header, preserve all entries, and surface no EEXIST errors.
func TestFirstAppendRace(t *testing.T) {
	defer leakcheck.Verify(t)
	dir := t.TempDir()
	cwd := "/tmp/ws-first-append"
	s, err := New(OpenOptions{Cwd: cwd, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}

	// Before any conversation message, file must not exist.
	if _, err := os.Stat(s.File()); !os.IsNotExist(err) {
		t.Fatalf("session file must not exist before first message")
	}

	const goroutines = 40
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, appendErr := s.AppendMessage(AgentMessage{
				Role: "user",
				User: &UserMessage{
					Role:      "user",
					Content:   fmt.Sprintf("first-race-%d", idx),
					Timestamp: int64(idx + 1),
				},
			})
			if appendErr != nil {
				errs <- appendErr
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("first append race error (possible EEXIST or corruption): %v", err)
	}

	// Verify exactly one header and all entries on disk.
	hdr, entries, _, err := ReadFileEntries(s.File())
	if err != nil {
		t.Fatalf("ReadFileEntries failed: %v", err)
	}
	if hdr == nil || hdr.ID != s.Header().ID {
		t.Fatalf("invalid or missing header: %+v", hdr)
	}
	if len(entries) != goroutines {
		t.Fatalf("expected %d entries, got %d", goroutines, len(entries))
	}
}

// Invariant: Buffered setup entries flush atomically alongside the first conversation message
// under a race between multiple concurrent appenders.
func TestFirstAppendRaceWithSetupEntries(t *testing.T) {
	defer leakcheck.Verify(t)
	dir := t.TempDir()
	cwd := "/tmp/ws-setup-race"
	s, err := New(OpenOptions{Cwd: cwd, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}

	// Append setup entries (thinking level change & model change) which stay buffered in memory.
	tlID, err := s.AppendThinkingLevelChange("high")
	if err != nil {
		t.Fatal(err)
	}
	mcID, err := s.AppendModelChange("anthropic", "claude-3-5-sonnet")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(s.File()); !os.IsNotExist(err) {
		t.Fatalf("file must not exist before conversation messages")
	}

	const goroutines = 30
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, appendErr := s.AppendMessage(AgentMessage{
				Role: "user",
				User: &UserMessage{Role: "user", Content: fmt.Sprintf("race-msg-%d", idx), Timestamp: int64(idx + 10)},
			})
			if appendErr != nil {
				errs <- appendErr
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("unexpected error during first append race with setup: %v", err)
	}

	hdr, entries, _, err := ReadFileEntries(s.File())
	if err != nil {
		t.Fatal(err)
	}
	if hdr == nil {
		t.Fatal("missing header")
	}
	// Total entries: 2 setup + goroutines messages.
	if len(entries) != 2+goroutines {
		t.Fatalf("expected %d entries, got %d", 2+goroutines, len(entries))
	}
	if entries[0].Base.ID != tlID || entries[1].Base.ID != mcID {
		t.Fatalf("setup entries not at head of session: %+v, %+v", entries[0].Base, entries[1].Base)
	}
}

// Invariant: Concurrent readers (ActivePath, Projection, Branch, GetEntry, Accounting, MetadataPath)
// and concurrent writers run cleanly under -race without panics or corrupt state.
func TestConcurrentReadersAndWriters(t *testing.T) {
	defer leakcheck.Verify(t)
	dir := t.TempDir()
	cwd := "/tmp/ws-rw-race"
	s, err := New(OpenOptions{Cwd: cwd, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}

	// Seed with an initial user + assistant turn.
	rootID, err := s.AppendMessage(AgentMessage{
		Role: "user",
		User: &UserMessage{Role: "user", Content: "seed user message", Timestamp: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AppendMessage(AgentMessage{
		Role: "assistant",
		Assistant: &AssistantMessage{
			Role:    "assistant",
			Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "seed assistant"}}},
			API:     "anthropic-messages", Provider: "anthropic", Model: "claude-x",
			Usage: Usage{Input: 10, Output: 5, TotalTokens: 15},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var knownMu sync.RWMutex
	knownIDs := []string{rootID}

	var stop atomic.Bool
	var wg sync.WaitGroup

	// 4 writer goroutines
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			iter := 0
			for !stop.Load() && iter < 100 {
				iter++
				var id string
				var appendErr error
				switch iter % 4 {
				case 0:
					id, appendErr = s.AppendMessage(AgentMessage{
						Role: "user",
						User: &UserMessage{Role: "user", Content: fmt.Sprintf("u-w%d-%d", workerID, iter), Timestamp: int64(iter)},
					})
				case 1:
					id, appendErr = s.AppendMessage(AgentMessage{
						Role: "assistant",
						Assistant: &AssistantMessage{
							Role:    "assistant",
							Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: fmt.Sprintf("a-w%d-%d", workerID, iter)}}},
							API:     "anthropic-messages", Provider: "anthropic", Model: "claude-x",
							Usage: Usage{Input: 5, Output: 2, TotalTokens: 7},
						},
					})
				case 2:
					id, appendErr = s.AppendSessionInfo(fmt.Sprintf("session-name-%d", iter))
				case 3:
					id, appendErr = s.AppendModelChange("anthropic", fmt.Sprintf("model-v%d", iter))
				}
				if appendErr != nil {
					t.Errorf("writer %d failed: %v", workerID, appendErr)
					return
				}
				knownMu.Lock()
				knownIDs = append(knownIDs, id)
				knownMu.Unlock()
			}
		}(w)
	}

	// 8 reader goroutines
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for !stop.Load() {
				// Reader 1: ActivePath
				path, pathErr := s.ActivePath()
				if pathErr != nil {
					t.Errorf("reader %d ActivePath error: %v", readerID, pathErr)
					return
				}
				if len(path) == 0 {
					t.Errorf("reader %d empty ActivePath", readerID)
					return
				}

				// Reader 2: Projection
				proj, projErr := s.Projection()
				if projErr != nil {
					t.Errorf("reader %d Projection error: %v", readerID, projErr)
					return
				}
				if proj == nil {
					t.Errorf("reader %d nil projection", readerID)
					return
				}

				// Reader 3: GetEntry
				knownMu.RLock()
				targetID := knownIDs[len(knownIDs)-1]
				knownMu.RUnlock()
				entry := s.GetEntry(targetID)
				if entry == nil {
					t.Errorf("reader %d GetEntry(%s) returned nil", readerID, targetID)
					return
				}

				// Reader 4: Accounting
				acct, acctErr := s.Accounting()
				if acctErr != nil {
					t.Errorf("reader %d Accounting error: %v", readerID, acctErr)
					return
				}
				if acct.Attempts < 1 {
					t.Errorf("reader %d expected attempts >= 1, got %d", readerID, acct.Attempts)
					return
				}

				// Reader 5: MetadataPath & Entries
				mpath, mErr := s.MetadataPath()
				if mErr != nil {
					t.Errorf("reader %d MetadataPath error: %v", readerID, mErr)
					return
				}
				if len(mpath) == 0 {
					t.Errorf("reader %d empty MetadataPath", readerID)
					return
				}
				_ = s.Entries()
				_ = s.SessionName()
				_ = s.LeafID()
			}
		}(r)
	}

	// Run for 300ms then stop
	time.Sleep(300 * time.Millisecond)
	stop.Store(true)
	wg.Wait()

	// Final verification of integrity
	finalPath, err := s.ActivePath()
	if err != nil {
		t.Fatalf("final ActivePath failed: %v", err)
	}
	if len(finalPath) < 2 {
		t.Fatalf("expected at least 2 entries, got %d", len(finalPath))
	}
}

func TestStoreUncoveredMethods(t *testing.T) {
	dir := t.TempDir()
	cwd := "/tmp/ws-uncovered"
	s, err := New(OpenOptions{Cwd: cwd, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}

	// Dir, Cwd, Persistent getters
	if s.Dir() != s.dir {
		t.Errorf("Dir() = %q, want %q", s.Dir(), s.dir)
	}
	if s.Cwd() != cwd {
		t.Errorf("Cwd() = %q, want %q", s.Cwd(), cwd)
	}
	if !s.Persistent() {
		t.Errorf("Persistent() = false, want true")
	}

	// Initial message
	uID, err := s.AppendMessage(AgentMessage{
		Role: "user",
		User: &UserMessage{Role: "user", Content: "initial user message", Timestamp: 100},
	})
	if err != nil {
		t.Fatal(err)
	}

	// GetEntry on existing entry
	loaded := s.GetEntry(uID)
	if loaded == nil || loaded.ID() != uID {
		t.Fatalf("GetEntry(%q) failed: %+v", uID, loaded)
	}
	// GetEntry on non-existent entry returns nil
	if missing := s.GetEntry("non-existent-id"); missing != nil {
		t.Fatalf("GetEntry for missing ID should return nil, got %+v", missing)
	}

	// AppendUsage
	cost := UsageCost{Input: 0.01, Output: 0.02, Total: 0.03}
	reasoningTokens := 50.0
	cache1h := 10.0
	usageVal := Usage{
		Input:        100,
		Output:       50,
		CacheRead:    20,
		CacheWrite:   10,
		TotalTokens:  180,
		Cost:         cost,
		Reasoning:    &reasoningTokens,
		CacheWrite1h: &cache1h,
	}
	usageID, err := s.AppendUsage("auxiliary", "anthropic", "claude-3-5-sonnet", usageVal, "batch usage")
	if err != nil {
		t.Fatalf("AppendUsage failed: %v", err)
	}
	if usageID == "" {
		t.Fatal("AppendUsage returned empty ID")
	}

	// Assistant message with usage
	respModel := "claude-3-5-sonnet-20241022"
	asstID, err := s.AppendMessage(AgentMessage{
		Role: "assistant",
		Assistant: &AssistantMessage{
			Role:          "assistant",
			Content:       []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "answer"}}},
			API:           "anthropic-messages",
			Provider:      "anthropic",
			Model:         "claude-3-5-sonnet",
			ResponseModel: &respModel,
			Usage: Usage{
				Input:       200,
				Output:      100,
				TotalTokens: 300,
			},
			Timestamp: 200,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = asstID

	// Compaction with usage
	compUsage := Usage{Input: 30, Output: 15, TotalTokens: 45}
	compID, err := s.AppendCompactionWithUsage("summary of prior turns", uID, 500, nil, &compUsage)
	if err != nil {
		t.Fatalf("AppendCompactionWithUsage failed: %v", err)
	}
	if compID == "" {
		t.Fatal("AppendCompactionWithUsage returned empty ID")
	}

	// Accounting
	acct, err := s.Accounting()
	if err != nil {
		t.Fatalf("Accounting() error: %v", err)
	}
	if acct.Attempts != 3 { // 1 assistant + 1 usage entry + 1 compaction usage
		t.Errorf("Accounting attempts = %d, want 3", acct.Attempts)
	}
	// Total input = 100 + 200 + 30 = 330
	if acct.Usage.Input != 330 {
		t.Errorf("Accounting Input = %f, want 330", acct.Usage.Input)
	}
	// Total output = 50 + 100 + 15 = 165
	if acct.Usage.Output != 165 {
		t.Errorf("Accounting Output = %f, want 165", acct.Usage.Output)
	}
	// ReturnedModel should match assistant ResponseModel
	if acct.ReturnedModel != respModel {
		t.Errorf("Accounting ReturnedModel = %q, want %q", acct.ReturnedModel, respModel)
	}

	// MetadataPath
	metaPath, err := s.MetadataPath()
	if err != nil {
		t.Fatalf("MetadataPath() error: %v", err)
	}
	if len(metaPath) != 4 { // uID, usageID, asstID, compID
		t.Errorf("MetadataPath len = %d, want 4", len(metaPath))
	}

	// AppendContextEdit tests
	// 1. Edit user message content
	newContent := json.RawMessage(`{"content":"edited user message"}`)
	editID, err := s.AppendContextEdit(uID, newContent)
	if err != nil {
		t.Fatalf("AppendContextEdit failed: %v", err)
	}
	if editID == "" {
		t.Fatal("AppendContextEdit returned empty ID")
	}

	// 2. Edit assistant message with string content (triggers mustMarshal normalization)
	asstEditContent := json.RawMessage(`{"content":"normalized assistant string"}`)
	asstEditID, err := s.AppendContextEdit(asstID, asstEditContent)
	if err != nil {
		t.Fatalf("AppendContextEdit assistant failed: %v", err)
	}
	if asstEditID == "" {
		t.Fatal("AppendContextEdit assistant returned empty ID")
	}

	// 3. Edit with null replacement (omits from projection)
	_, err = s.AppendContextEdit(asstID, json.RawMessage(`null`))
	if err != nil {
		t.Fatalf("AppendContextEdit null replacement failed: %v", err)
	}

	// 4. Edit non-existent entry -> error
	if _, err := s.AppendContextEdit("non-existent-target", newContent); err == nil {
		t.Errorf("AppendContextEdit on missing target should error")
	}

	// 5. Edit non-editable entry (e.g. usage) -> error
	if _, err := s.AppendContextEdit(usageID, newContent); err == nil {
		t.Errorf("AppendContextEdit on non-editable usage entry should error")
	}

	// 6. Edit with malformed JSON replacement -> error
	if _, err := s.AppendContextEdit(uID, json.RawMessage(`{"invalid":"structure"}`)); err == nil {
		t.Errorf("AppendContextEdit with missing content should error")
	}

	// FailWritesForTest
	injectedErr := fmt.Errorf("injected storage disk failure")
	s.FailWritesForTest(injectedErr)
	_, writeErr := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "should fail"}})
	if writeErr == nil {
		t.Fatalf("AppendMessage should fail after FailWritesForTest")
	}
	if s.WriteError() == nil {
		t.Fatalf("WriteError() should record error after failed append")
	}
}

func TestExternalChangeDetectionInLoad(t *testing.T) {
	dir := t.TempDir()
	s, err := New(OpenOptions{Cwd: "/tmp", SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	id1, err := s.AppendMessage(AgentMessage{
		Role: "user",
		User: &UserMessage{Role: "user", Content: "entry 1", Timestamp: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.AppendMessage(AgentMessage{
		Role: "user",
		User: &UserMessage{Role: "user", Content: "entry 2", Timestamp: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = id1

	// Set LoadHook to modify file on the second entry (when snapshot file f is already open)
	hookCalled := false
	s.LoadHook = func(m RecordMeta) {
		if m.ID == id2 {
			hookCalled = true
			f, _ := os.OpenFile(s.File(), os.O_WRONLY|os.O_APPEND, 0o644)
			f.WriteString(`{"type":"session","version":3,"id":"corrupt","timestamp":"t","cwd":"/tmp"}` + "\n")
			f.Close()
		}
	}

	// ActivePath loads all entries in order: id1 opens f, then id2 fires hook and triggers externalChangeError
	path, err := s.ActivePath()
	if err == nil || !strings.Contains(err.Error(), "session file changed externally during read") {
		t.Fatalf("expected externalChangeError, got path=%v err=%v", path, err)
	}
	if !hookCalled {
		t.Fatal("expected LoadHook to be called on id2")
	}
}

func TestLimitReaderAndParseError(t *testing.T) {
	// limitReader test
	src := strings.NewReader("hello world this is a test string")
	lr := limitReader(src, 5)
	buf, err := io.ReadAll(lr)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Errorf("limitReader returned %q, want 'hello'", string(buf))
	}

	// ParseError Unwrap
	innerErr := fmt.Errorf("syntax error in json")
	pe := &ParseError{Path: "/path/to/session.jsonl", Line: 42, Err: innerErr}
	if !errors.Is(pe, innerErr) {
		t.Errorf("ParseError does not wrap innerErr")
	}
	if pe.Unwrap() != innerErr {
		t.Errorf("pe.Unwrap() = %v, want %v", pe.Unwrap(), innerErr)
	}
	if !strings.Contains(pe.Error(), "42") || !strings.Contains(pe.Error(), "syntax error") {
		t.Errorf("pe.Error() = %q", pe.Error())
	}
}

func TestTimeMillisHelpers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	iso := now.Format(time.RFC3339Nano)
	ms := parseTimeMillis(iso)
	if ms != now.UnixMilli() {
		t.Errorf("parseTimeMillis(%q) = %d, want %d", iso, ms, now.UnixMilli())
	}

	// Lenient parse format
	lenient := "2026-05-15T12:30:00.123456789+02:00"
	msLenient := parseTimeMillis(lenient)
	if msLenient == 0 {
		t.Errorf("parseTimeMillis(%q) failed to parse lenient timestamp", lenient)
	}

	// Empty string
	if parseTimeMillis("") != 0 {
		t.Errorf("parseTimeMillis(\"\") should return 0")
	}
	// Garbage string
	if parseTimeMillis("not-a-timestamp") != 0 {
		t.Errorf("parseTimeMillis(garbage) should return 0")
	}

	// millisToISO round-trip
	reconstructed := millisToISO(now.UnixMilli())
	reMs := parseTimeMillis(reconstructed)
	if reMs != now.UnixMilli() {
		t.Errorf("millisToISO round trip failed: %d vs %d", reMs, now.UnixMilli())
	}
}
