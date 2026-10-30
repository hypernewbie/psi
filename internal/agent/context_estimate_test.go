package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
)

func TestContextEstimateIncludesImportedImagesAndActualToolSchemas(t *testing.T) {
	var message session.AgentMessage
	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image","mimeType":"image/png","data":"c3ludGhldGlj"}],"timestamp":1}`), &message); err != nil {
		t.Fatal(err)
	}
	proj := &session.Projection{Flat: []session.AgentMessage{message}}
	small := EstimatedContext(proj, Config{})
	if small < 6000 {
		t.Fatalf("imported image ignored: %d", small)
	}
	cfg := Config{System: strings.Repeat("s", 3000), Tools: []provider.ToolDef{{Name: "read", Description: strings.Repeat("d", 9000), Schema: map[string]any{"type": "object"}}}}
	large := EstimatedContext(proj, cfg)
	if large-small < 4000 {
		t.Fatalf("effective system or schema omitted: %d -> %d", small, large)
	}
}

func TestEstimatorEdgeCases(t *testing.T) {
	e := Estimator{}

	// 1. Nil or empty projection
	toks, conservative := e.Estimate(&session.Projection{}, 0, 0)
	if toks != 0 || !conservative {
		t.Fatalf("empty projection: toks=%d conservative=%v", toks, conservative)
	}

	toksSystem, _ := e.Estimate(&session.Projection{}, 300, 4)
	expectedBase := 300/3 + 4*250 // 100 + 1000 = 1100
	if toksSystem != expectedBase {
		t.Fatalf("system + tool calculation = %d, want %d", toksSystem, expectedBase)
	}

	// 2. Nil pointers inside messages (must not panic)
	nilMessages := []session.AgentMessage{
		{Role: "system", System: nil},
		{Role: "user", User: nil},
		{Role: "assistant", Assistant: nil},
		{Role: "toolResult", ToolResult: nil},
		{Role: "custom"},
		{Role: "unrecognized_role"},
	}
	projNil := &session.Projection{Flat: nilMessages}
	toksNil, _ := e.Estimate(projNil, 0, 0)
	if toksNil < 0 {
		t.Fatalf("negative tokens on nil messages: %d", toksNil)
	}

	// 3. Thinking blocks with and without ThinkingSignature
	sig := "sig-hash-123"
	assistantWithThinking := session.AgentMessage{
		Role: "assistant",
		Assistant: &session.AssistantMessage{
			Role: "assistant",
			Content: []session.ContentBlock{
				{Type: "text", Text: &session.TextContent{Type: "text", Text: "answer"}},
				{Type: "thinking", Thinking: &session.ThinkingContent{Type: "thinking", Thinking: strings.Repeat("t", 400), ThinkingSignature: &sig}},
			},
		},
	}
	assistantWithoutSig := session.AgentMessage{
		Role: "assistant",
		Assistant: &session.AssistantMessage{
			Role: "assistant",
			Content: []session.ContentBlock{
				{Type: "thinking", Thinking: &session.ThinkingContent{Type: "thinking", Thinking: strings.Repeat("t", 400), ThinkingSignature: nil}},
			},
		},
	}
	projThinking := &session.Projection{Flat: []session.AgentMessage{assistantWithThinking}}
	toksThinking, _ := e.Estimate(projThinking, 0, 0)
	projNoSig := &session.Projection{Flat: []session.AgentMessage{assistantWithoutSig}}
	toksNoSig, _ := e.Estimate(projNoSig, 0, 0)

	if toksThinking <= toksNoSig {
		t.Fatalf("thinking with signature (%d) should be > without signature (%d)", toksThinking, toksNoSig)
	}

	// 4. Tool call arguments variation
	assistantWithCalls := session.AgentMessage{
		Role: "assistant",
		Assistant: &session.AssistantMessage{
			Role: "assistant",
			Content: []session.ContentBlock{
				{Type: "toolCall", Call: nil}, // nil call must not panic
				{Type: "toolCall", Call: &session.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{"cmd": "echo 1"}}},
				{Type: "toolCall", Call: &session.ToolCall{ID: "c2", Name: "custom", Arguments: map[string]any{"list": []any{1, 2, "arg"}}}},
			},
		},
	}
	projCalls := &session.Projection{Flat: []session.AgentMessage{assistantWithCalls}}
	toksCalls, _ := e.Estimate(projCalls, 0, 0)
	if toksCalls <= 0 {
		t.Fatalf("tool call tokens should be positive, got %d", toksCalls)
	}

	// 5. ToolResult with image and text
	toolResultMsg := session.AgentMessage{
		Role: "toolResult",
		ToolResult: &session.ToolResultMessage{
			Role:       "toolResult",
			ToolCallID: "c1",
			Content: []session.UserContentBlock{
				{Type: "text", Text: &session.TextContent{Type: "text", Text: "text output"}},
				{Type: "image", Image: &session.ImageContent{Type: "image", Data: "abcd", MimeType: "image/png"}},
				{Type: "other"}, // nil text and image must not panic
			},
		},
	}
	projResult := &session.Projection{Flat: []session.AgentMessage{toolResultMsg}}
	toksResult, _ := e.Estimate(projResult, 0, 0)
	if toksResult < 6000 {
		t.Fatalf("tool result with image should be >= 6000, got %d", toksResult)
	}

	// 6. User message with complex structures, emojis and unicode
	userMsgComplex := session.AgentMessage{
		Role: "user",
		User: &session.UserMessage{
			Role:    "user",
			Content: "Hello world 🚀 🌟 🌍 " + strings.Repeat("世界 ", 100),
		},
	}
	projUser := &session.Projection{Flat: []session.AgentMessage{userMsgComplex}}
	toksUser, _ := e.Estimate(projUser, 0, 0)
	if toksUser <= 10 {
		t.Fatalf("unicode user message should have positive tokens: %d", toksUser)
	}

	// 7. User message with raw fallback
	userMsgRaw := session.AgentMessage{
		Role: "user",
		User: &session.UserMessage{
			Role: "user",
			Raw:  json.RawMessage(`{"invalid": json content [}`),
		},
	}
	projRaw := &session.Projection{Flat: []session.AgentMessage{userMsgRaw}}
	toksRaw, _ := e.Estimate(projRaw, 0, 0)
	if toksRaw <= 0 {
		t.Fatalf("raw fallback should produce positive tokens: %d", toksRaw)
	}
}

