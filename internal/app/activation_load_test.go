package app

import (
	"testing"

	"psi/internal/session"
)

// Activation must not decode historical tool bodies. Recall is allowed to load
// user prompts, but the continuation check and model restore are metadata-only.
func TestPrepareActivationDoesNotLoadHistoricalPayloads(t *testing.T) {
	dir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: "/tmp/ws", SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("prompt")); err != nil {
		t.Fatal(err)
	}
	toolIDs := map[string]bool{}
	for i := 0; i < 20; i++ {
		id, err := store.AppendMessage(session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{
			Role: "toolResult", ToolCallID: "c", ToolName: "read",
			Content: []session.UserContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "body"}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		toolIDs[id] = true
	}
	opened, err := session.OpenFile(store.File(), "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	loadedTools := 0
	opened.LoadHook = func(meta session.RecordMeta) {
		if toolIDs[meta.ID] {
			loadedTools++
		}
	}
	act, err := PrepareActivation(opened)
	if err != nil {
		t.Fatal(err)
	}
	if act.Offline {
		t.Fatalf("unexpected offline: %v", act.OfflineErr)
	}
	if loadedTools != 0 {
		t.Fatalf("activation loaded %d historical tool payloads", loadedTools)
	}
	if len(act.Recall) != 1 || act.Recall[0] != "prompt" {
		t.Fatalf("recall = %v", act.Recall)
	}
}
