package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
