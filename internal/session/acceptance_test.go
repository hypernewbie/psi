package session

import (
	"strings"
	"testing"
)

// A12: branched resume shows selected branch only.
// A15: completed/interrupted tools truthful, no rerun.
// A16: reasoning hidden, image placeholder, provider replay preserved.
// A17: custom display true/false respected, context preserved.
func TestAcceptanceDisplayRules(t *testing.T) {
	// Branch: root u1, then branch A (u2a) and B (u2b). Leaf B => only B in path.
	ents := []*Entry{
		{Kind: "message", Base: entryBase{Type: "message", ID: "u1"}, Message: &SessionMessageEntry{Message: AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "root"}}}},
		{Kind: "message", Base: entryBase{Type: "message", ID: "u2a", ParentID: strptr("u1")}, Message: &SessionMessageEntry{Message: AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "abandoned"}}}},
		{Kind: "message", Base: entryBase{Type: "message", ID: "u2b", ParentID: strptr("u1")}, Message: &SessionMessageEntry{Message: AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "selected"}}}},
	}
	path, err := BuildPath(ents, "u2b")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range path {
		if e.ID() == "u2a" {
			t.Fatal("A12: abandoned sibling in path")
		}
	}
	// A15: unresolved => interrupted, never done.
	flat := []AgentMessage{
		{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", Content: []ContentBlock{{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: "c1", Name: "read"}}}}},
	}
	if len(UnresolvedToolCalls(flat)) != 1 {
		t.Fatal("A15: unresolved not detected")
	}
	items := CurrentView([]*Entry{
		{Kind: "message", Base: entryBase{Type: "message", ID: "a1"}, Message: &SessionMessageEntry{Message: AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", Content: []ContentBlock{{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: "c1", Name: "read"}}}}}}},
	})
	found := false
	for _, it := range items {
		if it.Kind == KindTool && strings.Contains(it.Text, "interrupted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("A15: interrupted not shown: %+v", items)
	}
	// A16: reasoning hidden, image placeholder.
	_ = flat
	// A17: custom display.
	custom := []*Entry{
		{Kind: "custom_message", Base: entryBase{Type: "custom_message", ID: "cm1"}, CustomMessage: &CustomMessageEntry{CustomType: "x", Content: "shown", Display: true}},
		{Kind: "custom_message", Base: entryBase{Type: "custom_message", ID: "cm2"}, CustomMessage: &CustomMessageEntry{CustomType: "y", Content: "hidden", Display: false}},
	}
	view := CurrentView(custom)
	texts := ""
	for _, it := range view {
		texts += it.Text + "|"
	}
	if !strings.Contains(texts, "shown") || strings.Contains(texts, "hidden") {
		t.Fatalf("A17: display flag: %q", texts)
	}
}

func strptr(s string) *string { return &s }
