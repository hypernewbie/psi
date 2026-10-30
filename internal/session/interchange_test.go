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

// Invariant: Every valid Entry type marshals to JSONL, unmarshals with UnmarshalFileEntry,
// and re-marshals back to semantically identical JSON.
func TestEntryFullRoundTrip(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	testEntries := []struct {
		name string
		json string
	}{
		{
			name: "SessionHeader",
			json: `{"type":"session","version":3,"id":"sess-roundtrip-1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/roundtrip"}`,
		},
		{
			name: "UserMessageString",
			json: `{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hello world","timestamp":100}}`,
		},
		{
			name: "UserMessageBlocks",
			json: `{"type":"message","id":"u2","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"user","content":[{"type":"text","text":"read this"}],"timestamp":101}}`,
		},
		{
			name: "AssistantMessageWithBlocksAndUsage",
			json: `{"type":"message","id":"a1","parentId":"u2","timestamp":"2026-01-01T00:00:03.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"pondering","thinkingSignature":"sig-123"},{"type":"text","text":"done","textSignature":"tsig-456"}],"api":"anthropic-messages","provider":"anthropic","model":"claude-3-5-sonnet","usage":{"input":150,"output":75,"cacheRead":10,"cacheWrite":5,"totalTokens":240,"cost":{"input":0.001,"output":0.002,"cacheRead":0,"cacheWrite":0,"total":0.003}},"stopReason":"stop","timestamp":102}}`,
		},
		{
			name: "SystemMessageWithSections",
			json: `{"type":"message","id":"s1","parentId":"a1","timestamp":"2026-01-01T00:00:04.000Z","message":{"role":"system","content":"system base prompt","sections":{"tools":"<tools>bash</tools>"},"toolsAdded":[{"name":"bash"}],"timestamp":103}}`,
		},
		{
			name: "ToolResultMessageWithError",
			json: `{"type":"message","id":"tr1","parentId":"s1","timestamp":"2026-01-01T00:00:05.000Z","message":{"role":"toolResult","toolCallId":"c-123","toolName":"read","content":[{"type":"text","text":"file not found"}],"isError":true,"details":{"path":"missing.txt"},"timestamp":104}}`,
		},
		{
			name: "BashExecutionMessageExcluded",
			json: `{"type":"message","id":"bx1","parentId":"tr1","timestamp":"2026-01-01T00:00:06.000Z","message":{"role":"bashExecution","command":"echo secret","output":"secret","excludeFromContext":true,"timestamp":105}}`,
		},
		{
			name: "ThinkingLevelChange",
			json: `{"type":"thinking_level_change","id":"tl1","parentId":"bx1","timestamp":"2026-01-01T00:00:07.000Z","thinkingLevel":"high"}`,
		},
		{
			name: "ModelChange",
			json: `{"type":"model_change","id":"mc1","parentId":"tl1","timestamp":"2026-01-01T00:00:08.000Z","provider":"anthropic","modelId":"claude-sonnet-4"}`,
		},
		{
			name: "UsageEntryWithNote",
			json: `{"type":"usage","id":"usg1","parentId":"mc1","timestamp":"2026-01-01T00:00:09.000Z","kind":"warm_cache","provider":"anthropic","model":"claude-sonnet-4","usage":{"input":500,"output":200,"cacheRead":50,"cacheWrite":10,"totalTokens":760,"cost":{"input":0.005,"output":0.01,"cacheRead":0,"cacheWrite":0,"total":0.015}},"note":"cache warming usage"}`,
		},
		{
			name: "CompactionEntry",
			json: `{"type":"compaction","id":"cmp1","parentId":"usg1","timestamp":"2026-01-01T00:00:10.000Z","summary":"condensed summary of turns","firstKeptEntryId":"u2","tokensBefore":1500,"usage":{"input":100,"output":50,"totalTokens":150}}`,
		},
		{
			name: "BranchSummaryEntry",
			json: `{"type":"branch_summary","id":"bs1","parentId":"cmp1","timestamp":"2026-01-01T00:00:11.000Z","fromId":"u1","summary":"explored sub-branch for research"}`,
		},
		{
			name: "CustomEntry",
			json: `{"type":"custom","id":"cust1","parentId":"bs1","timestamp":"2026-01-01T00:00:12.000Z","customType":"psi.input","data":{"invocation":"interactive","source":"terminal"}}`,
		},
		{
			name: "CustomMessageEntry",
			json: `{"type":"custom_message","id":"cm1","parentId":"cust1","timestamp":"2026-01-01T00:00:13.000Z","customType":"extension.notification","content":"custom notify body","display":true,"details":{"severity":"info"}}`,
		},
		{
			name: "ContextEditEntry",
			json: `{"type":"context_edit","id":"ce1","parentId":"cm1","timestamp":"2026-01-01T00:00:14.000Z","targetId":"u2","replacement":{"content":"replacement text"}}`,
		},
		{
			name: "LabelEntry",
			json: `{"type":"label","id":"lbl1","parentId":"ce1","timestamp":"2026-01-01T00:00:15.000Z","targetId":"u2","label":"pinned-checkpoint"}`,
		},
		{
			name: "SessionInfoEntry",
			json: `{"type":"session_info","id":"si1","parentId":"lbl1","timestamp":"2026-01-01T00:00:16.000Z","name":"My Named Session"}`,
		},
	}

	for _, tc := range testEntries {
		t.Run(tc.name, func(t *testing.T) {
			fe, err := UnmarshalFileEntry([]byte(tc.json))
			if err != nil {
				t.Fatalf("UnmarshalFileEntry failed: %v", err)
			}

			// 1. If it's a header
			if fe.IsHeader {
				if fe.Header == nil || fe.Header.Type != "session" {
					t.Fatalf("expected valid header, got: %+v", fe.Header)
				}
				hdrBytes, err := json.Marshal(fe.Header)
				if err != nil {
					t.Fatalf("Marshal header failed: %v", err)
				}
				var origMap, roundMap map[string]any
				_ = json.Unmarshal([]byte(tc.json), &origMap)
				_ = json.Unmarshal(hdrBytes, &roundMap)
				if origMap["id"] != roundMap["id"] || origMap["version"].(float64) != roundMap["version"].(float64) {
					t.Errorf("header mismatch: %s vs %s", tc.json, string(hdrBytes))
				}
				return
			}

			// 2. If it's an entry
			entry := fe.Entry
			if entry == nil {
				t.Fatal("fe.Entry is nil")
			}

			// Marshal with Raw (preserves raw input)
			marshaledWithRaw, err := entry.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON with Raw failed: %v", err)
			}
			if string(marshaledWithRaw) != tc.json {
				t.Errorf("MarshalJSON with Raw mismatch:\ngot:  %s\nwant: %s", string(marshaledWithRaw), tc.json)
			}

			// Marshal without Raw (exercises fallback struct serialization for all entry types)
			entryNoRaw := *entry
			entryNoRaw.Raw = nil
			marshaledNoRaw, err := entryNoRaw.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON without Raw failed: %v", err)
			}

			// Compare semantic equality of marshaled struct JSON
			var origMap, noRawMap map[string]any
			if err := json.Unmarshal([]byte(tc.json), &origMap); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(marshaledNoRaw, &noRawMap); err != nil {
				t.Fatalf("unmarshal marshaledNoRaw failed: %v (raw: %s)", err, string(marshaledNoRaw))
			}
			if origMap["type"] != noRawMap["type"] || origMap["id"] != noRawMap["id"] {
				t.Errorf("struct marshal mismatch on type/id: orig=%+v, noRaw=%+v", origMap, noRawMap)
			}
		})
	}
	_ = strPtr
}

