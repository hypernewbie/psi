package session

import "testing"

func TestTransformMessagesForAPIRepairsOrphanedCallsLikePi(t *testing.T) {
	call := func(id, name string) ContentBlock {
		return ContentBlock{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: id, Name: name}}
	}
	messages := []AgentMessage{
		{Role: "user", User: &UserMessage{Role: "user", Content: "start"}},
		{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", StopReason: "toolUse", Content: []ContentBlock{call("done", "read"), call("missing", "bash")}}},
		{Role: "system", System: &SystemMessage{Role: "system", Content: "declaration"}},
		{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult", ToolCallID: "done", ToolName: "read", Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}}}},
		{Role: "user", User: &UserMessage{Role: "user", Content: "continue"}},
	}

	got := TransformMessagesForAPI(messages)
	if len(got) != 6 {
		t.Fatalf("transformed message count = %d, want 6: %#v", len(got), got)
	}
	if got[2].ToolResult == nil || got[2].ToolResult.ToolCallID != "done" {
		t.Fatalf("durable result position = %#v", got[2])
	}
	if got[3].Role != "toolResult" || got[3].ToolResult.ToolCallID != "missing" || got[3].ToolResult.ToolName != "bash" || !got[3].ToolResult.IsError || got[3].ToolResult.Content[0].Text.Text != "No result provided" {
		t.Fatalf("synthetic result = %#v", got[3])
	}
	if got[4].Role != "system" || got[5].Role != "user" {
		t.Fatalf("system/user ordering = %#v", got)
	}
	if len(UnresolvedToolCalls(got)) != 0 {
		t.Fatal("transformed API context still has missing calls")
	}
	if len(messages) != 5 {
		t.Fatal("transform mutated its input")
	}
}

func TestTransformMessagesForAPISkipsAbortedAssistants(t *testing.T) {
	messages := []AgentMessage{
		{Role: "user", User: &UserMessage{Role: "user", Content: "start"}},
		{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", StopReason: "aborted", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "partial"}}}}},
		{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", StopReason: "error", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "failed"}}}}},
		{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", StopReason: "stop", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "kept"}}}}},
	}
	got := TransformMessagesForAPI(messages)
	if len(got) != 2 || got[0].Role != "user" || got[1].Assistant == nil || got[1].Assistant.Content[0].Text.Text != "kept" {
		t.Fatalf("transformed messages = %#v", got)
	}
}
