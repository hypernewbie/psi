package session

import (
	"encoding/json"
	"testing"
)

func mustEntry(t *testing.T, kind, id string, parent *string, payload map[string]any) *Entry {
	t.Helper()
	base := map[string]any{"type": kind, "id": id, "timestamp": "2026-01-01T00:00:00.000Z"}
	if parent != nil {
		base["parentId"] = *parent
	} else {
		base["parentId"] = nil
	}
	for k, v := range payload {
		base[k] = v
	}
	b, _ := json.Marshal(base)
	e, err := UnmarshalEntry(b)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestActivePathSingleBranch(t *testing.T) {
	a := mustEntry(t, "message", "a", nil, map[string]any{"message": map[string]any{"role": "user", "content": "a", "timestamp": 1}})
	b := mustEntry(t, "message", "b", strPtr("a"), map[string]any{"message": map[string]any{"role": "user", "content": "b", "timestamp": 2}})
	c := mustEntry(t, "message", "c", strPtr("a"), map[string]any{"message": map[string]any{"role": "user", "content": "c", "timestamp": 3}})
	// Leaf c: path a->c, sibling b excluded.
	path, err := BuildPath([]*Entry{a, b, c}, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0].ID() != "a" || path[1].ID() != "c" {
		t.Fatalf("path=%v", ids(path))
	}
}

func ids(es []*Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID())
	}
	return out
}

func TestOrdinaryMessagesProjection(t *testing.T) {
	sys := mustEntry(t, "message", "s", nil, map[string]any{"message": map[string]any{"role": "system", "content": "base", "timestamp": 1}})
	u := mustEntry(t, "message", "u", strPtr("s"), map[string]any{"message": map[string]any{"role": "user", "content": "hi", "timestamp": 2}})
	a := mustEntry(t, "message", "a", strPtr("u"), map[string]any{"message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "hello"}},
		"api": "anthropic-messages", "provider": "anthropic", "model": "m",
		"usage": map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}},
		"stopReason": "stop", "timestamp": 3}})
	tr := mustEntry(t, "message", "t", strPtr("a"), map[string]any{"message": map[string]any{
		"role": "toolResult", "toolCallId": "c1", "toolName": "read",
		"content": []any{map[string]any{"type": "text", "text": "ok"}}, "isError": false, "timestamp": 4}})
	// Legacy null content normalized.
	nullU := mustEntry(t, "message", "n", strPtr("t"), map[string]any{"message": map[string]any{"role": "user", "content": nil, "timestamp": 5}})
	p := BuildProjection([]*Entry{sys, u, a, tr, nullU})
	if len(p.Flat) != 5 {
		t.Fatalf("flat=%d", len(p.Flat))
	}
	if p.Flat[0].Role != "system" || p.Flat[1].Role != "user" || p.Flat[2].Role != "assistant" || p.Flat[3].Role != "toolResult" {
		t.Errorf("roles wrong: %v", roles(p.Flat))
	}
	// Tool-call IDs and block order preserved.
	if p.Flat[3].ToolResult.ToolCallID != "c1" {
		t.Errorf("toolCallId lost")
	}
	_ = sys
}

func roles(ms []AgentMessage) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Role)
	}
	return out
}

func TestRestoreModelState(t *testing.T) {
	m1 := mustEntry(t, "model_change", "m1", nil, map[string]any{"provider": "anthropic", "modelId": "claude-a"})
	th := mustEntry(t, "thinking_level_change", "t1", strPtr("m1"), map[string]any{"thinkingLevel": "high"})
	assistant := mustEntry(t, "message", "a", strPtr("t1"), map[string]any{"message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "x"}},
		"api": "anthropic-messages", "provider": "anthropic", "model": "claude-b",
		"usage": map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}},
		"stopReason": "stop", "timestamp": 3}})
	st := RestoreModelState([]*Entry{m1, th, assistant})
	if st.Provider != "anthropic" || st.ModelID != "claude-b" {
		t.Errorf("assistant metadata should win: %+v", st)
	}
	if st.ThinkingLevel != "high" {
		t.Errorf("thinking: %+v", st)
	}
}