func TestShouldCompactBoundariesAndOverflow(t *testing.T) {
	// 1. Disabled
	if ShouldCompact(500000, 100000, 10000, false) {
		t.Fatal("ShouldCompact must be false when enabled=false")
	}

	// 2. Zero or negative context window
	if ShouldCompact(100, 0, 10, true) {
		t.Fatal("ShouldCompact must be false when contextWindow <= 0")
	}
	if ShouldCompact(100, -100, 10, true) {
		t.Fatal("ShouldCompact must be false when contextWindow < 0")
	}

	// 3. Negative reserve is clamped to 0
	// contextWindow = 1000, reserve = -50 -> threshold is 1000 - 0 = 1000
	if ShouldCompact(1000, 1000, -50, true) {
		t.Fatal("1000 not > 1000")
	}
	if !ShouldCompact(1001, 1000, -50, true) {
		t.Fatal("1001 > 1000 should be true")
	}

	// 4. Reserve > contextWindow: exceeds threshold even for 0 tokens
	// contextWindow = 1000, reserve = 1500 -> threshold = 1000 - 1500 = -500
	if !ShouldCompact(0, 1000, 1500, true) {
		t.Fatal("reserve > contextWindow should trigger even for 0 tokens")
	}

	// 5. Strict boundary check
	// contextWindow = 200000, reserve = 20000 -> threshold = 180000
	if ShouldCompact(179999, 200000, 20000, true) {
		t.Fatal("179999 should not trigger")
	}
	if ShouldCompact(180000, 200000, 20000, true) {
		t.Fatal("180000 exactly at threshold should not trigger (must be >)")
	}
	if !ShouldCompact(180001, 200000, 20000, true) {
		t.Fatal("180001 > 180000 must trigger")
	}

	// 6. Large numbers / overflow safety
	largeWindow := 10_000_000
	largeReserve := 2_000_000
	largeEstimated := 8_500_000
	if !ShouldCompact(largeEstimated, largeWindow, largeReserve, true) {
		t.Fatal("large numbers ShouldCompact failed")
	}
}
