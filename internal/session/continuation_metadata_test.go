package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const metadataContinuationHeader = `{"type":"session","version":3,"id":"meta","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp/ws"}`

func continuationLine(kind, id, parent, body string) string {
	p := "null"
	if parent != "" {
		p = fmt.Sprintf("%q", parent)
	}
	return fmt.Sprintf(`{"type":%q,"id":%q,"parentId":%s,"timestamp":"2026-01-01T00:00:01Z"%s}`, kind, id, p, body)
}

func continuationStore(t *testing.T, records ...string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	data := metadataContinuationHeader + "\n"
	for _, record := range records {
		data += record + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFile(path, "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func assertContinuationParity(t *testing.T, store *Store) {
	t.Helper()
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.MetadataPath()
	if err != nil {
		t.Fatal(err)
	}
	fullErr := CheckContinuable(projection.Flat, projection.Entries)
	metaErr := CheckContinuableMetadata(BuildContextEntries(path))
	if (fullErr == nil) != (metaErr == nil) {
		t.Fatalf("continuation disagreement: full=%v metadata=%v", fullErr, metaErr)
	}
}

func TestContinuationMetadataParity(t *testing.T) {
	user := continuationLine("message", "u1", "", `,"message":{"role":"user","content":"hi","timestamp":1}`)
	assistant := continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop","timestamp":2}`)
	tool := continuationLine("message", "t1", "a1", `,"message":{"role":"toolResult","toolCallId":"c1","toolName":"read","content":[{"type":"text","text":"body"}],"timestamp":3}`)

	cases := []struct {
		name    string
		records []string
		want    bool // true = continuation allowed
	}{
		{"valid", []string{user, assistant, tool}, true},
		{"assistant unknown block", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"mystery"}],"stopReason":"stop"}`)}, false},
		{"assistant deferred stop", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"text","text":"partial"}],"stopReason":"deferred"}`)}, false},
		{"assistant deferred field", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"text","text":"partial"}],"deferred":{"handle":"x"}}`)}, false},
		{"assistant deferred alias field", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"text","text":"partial"}],"Deferred":{"handle":"x"}}`)}, false},
		{"unknown role", []string{
			continuationLine("message", "x1", "", `,"message":{"role":"future","content":"x"}`)}, false},
		{"system image", []string{
			continuationLine("message", "s1", "", `,"message":{"role":"system","content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}`)}, false},
		{"user image", []string{
			continuationLine("message", "u1", "", `,"message":{"role":"user","content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}`)}, true},
		{"custom message object", []string{
			continuationLine("custom_message", "cm1", "", `,"customType":"ext","content":{"bad":true},"display":true`)}, false},
		{"custom message alias content ignored", []string{
			continuationLine("custom_message", "cm1", "", `,"customType":"ext","Content":{"bad":true},"display":true`)}, true},
		{"compaction system alias content ignored", []string{
			continuationLine("compaction", "cp", "", `,"summary":"s","firstKeptEntryId":"cp","tokensBefore":1,"systemMessage":{"role":"system","Content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}`)}, true},
		{"context edit repairs assistant", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"mystery"}]}`),
			continuationLine("context_edit", "e1", "a1", `,"targetId":"a1","replacement":{"content":[{"type":"text","text":"fixed"}]}`)}, true},
		{"context edit drops assistant", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"mystery"}]}`),
			continuationLine("context_edit", "e1", "a1", `,"targetId":"a1","replacement":null`)}, true},
		{"context edit drops deferred stop", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"text","text":"partial"}],"stopReason":"deferred"}`),
			continuationLine("context_edit", "e1", "a1", `,"targetId":"a1","replacement":null`)}, true},
		{"context edit keeps deferred stop", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"text","text":"partial"}],"stopReason":"deferred"}`),
			continuationLine("context_edit", "e1", "a1", `,"targetId":"a1","replacement":{"content":"fixed"}`)}, false},
		{"context edit drops deferred field", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"text","text":"partial"}],"deferred":{"handle":"x"}}`),
			continuationLine("context_edit", "e1", "a1", `,"targetId":"a1","replacement":null`)}, false},
		{"context edit drops unknown role", []string{
			continuationLine("message", "x1", "", `,"message":{"role":"future","content":"x"}`),
			continuationLine("context_edit", "e1", "x1", `,"targetId":"x1","replacement":null`)}, true},
		{"context edit drops system image", []string{
			continuationLine("message", "s1", "", `,"message":{"role":"system","content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}`),
			continuationLine("context_edit", "e1", "s1", `,"targetId":"s1","replacement":null`)}, true},
		{"context edit introduces block", []string{user, assistant,
			continuationLine("context_edit", "e1", "a1", `,"targetId":"a1","replacement":{"content":[{"type":"mystery"}]}`)}, false},
		{"context edit invalid shape keeps original", []string{user,
			continuationLine("message", "a1", "u1", `,"message":{"role":"assistant","content":[{"type":"mystery"}]}`),
			continuationLine("context_edit", "e1", "a1", `,"targetId":"a1","replacement":{"content":{"not":"array"}}`)}, false},
		{"context edit repairs custom message", []string{
			continuationLine("custom_message", "cm1", "", `,"customType":"ext","content":{"bad":true},"display":true`),
			continuationLine("context_edit", "e1", "cm1", `,"targetId":"cm1","replacement":{"content":"fixed"}`)}, true},
		{"context edit repairs user object", []string{
			continuationLine("message", "u1", "", `,"message":{"role":"user","content":{"bad":true}}`),
			continuationLine("context_edit", "e1", "u1", `,"targetId":"u1","replacement":{"content":"fixed"}`)}, true},
		{"context edit introduces user image on system target", []string{
			continuationLine("message", "s1", "", `,"message":{"role":"system","content":"prompt"}`),
			continuationLine("context_edit", "e1", "s1", `,"targetId":"s1","replacement":{"content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}`)}, true},
		{"context edit cannot repair system image", []string{
			continuationLine("message", "s1", "", `,"message":{"role":"system","content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}`),
			continuationLine("context_edit", "e1", "s1", `,"targetId":"s1","replacement":{"content":"fixed"}`)}, false},
		{"latest edit wins", []string{user, assistant,
			continuationLine("context_edit", "e1", "a1", `,"targetId":"a1","replacement":{"content":[{"type":"text","text":"fixed"}]}`),
			continuationLine("context_edit", "e2", "e1", `,"targetId":"a1","replacement":{"content":[{"type":"mystery"}]}`)}, false},
		{"older compaction checkpoint ignored", []string{
			continuationLine("message", "u0", "", `,"message":{"role":"user","content":"root"}`),
			continuationLine("compaction", "cp1", "u0", `,"summary":"old","firstKeptEntryId":"u0","tokensBefore":10,"systemMessage":{"role":"system","content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}`),
			continuationLine("message", "m1", "cp1", `,"message":{"role":"user","content":"middle"}`),
			continuationLine("compaction", "cp2", "m1", `,"summary":"new","firstKeptEntryId":"u0","tokensBefore":20,"systemMessage":{"role":"system","content":"current instructions"}`),
			continuationLine("message", "u2", "cp2", `,"message":{"role":"user","content":"latest"}`)}, true},
		{"edit outside retained context", []string{
			continuationLine("message", "u0", "", `,"message":{"role":"future","content":"x"}`),
			continuationLine("message", "u1", "u0", `,"message":{"role":"user","content":"kept"}`),
			continuationLine("compaction", "cp", "u1", `,"summary":"s","firstKeptEntryId":"u1","tokensBefore":100`),
			continuationLine("context_edit", "e1", "cp", `,"targetId":"u0","replacement":null`)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := continuationStore(t, tc.records...)
			path, err := store.MetadataPath()
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "edit outside retained context" {
				for _, e := range BuildContextEntries(path) {
					if e.ID() == "u0" {
						t.Fatal("pre-compaction target was retained")
					}
				}
			}
			metaErr := CheckContinuableMetadata(BuildContextEntries(path))
			if (metaErr == nil) != tc.want {
				t.Logf("entry count=%d path=%d", store.EntryCount(), len(path))
				ctx := BuildContextEntries(path)
				for _, e := range ctx {
					t.Logf("entry kind=%s id=%s contentIssue=%q deferred=%v", e.Kind, e.ID(), e.contentIssue, e.deferred)
				}
				t.Fatalf("metadata continuation = %v, want allowed=%v", metaErr, tc.want)
			}
			assertContinuationParity(t, store)
		})
	}
}

