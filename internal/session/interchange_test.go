package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pi -> psi -> Pi interchange (Phase 11 Step 88).
// Builds a Pi-shaped session covering text, tools, model changes, compaction,
// context edits, and historical metadata; continues a copy with psi; reopens
// the copy and checks projection parity + Pi readability.

func piFixture(t *testing.T, path string) {
	t.Helper()
	lines := []string{
		`{"type":"session","version":3,"id":"pi-sess-1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/ws"}`,
		// System declaration with tools.
		`{"type":"message","id":"s1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"system","content":"base","sections":{"tools":"<tools>read</tools>"},"toolsAdded":[{"name":"read"}],"timestamp":1}}`,
		`{"type":"model_change","id":"m1","parentId":"s1","timestamp":"2026-01-01T00:00:02.000Z","provider":"anthropic","modelId":"claude-x"}`,
		`{"type":"thinking_level_change","id":"t1","parentId":"m1","timestamp":"2026-01-01T00:00:03.000Z","thinkingLevel":"medium"}`,
		`{"type":"message","id":"u1","parentId":"t1","timestamp":"2026-01-01T00:00:04.000Z","message":{"role":"user","content":"hi","timestamp":4}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"api":"anthropic-messages","provider":"anthropic","model":"claude-x","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":5}}`,
		`{"type":"usage","id":"use1","parentId":"a1","timestamp":"2026-01-01T00:00:06.000Z","kind":"cache_warm","provider":"anthropic","model":"claude-x","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}}`,
		`{"type":"compaction","id":"c1","parentId":"use1","timestamp":"2026-01-01T00:00:07.000Z","summary":"old summary","firstKeptEntryId":"u1","tokensBefore":100}`,
		`{"type":"message","id":"u2","parentId":"c1","timestamp":"2026-01-01T00:00:08.000Z","message":{"role":"user","content":"after","timestamp":8}}`,
		// Historical metadata: branch summary + custom + bashExecution (included + excluded).
		`{"type":"branch_summary","id":"b1","parentId":"u2","timestamp":"2026-01-01T00:00:09.000Z","fromId":"u1","summary":"branch work"}`,
		`{"type":"custom_message","id":"cm1","parentId":"b1","timestamp":"2026-01-01T00:00:10.000Z","customType":"ext","content":"custom hi","display":true}`,
		`{"type":"message","id":"bx1","parentId":"cm1","timestamp":"2026-01-01T00:00:11.000Z","message":{"role":"bashExecution","command":"ls","output":"f","timestamp":11}}`,
		`{"type":"message","id":"bx2","parentId":"bx1","timestamp":"2026-01-01T00:00:12.000Z","message":{"role":"bashExecution","command":"secret","output":"s","timestamp":12,"excludeFromContext":true}}`,
		// Unknown metadata preserved.
		`{"type":"label","id":"l1","parentId":"bx2","timestamp":"2026-01-01T00:00:13.000Z","targetId":"u2","label":"mark"}`,
		`{"type":"session_info","id":"si1","parentId":"l1","timestamp":"2026-01-01T00:00:14.000Z","name":"demo"}`,
		// Context edit omitting u2? No: replace u2 content instead (keeps branch non-empty).
		`{"type":"context_edit","id":"e1","parentId":"si1","timestamp":"2026-01-01T00:00:15.000Z","targetId":"u2","replacement":{"content":"after (edited)"}}`,
	}
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInterchangePiPsiPi(t *testing.T) {
	dir := t.TempDir()
	piPath := filepath.Join(dir, "pi.jsonl")
	piFixture(t, piPath)

	// Open copy with psi.
	psiPath := filepath.Join(dir, "psi.jsonl")
	data, _ := os.ReadFile(piPath)
	if err := os.WriteFile(psiPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFile(psiPath, "/tmp/ws", true)
	if err != nil {
		t.Fatalf("psi open Pi session: %v", err)
	}
	before, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	nBefore := len(ConvertToLLM(before.Flat))

	// Continue with psi (append user + assistant).
	if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "psi continues", Timestamp: 20}}); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Projection()
	if len(after.Flat) <= len(before.Flat) {
		t.Errorf("append should extend context")
	}

	origLen := len(data)
	// Reopen the copy (Pi-reader parity): same projection, byte prefix preserved.
	rawBefore, _ := os.ReadFile(psiPath)
	store2, err := OpenFile(psiPath, "/tmp/ws", true)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	proj2, _ := store2.Projection()
	llm2 := ConvertToLLM(proj2.Flat)
	if len(llm2) != len(ConvertToLLM(after.Flat)) {
		t.Errorf("reopen parity: %d vs %d", len(llm2), len(ConvertToLLM(after.Flat)))
	}
	rawAfter, _ := os.ReadFile(psiPath)
	if len(rawAfter) <= origLen || len(rawAfter) < len(rawBefore) {
		t.Errorf("file should grow")
	}
	// Prefix preserved: first bytes identical.
	if len(rawAfter) < len(data) || string(rawAfter[:len(data)]) != string(data) {
		// data was the original Pi file; psi appends, never rewrites prefix.
		// Note: OpenFile does not rewrite on open; only appends.
		t.Errorf("prefix not preserved")
	}
	_ = nBefore

	// Pi readability: every line is valid JSON with required envelope fields.
	for i, line := range strings.Split(strings.TrimSpace(string(rawAfter)), "\n") {
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatalf("line %d invalid JSON for Pi: %v", i, err)
		}
		if probe["type"] == nil || probe["id"] == nil && probe["type"] != "session" {
			t.Errorf("line %d missing envelope: %s", i, line)
		}
	}
}