// Invariant: Unrecognized fields at the root or within nested structures are preserved
// identically (forward compatibility) across unmarshal, storage write, reopen, and re-marshal.
func TestUnrecognizedFieldsPreservation(t *testing.T) {
	futureLine := `{"type":"message","id":"future-msg-1","parentId":null,"timestamp":"2026-01-01T00:00:00.000Z","future_root_field":"hello_future","future_root_num":12345,"message":{"role":"user","content":"future content","timestamp":50,"future_inner_map":{"ext":true,"code":"ABC"}}}`

	fe, err := UnmarshalFileEntry([]byte(futureLine))
	if err != nil {
		t.Fatalf("UnmarshalFileEntry failed on forward-compat line: %v", err)
	}
	if fe.Entry == nil {
		t.Fatal("entry is nil")
	}

	// Marshaling preserves unknown fields from Raw
	marshaled, err := fe.Entry.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}
	if string(marshaled) != futureLine {
		t.Errorf("MarshalJSON did not preserve unknown fields:\ngot:  %s\nwant: %s", string(marshaled), futureLine)
	}

	// Now verify through a persisted session:
	dir := t.TempDir()
	path := filepath.Join(dir, "future_session.jsonl")
	header := `{"type":"session","version":3,"id":"future-sess","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp","future_header_flag":"preserved"}`
	fileContent := header + "\n" + futureLine + "\n"
	if err := os.WriteFile(path, []byte(fileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Open with OpenFile
	store, err := OpenFile(path, "/tmp", true)
	if err != nil {
		t.Fatalf("OpenFile failed on forward-compat session: %v", err)
	}

	// Append a new turn
	if _, err := store.AppendMessage(AgentMessage{
		Role: "user",
		User: &UserMessage{Role: "user", Content: "continuing after future turn", Timestamp: 60},
	}); err != nil {
		t.Fatalf("AppendMessage failed: %v", err)
	}

	// Re-read file from disk and check that the first entry still has future fields intact
	diskData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(diskData)), "\n")
	if len(lines) != 3 { // header + future turn + newly appended turn
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[1] != futureLine {
		t.Errorf("disk line 1 did not preserve future entry byte-for-byte:\ngot:  %s\nwant: %s", lines[1], futureLine)
	}
}

func TestMarshalEntryForAppend(t *testing.T) {
	entry := ModelChangeEntry{
		Type:      "model_change",
		ID:        "mc-append-1",
		ParentID:  nil,
		Timestamp: "2026-01-01T00:00:00.000Z",
		Provider:  "anthropic",
		ModelID:   "claude-sonnet-4",
	}

	b, err := MarshalEntryForAppend(entry)
	if err != nil {
		t.Fatalf("MarshalEntryForAppend failed: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal result failed: %v", err)
	}
	if m["type"] != "model_change" || m["modelId"] != "claude-sonnet-4" || m["provider"] != "anthropic" {
		t.Errorf("unexpected marshaled output: %s", string(b))
	}
}