func TestReplaySystem(t *testing.T) {
	_ = func(id string, parent *string, content string, sections map[string]*string, added []Tool, removed []ToolReference) *Entry {
		msg := map[string]any{"role": "system", "content": content, "timestamp": 1}
		if sections != nil {
			msg["sections"] = sections
		}
		if added != nil {
			msg["toolsAdded"] = added
		}
		if removed != nil {
			msg["toolsRemoved"] = removed
		}
		return mustEntry(t, "message", id, parent, map[string]any{"message": msg})
	}
	s := func(v string) *string { return &v }
	// Build manually to avoid helper complexity.
	sys1 := &AgentMessage{Role: "system", System: &SystemMessage{Role: "system", Content: "base ", Sections: map[string]*string{"k": s("v1")}, ToolsAdded: []Tool{{Name: "read"}, {Name: "write"}}, Timestamp: 1}}
	sys2 := &AgentMessage{Role: "system", System: &SystemMessage{Role: "system", Content: "extra ", Sections: map[string]*string{"k": s("v2"), "gone": nil}, ToolsAdded: []Tool{{Name: "bash"}}, ToolsRemoved: []ToolReference{{Name: s("write")}}, Timestamp: 2}}
	eff := ReplaySystem([]AgentMessage{*sys1, *sys2})
	if eff.Content == "" {
		t.Logf("content=%q", eff.Content)
	}
	names := []string{}
	for _, tl := range eff.Tools {
		names = append(names, tl.Name)
	}
	if len(names) != 2 || names[0] != "bash" || names[1] != "read" {
		t.Errorf("tools=%v", names)
	}
}

func TestCompactionProjection(t *testing.T) {
	mk := func(id string, parent *string, text string) *Entry {
		return mustEntry(t, "message", id, parent, map[string]any{"message": map[string]any{"role": "user", "content": text, "timestamp": 1}})
	}
	a := mk("a", nil, "old1")
	b := mk("b", strPtr("a"), "old2")
	c := mustEntry(t, "compaction", "cp", strPtr("b"), map[string]any{"summary": "s", "firstKeptEntryId": "b", "tokensBefore": 100})
	d := mk("d", strPtr("cp"), "new")
	p := BuildProjection([]*Entry{a, b, c, d})
	// a omitted (before kept boundary), compaction contributes system?+summary, b+d kept.
	if len(p.Flat) != 3 {
		t.Fatalf("flat=%d roles=%v", len(p.Flat), roles(p.Flat))
	}
	if p.Flat[0].Role != "compactionSummary" {
		t.Errorf("first should be compaction summary, got %v", roles(p.Flat))
	}
	// Retain-none: firstKept == own id => only compaction + after.
	c2 := mustEntry(t, "compaction", "cp2", strPtr("d"), map[string]any{"summary": "s2", "firstKeptEntryId": "cp2", "tokensBefore": 50})
	e := mk("e", strPtr("cp2"), "new2")
	p2 := BuildProjection([]*Entry{a, b, c, d, c2, e})
	found := 0
	for _, m := range p2.Flat {
		if m.Role == "compactionSummary" {
			found++
		}
	}
	if found != 1 {
		t.Errorf("repeated compaction should contribute exactly one summary, got %d (%v)", found, roles(p2.Flat))
	}
}