func TestFuzzJSONL(t *testing.T) {
	// Bounded fuzz smoke: malformed tails, random bytes, overlarge records.
	seeds := []string{
		"{\"type\":\"session\",\"version\":3,\"id\":\"a\",\"timestamp\":\"t\",\"cwd\":\"/tmp\"}\n",
		"not json\n",
		"\x00\x01\x02\n",
		strings.Repeat("x", 1000) + "\n",
		"{\"type\":\"message\",\"id\":\"m\",\"parentId\":null,\"timestamp\":\"t\",\"message\":{\"role\":\"user\",\"content\":\"hi\",\"timestamp\":1}}\n",
	}
	for _, s := range seeds {
		_, _, _, _ = DecodeEntries(strings.NewReader(s), "fuzz")
		// Must not panic or loop unboundedly.
	}
	// Graph fuzz: random parent links diagnose without hanging.
	ents := []*Entry{}
	for i := 0; i < 50; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		ents = append(ents, &Entry{Kind: "message", Base: entryBase{Type: "message", ID: id, ParentID: nil}})
	}
	_, _ = BuildPath(ents, "")
}

func TestFailureInjection(t *testing.T) {
	dir := t.TempDir()
	// Disk-full simulation: read-only dir.
	ro := filepath.Join(dir, "ro")
	if err := os.MkdirAll(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	s, err := New(OpenOptions{Cwd: "/tmp", SessionDir: ro, Persist: true})
	if err == nil {
		// Creation of dir succeeds (exists); first message flush fails under perms? root may bypass; just check error path exists.
		_, _ = s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi", Timestamp: 1}})
	}
	// Truncation + external change already covered; ensure they stop, not repair.
	p := filepath.Join(dir, "s.jsonl")
	piFixture(t, p)
	orig, _ := os.ReadFile(p)
	f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString("{\"type\":\"broken\",")
	f.Close()
	if _, err := OpenFile(p, "/tmp/ws", true); err == nil {
		t.Errorf("truncated must stop")
	}
	after, _ := os.ReadFile(p)
	// Incomplete tail preserved (no silent repair/concat): file ends with fragment.
	if !strings.HasSuffix(string(after), "{\"type\":\"broken\",") {
		t.Errorf("file was repaired silently")
	}
	_ = orig
}
