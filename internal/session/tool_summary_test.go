package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLazyTimelineExtractsBoundedToolSummaryWithoutBodyCanary(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := New(OpenOptions{Cwd: cwd, SessionDir: filepath.Join(root, "sessions"), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "write a file"}}); err != nil {
		t.Fatal(err)
	}
	const canary = "large-write-body-must-not-reach-timeline"
	body := strings.Repeat(canary, 12_000)
	call := ToolCall{Type: "toolCall", ID: "write-call", Name: "write", Arguments: map[string]any{"path": filepath.Join(cwd, "src", "result.go"), "content": body}}
	callEntryID, err := store.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", Content: []ContentBlock{{Type: "toolCall", Call: &call}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult", ToolCallID: call.ID, ToolName: call.Name, Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "hidden-result-canary"}}}}}); err != nil {
		t.Fatal(err)
	}
	lazy, err := OpenFile(store.File(), cwd, true)
	if err != nil {
		t.Fatal(err)
	}
	loadedCallRecord := false
	lazy.LoadHook = func(meta RecordMeta) {
		if meta.ID == callEntryID {
			loadedCallRecord = true
		}
	}
	var found *TranscriptItem
	complete, err := lazy.Timeline(false, 0, func(item TranscriptItem) bool {
		if item.Kind == KindTool {
			copy := item
			found = &copy
		}
		return true
	})
	if err != nil || !complete || found == nil {
		t.Fatalf("timeline complete=%v found=%+v err=%v", complete, found, err)
	}
	if loadedCallRecord {
		t.Fatal("lazy replay loaded the full tool-call record to recover its summary")
	}
	if found.ToolName != "write" || found.ToolState != "done" || found.ToolSummary.Path != "src/result.go" {
		t.Fatalf("structured replay summary=%+v", found)
	}
	encoded, err := json.Marshal(found)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 1024 || strings.Contains(string(encoded), canary) || strings.Contains(string(encoded), "hidden-result-canary") {
		t.Fatalf("lazy display item retained hidden content (%d bytes)", len(encoded))
	}
	if found.ToolSummary.ExitCode != nil || found.ToolSummary.Note != "" {
		t.Fatalf("spurious metadata: %+v", found.ToolSummary)
	}
}