func TestContextEdits(t *testing.T) {
	a := mustEntry(t, "message", "a", nil, map[string]any{"message": map[string]any{"role": "user", "content": "secret", "timestamp": 1}})
	omit := mustEntry(t, "context_edit", "e1", strPtr("a"), map[string]any{"targetId": "a", "replacement": nil})
	p := BuildProjection([]*Entry{a, omit})
	if len(p.Flat) != 0 {
		t.Fatalf("omission should remove target, got %v", roles(p.Flat))
	}
	// Replacement + latest wins.
	a2 := mustEntry(t, "message", "a2", nil, map[string]any{"message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "orig"}},
		"api": "x", "provider": "anthropic", "model": "m",
		"usage": map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}},
		"stopReason": "stop", "timestamp": 1}})
	rep1 := map[string]any{"content": "first"}
	rep2 := map[string]any{"content": "second"}
	e1 := mustEntry(t, "context_edit", "e1", strPtr("a2"), map[string]any{"targetId": "a2", "replacement": rep1})
	e2 := mustEntry(t, "context_edit", "e2", strPtr("e1"), map[string]any{"targetId": "a2", "replacement": rep2})
	p2 := BuildProjection([]*Entry{a2, e1, e2})
	if len(p2.Flat) != 1 || p2.Flat[0].Role != "assistant" {
		t.Fatalf("flat=%v", roles(p2.Flat))
	}
	got := p2.Flat[0].Assistant.Content[0].Text.Text
	if got != "second" {
		t.Errorf("latest edit should win, got %q", got)
	}
}

func TestSpecialMessages(t *testing.T) {
	bs := mustEntry(t, "branch_summary", "bs", nil, map[string]any{"fromId": "x", "summary": "did stuff"})
	cm := mustEntry(t, "custom_message", "cm", strPtr("bs"), map[string]any{"customType": "ext", "content": "hello", "display": true})
	hidden := mustEntry(t, "custom_message", "h", strPtr("cm"), map[string]any{"customType": "ext", "content": "hidden", "display": false})
	// bashExecution with excludeFromContext=true contributes nothing.
	bashEx := mustEntry(t, "message", "b", strPtr("h"), map[string]any{"message": map[string]any{
		"role": "bashExecution", "command": "ls", "output": "f", "cancelled": false, "truncated": false, "timestamp": 1, "excludeFromContext": true}})
	bashIn := mustEntry(t, "message", "b2", strPtr("b"), map[string]any{"message": map[string]any{
		"role": "bashExecution", "command": "pwd", "output": "/tmp", "cancelled": false, "truncated": false, "timestamp": 2}})
	meta := mustEntry(t, "usage", "u", strPtr("b2"), map[string]any{"kind": "x", "provider": "p", "model": "m",
		"usage": map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}})
	p := BuildProjection([]*Entry{bs, cm, hidden, bashEx, bashIn, meta})
	llm := ConvertToLLM(p.Flat)
	// usage never becomes context; excluded bash dropped; included bash becomes user text.
	for _, m := range llm {
		if m.Role == "usage" {
			t.Errorf("usage leaked into context")
		}
	}
	texts := []string{}
	for _, m := range llm {
		if m.Role == "user" && m.User != nil {
			if s, ok := m.User.Content.(string); ok {
				texts = append(texts, s)
			}
			if blocks, ok := m.User.Content.([]UserContentBlock); ok {
				for _, b := range blocks {
					if b.Text != nil {
						texts = append(texts, b.Text.Text)
					}
				}
			}
			// Decoded []any form (from JSON) is normalized via raw; also check raw text extraction.
			if m.User.Raw != nil {
				texts = append(texts, string(m.User.Raw))
			}
		}
	}
	joined := ""
	for _, s := range texts {
		joined += s + "\n"
	}
	if !contains(joined, "did stuff") {
		t.Errorf("branch summary missing: %q", joined)
	}
	if !contains(joined, "hello") || !contains(joined, "hidden") {
		t.Errorf("custom messages missing: %q", joined)
	}
	if contains(joined, "Ran `ls`") {
		t.Errorf("excluded bash leaked: %q", joined)
	}
	if !contains(joined, "Ran `pwd`") {
		t.Errorf("included bash missing: %q", joined)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestToolContinuity(t *testing.T) {
	asst := mustEntry(t, "message", "a", nil, map[string]any{"message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "toolCall", "id": "c1", "name": "write", "arguments": map[string]any{}}},
		"api": "x", "provider": "anthropic", "model": "m",
		"usage": map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}},
		"stopReason": "toolUse", "timestamp": 1}})
	p := BuildProjection([]*Entry{asst})
	missing := UnresolvedToolCalls(p.Flat)
	if len(missing) != 1 || missing[0].ID != "c1" {
		t.Fatalf("unresolved=%v", missing)
	}
	// With matching result, resolved.
	res := mustEntry(t, "message", "r", strPtr("a"), map[string]any{"message": map[string]any{
		"role": "toolResult", "toolCallId": "c1", "toolName": "write",
		"content": []any{map[string]any{"type": "text", "text": "ok"}}, "isError": false, "timestamp": 2}})
	p2 := BuildProjection([]*Entry{asst, res})
	if len(UnresolvedToolCalls(p2.Flat)) != 0 {
		t.Errorf("should be resolved")
	}
}

