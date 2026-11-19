package session

import (
	"strings"
	"testing"
)

func newHistoryStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(OpenOptions{Cwd: t.TempDir(), AgentDir: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func historyUser(text string) AgentMessage {
	return AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: text}}
}

func historyAssistantText(text string) AgentMessage {
	return AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop",
		Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: text}}},
	}}
}

func historyToolCall(id, name string, args map[string]any) AgentMessage {
	return AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "toolUse",
		Content: []ContentBlock{{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: id, Name: name, Arguments: args}}},
	}}
}

func historyToolResult(id, name, text string) AgentMessage {
	return AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{
		Role: "toolResult", ToolCallID: id, ToolName: name,
		Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: text}}},
	}}
}

func TestHistoryListNewestFirstAndSkipsStateOnlyEntries(t *testing.T) {
	store := newHistoryStore(t)
	for _, message := range []AgentMessage{
		historyUser("first request"),
		historyAssistantText("second answer"),
		historyToolCall("call-1", "read", map[string]any{"path": "a.txt"}),
		historyToolResult("call-1", "read", "file body"),
	} {
		if _, err := store.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AppendCustomEntry("psi.loop.state", map[string]any{"active": true}); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	result := ListHistory(path, 0)
	if !result.OK || len(result.ReturnedIDs) != 4 {
		t.Fatalf("list result = %+v", result)
	}
	if !strings.Contains(result.Text, "newest first") || !strings.Contains(result.Text, "tool result (read)") {
		t.Fatalf("list text = %q", result.Text)
	}
	// Newest first: the tool result entry leads, the first user request trails.
	if result.ReturnedIDs[0] != path[3].ID() || result.ReturnedIDs[3] != path[0].ID() {
		t.Fatalf("list order = %v", result.ReturnedIDs)
	}
	if strings.Contains(result.Text, "psi.loop.state") {
		t.Fatalf("state-only entry leaked into the index: %q", result.Text)
	}
}

// A compaction hides earlier entries from the model-facing projection, but the
// raw active branch still has them: that is exactly what this tool recovers.
func TestHistorySearchFindsEntriesHiddenByCompaction(t *testing.T) {
	store := newHistoryStore(t)
	if _, err := store.AppendMessage(historyUser("earlier detail NEEDLE-42")); err != nil {
		t.Fatal(err)
	}
	kept, err := store.AppendMessage(historyUser("kept tail"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendCompaction("summary of the earlier work", kept, 100, nil); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	projection := BuildProjection(path)
	if strings.Contains(flatText(projection.Flat), "NEEDLE-42") {
		t.Fatal("projection unexpectedly kept the compacted detail")
	}

	result := SearchHistory(path, "needle-42", 0)
	if !result.OK || len(result.ReturnedIDs) != 1 {
		t.Fatalf("search result = %+v", result)
	}
	if !strings.Contains(result.Text, "NEEDLE-42") || !strings.Contains(result.Text, "matches for") {
		t.Fatalf("search text = %q", result.Text)
	}
	if result := SearchHistory(path, "  ", 0); result.OK || !strings.Contains(result.Text, "non-empty query") {
		t.Fatalf("blank search = %+v", result)
	}
	if result := SearchHistory(path, "absent-needle", 0); !result.OK || !strings.Contains(result.Text, "No raw session entries match") {
		t.Fatalf("miss search = %+v", result)
	}
}

func flatText(messages []AgentMessage) string {
	var b strings.Builder
	for _, message := range messages {
		if message.CompactionSummary != nil {
			b.WriteString(message.CompactionSummary.Summary)
		}
		if message.User != nil {
			if text, ok := message.User.Content.(string); ok {
				b.WriteString(text)
			}
		}
	}
	return b.String()
}

func TestHistoryReadExpandsToolPairsAndBoundsOutput(t *testing.T) {
	store := newHistoryStore(t)
	if _, err := store.AppendMessage(historyUser("plan")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(historyToolCall("call-1", "read", map[string]any{"path": "big.txt"})); err != nil {
		t.Fatal(err)
	}
	resultID, err := store.AppendMessage(historyToolResult("call-1", "read", strings.Repeat("payload ", 20_000)))
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	// Asking for the call alone must return its result too, bounded.
	read := ReadHistory(path, path[1].ID(), path[1].ID(), 1_000)
	if !read.OK {
		t.Fatalf("read result = %+v", read)
	}
	if len(read.ReturnedIDs) != 2 || read.ReturnedIDs[1] != resultID {
		t.Fatalf("tool pair was split: %v", read.ReturnedIDs)
	}
	if !strings.Contains(read.Text, "bytes omitted") {
		t.Fatalf("large payload was not bounded: %d bytes", len(read.Text))
	}
	if len(read.Text) > 1_000*4 {
		t.Fatalf("read text = %d bytes, want bounded by ~4000", len(read.Text))
	}
	if read.EstimatedTokens <= 0 {
		t.Fatalf("estimated tokens = %d", read.EstimatedTokens)
	}
}

func TestHistoryReadRejectsBadRanges(t *testing.T) {
	store := newHistoryStore(t)
	if _, err := store.AppendMessage(historyUser("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(historyUser("two")); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		start, end string
		want       string
	}{
		{"missing bound", "", path[0].ID(), "requires both"},
		{"unknown id", "nope", path[0].ID(), "not on the active raw session branch"},
		{"reversed", path[1].ID(), path[0].ID(), "must come before"},
	}
	for _, tc := range cases {
		result := ReadHistory(path, tc.start, tc.end, 0)
		if result.OK || !strings.Contains(result.Text, tc.want) {
			t.Fatalf("%s read = %+v", tc.name, result)
		}
		if !strings.HasPrefix(result.Text, "Cannot read session history: ") {
			t.Fatalf("%s read text = %q", tc.name, result.Text)
		}
	}
}