func TestContinuationMetadataInMemoryAppend(t *testing.T) {
	store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", StopReason: "deferred",
		Content: []ContentBlock{{Type: "mystery", Raw: json.RawMessage(`{"type":"mystery"}`)}},
	}}); err != nil {
		t.Fatal(err)
	}
	path, err := store.MetadataPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckContinuableMetadata(BuildContextEntries(path)); err == nil {
		t.Fatal("in-memory unsupported assistant content was allowed")
	}
	assertContinuationParity(t, store)
}

func TestContinuationMetadataPreFlush(t *testing.T) {
	store, err := New(OpenOptions{Cwd: "/tmp/ws", SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{
		Role: "future",
		Raw:  json.RawMessage(`{"role":"future","content":"x"}`),
	}); err != nil {
		t.Fatal(err)
	}
	path, err := store.MetadataPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckContinuableMetadata(BuildContextEntries(path)); err == nil {
		t.Fatal("pre-flush unknown role was allowed")
	}
	assertContinuationParity(t, store)
}

func TestProjectionCacheAppendKeepsSnapshots(t *testing.T) {
	store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Flat) != 1 {
		t.Fatalf("first flat = %d", len(first.Flat))
	}
	again, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if again == first {
		t.Fatal("Projection must return a snapshot header")
	}
	if len(again.Flat) != len(first.Flat) {
		t.Fatal("cache hit changed flat length")
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}}}}); err != nil {
		t.Fatal(err)
	}
	grown, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Flat) != 1 || len(grown.Flat) != 2 {
		t.Fatalf("append snapshot isolation failed: first=%d grown=%d", len(first.Flat), len(grown.Flat))
	}
}

func TestProjectionCacheInvalidatesOnContextEdit(t *testing.T) {
	store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "bad"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Projection(); err != nil {
		t.Fatal(err)
	}
	replacement := []byte(`{"content":"fixed"}`)
	if _, err := store.AppendContextEdit(target, replacement); err != nil {
		t.Fatal(err)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Flat) != 1 || projection.Flat[0].User == nil || projection.Flat[0].User.Content != "fixed" {
		t.Fatalf("context edit cache stale: %+v", projection.Flat)
	}
}