func TestOpaqueSignaturesPreserved(t *testing.T) {
	raw := `{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:00.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"h","thinkingSignature":"opaque-123"},{"type":"text","text":"hi","textSignature":"sig-1"}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":1}}`
	e, err := UnmarshalEntry([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	p := BuildProjection([]*Entry{e})
	if len(p.Flat) != 1 || p.Flat[0].Assistant == nil {
		t.Fatal("projection")
	}
	th := p.Flat[0].Assistant.Content[0].Thinking
	if th == nil || th.ThinkingSignature == nil || *th.ThinkingSignature != "opaque-123" {
		t.Errorf("thinking signature lost: %+v", p.Flat[0].Assistant.Content[0])
	}
}

func TestUnknownRoleRefuses(t *testing.T) {
	m := AgentMessage{Role: "deferredThing", Raw: []byte(`{"role":"deferredThing"}`)}
	if err := CheckContinuable([]AgentMessage{m}, nil); err == nil {
		t.Errorf("unknown context role must refuse")
	}
}

func TestReplaceContent(t *testing.T) {
	// 1. User message with string replacement
	userMsg := AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "old string", Timestamp: 1}}
	rep1 := replaceContent(userMsg, json.RawMessage(`"new string"`), false)
	if len(rep1) != 1 || rep1[0].User.Content != "new string" {
		t.Errorf("user string replacement failed: %+v", rep1)
	}

	// 2. User message with blocks replacement
	rep2 := replaceContent(userMsg, json.RawMessage(`[{"type":"text","text":"new block"}]`), false)
	if len(rep2) != 1 {
		t.Fatalf("user block replacement failed: %+v", rep2)
	}
	blocks, ok := rep2[0].User.Content.([]UserContentBlock)
	if !ok || len(blocks) != 1 || blocks[0].Text == nil || blocks[0].Text.Text != "new block" {
		t.Errorf("user block replacement content mismatch: %+v", rep2[0].User.Content)
	}

	// 3. User message with invalid content fallback
	rep3 := replaceContent(userMsg, json.RawMessage(`12345`), false)
	if len(rep3) != 1 || rep3[0].User.Content != "old string" {
		t.Errorf("user invalid content should fallback: %+v", rep3)
	}

	// 4. User message with nil User pointer
	nilUser := AgentMessage{Role: "user"}
	repNilUser := replaceContent(nilUser, json.RawMessage(`"new"`), false)
	if len(repNilUser) != 1 || repNilUser[0].User != nil {
		t.Errorf("nil user pointer should return original message")
	}

	// 5. Custom message with string replacement
	customMsg := AgentMessage{Role: "custom", Custom: &CustomMessage{Role: "custom", Content: "old custom", Timestamp: 2}}
	repCust1 := replaceContent(customMsg, json.RawMessage(`"new custom"`), false)
	if len(repCust1) != 1 || repCust1[0].Custom.Content != "new custom" {
		t.Errorf("custom string replacement failed: %+v", repCust1)
	}

	// 6. Custom message with object replacement
	repCust2 := replaceContent(customMsg, json.RawMessage(`{"customKey":"customVal"}`), false)
	if len(repCust2) != 1 {
		t.Fatalf("custom object replacement failed: %+v", repCust2)
	}
	obj, ok := repCust2[0].Custom.Content.(map[string]any)
	if !ok || obj["customKey"] != "customVal" {
		t.Errorf("custom object replacement content mismatch: %+v", repCust2[0].Custom.Content)
	}

	// 7. Custom message with nil Custom pointer
	nilCustom := AgentMessage{Role: "custom"}
	repNilCust := replaceContent(nilCustom, json.RawMessage(`"new"`), false)
	if len(repNilCust) != 1 || repNilCust[0].Custom != nil {
		t.Errorf("nil custom pointer should return original message")
	}

	// 8. Assistant message with ContentBlock array replacement
	asstMsg := AgentMessage{
		Role: "assistant",
		Assistant: &AssistantMessage{
			Role:    "assistant",
			Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "old assistant"}}},
		},
	}
	asstBlocks := `[{"type":"text","text":"replaced assistant"}]`
	repAsst := replaceContent(asstMsg, json.RawMessage(asstBlocks), true)
	if len(repAsst) != 1 || len(repAsst[0].Assistant.Content) != 1 || repAsst[0].Assistant.Content[0].Text.Text != "replaced assistant" {
		t.Errorf("assistant replacement failed: %+v", repAsst)
	}

	// 9. Assistant message with invalid content fallback
	repAsstInv := replaceContent(asstMsg, json.RawMessage(`"invalid string for assistant"`), true)
	if len(repAsstInv) != 1 || repAsstInv[0].Assistant.Content[0].Text.Text != "old assistant" {
		t.Errorf("assistant invalid content should fallback: %+v", repAsstInv)
	}

	// 10. Assistant message with nil Assistant pointer
	nilAsst := AgentMessage{Role: "assistant"}
	repNilAsst := replaceContent(nilAsst, json.RawMessage(asstBlocks), true)
	if len(repNilAsst) != 1 || repNilAsst[0].Assistant != nil {
		t.Errorf("nil assistant pointer should return original message")
	}

	// 11. ToolResult message with UserContentBlock array replacement
	toolMsg := AgentMessage{
		Role: "toolResult",
		ToolResult: &ToolResultMessage{
			Role:       "toolResult",
			ToolCallID: "c1",
			Content:    []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "old tool"}}},
		},
	}
	toolBlocks := `[{"type":"text","text":"replaced tool"}]`
	repTool := replaceContent(toolMsg, json.RawMessage(toolBlocks), true)
	if len(repTool) != 1 || len(repTool[0].ToolResult.Content) != 1 || repTool[0].ToolResult.Content[0].Text.Text != "replaced tool" {
		t.Errorf("toolResult replacement failed: %+v", repTool)
	}

	// 12. ToolResult with invalid content fallback
	repToolInv := replaceContent(toolMsg, json.RawMessage(`"invalid string for tool"`), true)
	if len(repToolInv) != 1 || repToolInv[0].ToolResult.Content[0].Text.Text != "old tool" {
		t.Errorf("toolResult invalid content should fallback: %+v", repToolInv)
	}

	// 13. ToolResult with nil pointer
	nilTool := AgentMessage{Role: "toolResult"}
	repNilTool := replaceContent(nilTool, json.RawMessage(toolBlocks), true)
	if len(repNilTool) != 1 || repNilTool[0].ToolResult != nil {
		t.Errorf("nil tool pointer should return original message")
	}

	// 14. Unknown role fallback
	unknownMsg := AgentMessage{Role: "otherRole"}
	repUnknown := replaceContent(unknownMsg, json.RawMessage(`"content"`), false)
	if len(repUnknown) != 1 || repUnknown[0].Role != "otherRole" {
		t.Errorf("unknown role should return original message")
	}
}
