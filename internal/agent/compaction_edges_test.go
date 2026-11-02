package agent

import (
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
)

func TestCompactionNoWorkErrorMessages(t *testing.T) {
	tests := []struct {
		disposition CompactionDisposition
		want        string
	}{
		{CompactionEmpty, "nothing to compact: no conversation history"},
		{CompactionWithinBudget, "nothing to compact: recent conversation is already within the retained budget"},
		{CompactionAlreadyDone, "session is already compacted"},
		{CompactionEligible, "nothing to compact: recent conversation is already within the retained budget"},
	}
	for _, tt := range tests {
		if got := (&CompactionNoWorkError{Disposition: tt.disposition}).Error(); got != tt.want {
			t.Errorf("Error(%q) = %q, want %q", tt.disposition, got, tt.want)
		}
	}
}

func TestCompactionSourceNames(t *testing.T) {
	tests := []struct{ reason, want string }{
		{event.CompactionManual, "command"},
		{event.CompactionAdaptive, "automatic"},
		{event.CompactionThreshold, "automatic"},
		{event.CompactionEndTurn, "end-of-turn"},
		{event.CompactionOverflow, "overflow"},
		{"custom-reason", "custom-reason"},
	}
	for _, tt := range tests {
		if got := compactionSourceName(tt.reason); got != tt.want {
			t.Errorf("compactionSourceName(%q) = %q, want %q", tt.reason, got, tt.want)
		}
	}
}

func TestCompactionStringAndContentEstimators(t *testing.T) {
	if got := contentLen("abcd"); got != 4 {
		t.Errorf("contentLen(string) = %d, want 4", got)
	}
	if got := contentLen([]any{"ab", 12}); got != 4 {
		t.Errorf("contentLen([]any) = %d, want 4", got)
	}
	if got := contentLen([]int{1, 2}); got != len("[1 2]") {
		t.Errorf("contentLen(fallback) = %d, want %d", got, len("[1 2]"))
	}

	textBlock := session.UserContentBlock{Type: "text", Text: &session.TextContent{Type: "text", Text: "hi"}}
	imageBlock := session.UserContentBlock{Type: "image", Image: &session.ImageContent{Type: "image", MimeType: "image/png", Data: "synthetic"}}
	if got := piContentChars("abc"); got != 3 {
		t.Errorf("piContentChars(string) = %d, want 3", got)
	}
	if got := piContentChars([]session.UserContentBlock{textBlock, imageBlock}); got != 4_802 {
		t.Errorf("piContentChars(session blocks) = %d, want 4802", got)
	}
	if got := piContentChars([]any{
		map[string]any{"type": "text", "text": "xyz"},
		map[string]any{"type": "image"},
	}); got != 4_803 {
		t.Errorf("piContentChars(generic blocks) = %d, want 4803", got)
	}
	if got := piContentChars(123); got != 0 {
		t.Errorf("piContentChars(fallback) = %d, want 0", got)
	}
	if got := piJSONChars(map[string]any{"key": "value"}); got == 0 {
		t.Error("piJSONChars returned zero for JSON data")
	}
}

func TestPiEstimateTokensCoversContextRoles(t *testing.T) {
	text := &session.TextContent{Type: "text", Text: "four"}
	thinking := &session.ThinkingContent{Type: "thinking", Thinking: "four"}
	call := &session.ToolCall{Type: "toolCall", ID: "call", Name: "read", Arguments: map[string]any{"path": "x"}}
	image := &session.ImageContent{Type: "image", MimeType: "image/png", Data: "eA=="}
	messages := []session.AgentMessage{
		{Role: "system", System: &session.SystemMessage{Role: "system", Content: "rules", Sections: map[string]*string{"extra": stringPointer("section")}}},
		{Role: "user", User: &session.UserMessage{Role: "user", Content: "four"}},
		{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", Content: []session.ContentBlock{{Type: "text", Text: text}, {Type: "thinking", Thinking: thinking}, {Type: "toolCall", Call: call}}}},
		{Role: "toolResult", ToolResult: &session.ToolResultMessage{Role: "toolResult", Content: []session.UserContentBlock{{Type: "text", Text: text}, {Type: "image", Image: image}}}},
		{Role: "custom", Custom: &session.CustomMessage{Content: "custom"}},
		{Role: "bashExecution", BashExecution: &session.BashExecutionMessage{Command: "echo", Output: "ok"}},
		{Role: "branchSummary", BranchSummary: &session.BranchSummaryMessage{Summary: "branch"}},
		{Role: "compactionSummary", CompactionSummary: &session.CompactionSummaryMessage{Summary: "checkpoint"}},
	}
	for _, message := range messages {
		if got := piEstimateTokens(message); got <= 0 {
			t.Errorf("piEstimateTokens(%s) = %d, want a positive estimate", message.Role, got)
		}
	}
	if got := piEstimateTokens(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "😀"}}); got != 1 {
		t.Errorf("emoji token estimate = %d, want one UTF-16-based token", got)
	}
}

func TestCombineProviderUsageOptionalFields(t *testing.T) {
	cacheA, cacheB := int64(3), int64(4)
	reasonA, reasonB := int64(5), int64(6)
	got := combineProviderUsage(provider.Usage{
		Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, TotalTokens: 10,
		CacheWrite1h: &cacheA, Reasoning: &reasonA,
	}, provider.Usage{
		Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40, TotalTokens: 100,
		CacheWrite1h: &cacheB, Reasoning: &reasonB,
	})
	if got.Input != 11 || got.Output != 22 || got.CacheRead != 33 || got.CacheWrite != 44 || got.TotalTokens != 110 {
		t.Fatalf("combined counters = %+v", got)
	}
	if got.CacheWrite1h == nil || *got.CacheWrite1h != 7 || got.Reasoning == nil || *got.Reasoning != 11 {
		t.Fatalf("combined optional counters = %+v", got)
	}
	withoutOptional := combineProviderUsage(provider.Usage{Input: 1}, provider.Usage{Output: 2})
	if withoutOptional.CacheWrite1h != nil || withoutOptional.Reasoning != nil {
		t.Fatalf("absent optional usage became present: %+v", withoutOptional)
	}
}

func stringPointer(s string) *string { return &s }
