package app

import (
	"testing"

	"psi/internal/session"
)

// Activation restores the model state and the prompt history from the loaded
// session. Recall is allowed to load user prompts.
func TestPrepareActivationRestoresHistoryAndRecall(t *testing.T) {
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
	act, err := PrepareActivation(opened)
	if err != nil {
		t.Fatal(err)
	}
	if act.Offline {
		t.Fatalf("unexpected offline: %v", act.OfflineErr)
	}
	if len(act.Recall) != 1 || act.Recall[0] != "prompt" {
		t.Fatalf("recall = %v", act.Recall)
	}
}
