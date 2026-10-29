package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"psi/internal/modelmeta"
)

func completionCompat(t *testing.T, fields map[string]any) modelmeta.Compat {
	t.Helper()
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var compat modelmeta.Compat
	if err := json.Unmarshal(encoded, &compat); err != nil {
		t.Fatal(err)
	}
	return compat
}

func completionString(value string) *string { return &value }

func TestDeepSeekCompletionsRequestTranslationAndReplay(t *testing.T) {
	model := ModelMeta{
		ID: "synthetic-deepseek", API: "openai-completions", Provider: "deepseek", BaseURL: "https://api.deepseek.com",
		MaxTokens: 8192, Reasoning: true, InputImage: true,
		ThinkingLevelMap: map[string]*string{"off": nil, "high": completionString("high")},
		Compat: completionCompat(t, map[string]any{
			"supportsStore": false, "supportsDeveloperRole": false, "maxTokensField": "max_tokens",
			"requiresReasoningContentOnAssistantMessages": true, "thinkingFormat": "deepseek", "supportsStrictMode": true,
		}),
	}
	longCallID := "call_1234567890123456789012345678901234567890|fc_abcdef123456789"
	req := Request{Model: model, System: "system rules", Thinking: "high", Tools: []ToolDef{{Name: "read", Description: "read a file", Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}, Messages: []Message{
		{Role: "user", Content: []Block{{Type: "text", Text: "inspect"}, {Type: "image", MimeType: "image/png", Data: "AQID"}}},
		{Role: "assistant", Provider: "deepseek", API: "openai-completions", Model: model.ID, Content: []Block{
			{Type: "thinking", Thinking: "reasoned", ThinkingSignature: "reasoning_content"},
			{Type: "text", Text: "I will inspect."},
			{Type: "toolCall", ID: longCallID, Name: "read", Arguments: map[string]any{"path": "a.txt"}},
		}},
		{Role: "toolResult", ToolCallID: longCallID, ToolName: "read", Content: []Block{{Type: "text", Text: "file content"}}},
	}}
	body, err := BuildCompletionsRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body["model"] != model.ID || body["max_tokens"] != 8192 || body["max_completion_tokens"] != nil || body["store"] != nil {
		t.Fatalf("model-specific request fields = %#v", body)
	}
	if body["thinking"].(map[string]any)["type"] != "enabled" || body["reasoning_effort"] != "high" {
		t.Fatalf("DeepSeek thinking fields = %#v", body)
	}
	if body["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("stream usage options = %#v", body["stream_options"])
	}
	messages := body["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("system role = %#v", messages[0])
	}
	user := messages[1].(map[string]any)
	parts := user["content"].([]any)
	if parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,AQID" {
		t.Fatalf("image translation = %#v", user)
	}
	assistant := messages[2].(map[string]any)
	if assistant["reasoning_content"] != "reasoned" || assistant["content"] != "I will inspect." {
		t.Fatalf("assistant replay = %#v", assistant)
	}
	calls := assistant["tool_calls"].([]any)
	call := calls[0].(map[string]any)
	callID := call["id"].(string)
	if len(callID) > 40 || callID == longCallID {
		t.Fatalf("cross-API call ID was not normalized: %q", callID)
	}
	toolResult := messages[3].(map[string]any)
	if toolResult["tool_call_id"] != callID || toolResult["content"] != "file content" {
		t.Fatalf("tool result link = %#v, call id %q", toolResult, callID)
	}
	tool := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if tool["strict"] != false || tool["name"] != "read" {
		t.Fatalf("tool schema = %#v", tool)
	}

	off := req
	off.Thinking = "off"
	off.Tools = nil
	body, err = BuildCompletionsRequest(off)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := body["thinking"]; exists {
		t.Fatalf("explicit DeepSeek off:null must omit thinking control: %#v", body)
	}
}

func TestZAIThinkingAndToolStreamFollowModelCompat(t *testing.T) {
	tool := ToolDef{Name: "read", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}
	model := ModelMeta{ID: "glm-synthetic", API: "openai-completions", Provider: "zai", MaxTokens: 4000, Reasoning: true,
		ThinkingLevelMap: map[string]*string{"off": completionString("none"), "high": completionString("high")},
		Compat:           completionCompat(t, map[string]any{"maxTokensField": "max_tokens", "thinkingFormat": "zai", "supportsReasoningEffort": false, "zaiToolStream": true, "supportsStrictMode": true})}
	req := Request{Model: model, Thinking: "high", Tools: []ToolDef{tool}, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}}
	body, err := BuildCompletionsRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["clear_thinking"] != false || body["reasoning_effort"] != nil {
		t.Fatalf("Z.AI compatibility fields = %#v, effort %#v", thinking, body["reasoning_effort"])
	}
	if body["tool_stream"] != true {
		t.Fatalf("Z.AI tool stream not enabled with declared tools: %#v", body)
	}
	req.Thinking = "off"
	req.Tools = nil
	body, err = BuildCompletionsRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body["thinking"].(map[string]any)["type"] != "disabled" || body["tool_stream"] != nil || body["reasoning_effort"] != nil {
		t.Fatalf("Z.AI off/toolless request = %#v", body)
	}

	model.Compat = completionCompat(t, map[string]any{"maxTokensField": "max_tokens", "thinkingFormat": "zai", "supportsReasoningEffort": true, "zaiToolStream": true})
	req.Model, req.Thinking, req.Tools = model, "high", []ToolDef{tool}
	body, err = BuildCompletionsRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("mapped Z.AI effort = %#v", body["reasoning_effort"])
	}
}

func TestOpenCodeCompletionsReasoningReplayAndUnsupportedCompat(t *testing.T) {
	model := ModelMeta{ID: "open-code-model", API: "openai-completions", Provider: "opencode-go", MaxTokens: 1024, Reasoning: true,
		Compat: completionCompat(t, map[string]any{"maxTokensField": "max_tokens", "supportsStore": false, "supportsDeveloperRole": false})}
	req := Request{Model: model, Thinking: "off", Messages: []Message{{Role: "assistant", Provider: "opencode-go", API: "openai-completions", Model: model.ID, Content: []Block{
		{Type: "thinking", Thinking: "why", ThinkingSignature: "reasoning"}, {Type: "text", Text: "answer"},
	}}}}
	body, err := BuildCompletionsRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	message := body["messages"].([]any)[0].(map[string]any)
	if message["reasoning_content"] != "why" || message["reasoning"] != nil {
		t.Fatalf("OpenCode reasoning normalization = %#v", message)
	}

	model.Compat = completionCompat(t, map[string]any{"supportsMidConvoSystemMessages": true})
	if _, err := BuildCompletionsRequest(Request{Model: model, Thinking: "off"}); err == nil || !strings.Contains(err.Error(), "supportsMidConvoSystemMessages") {
		t.Fatalf("unsupported compatibility feature accepted: %v", err)
	}
	model.Compat = completionCompat(t, map[string]any{"maxTokensField": "max_tokens", "unknownFutureFlag": true})
	if _, err := BuildCompletionsRequest(Request{Model: model, Thinking: "off"}); err == nil || !strings.Contains(err.Error(), "unknownFutureFlag") {
		t.Fatalf("unknown compatibility feature guessed: %v", err)
	}
}

func TestProviderAPISelectionNeverFallsBackAcrossRegisteredFamilies(t *testing.T) {
	for _, model := range []ModelMeta{
		{ID: "missing-api", Provider: "deepseek"},
		{ID: "wrong-family", Provider: "deepseek", API: "anthropic-messages"},
		{ID: "wrong-codex-api", Provider: "openai-codex", API: "anthropic-messages"},
	} {
		if _, err := BuildRequest(Request{Model: model, Thinking: "off"}); err == nil {
			t.Errorf("provider/API mismatch accepted: %+v", model)
		}
	}
	if _, err := BuildRequest(Request{Model: ModelMeta{ID: "legacy-anthropic"}, Thinking: "off"}); err != nil {
		t.Fatalf("legacy provider-less Anthropic metadata changed: %v", err)
	}
}

func TestCompletionsThinkingDescriptionsReflectWireControls(t *testing.T) {
	deepseek := ModelMeta{ID: "deepseek", API: "openai-completions", Provider: "deepseek", Reasoning: true,
		ThinkingLevelMap: map[string]*string{"off": nil, "high": completionString("high")},
		Compat:           completionCompat(t, map[string]any{"thinkingFormat": "deepseek", "supportsReasoningEffort": true})}
	if got := ThinkingDescription(deepseek, "high"); got != "high (effective effort high)" {
		t.Fatalf("DeepSeek thinking description = %q", got)
	}
	if got := ThinkingDescription(deepseek, "off"); !strings.Contains(got, "disabling reasoning unavailable") {
		t.Fatalf("DeepSeek off description = %q", got)
	}
	zai := ModelMeta{ID: "glm", API: "openai-completions", Provider: "zai", Reasoning: true,
		ThinkingLevelMap: map[string]*string{"off": completionString("none"), "high": completionString("high")},
		Compat:           completionCompat(t, map[string]any{"thinkingFormat": "zai", "supportsReasoningEffort": false})}
	if got := ThinkingDescription(zai, "high"); !strings.Contains(got, "thinking enabled; provider effort default") || strings.Contains(got, "budget") {
		t.Fatalf("Z.AI effort description = %q", got)
	}
	if got := ThinkingDescription(zai, "off"); got != "off" {
		t.Fatalf("Z.AI off description = %q", got)
	}
}

func TestOpenAICompletionsModelSpecificFields(t *testing.T) {
	model := ModelMeta{ID: "feature-flags", API: "openai-completions", Provider: "opencode-go", Reasoning: true, MaxTokens: 3000,
		ThinkingLevelMap: map[string]*string{"off": completionString("none"), "medium": completionString("medium")},
		Compat: completionCompat(t, map[string]any{"maxTokensField": "max_completion_tokens", "supportsStore": true,
			"supportsDeveloperRole": true, "supportsUsageInStreaming": false, "supportsFinishReason": false,
			"supportsStrictMode": false})}
	body, err := BuildCompletionsRequest(Request{Model: model, System: "rules", Thinking: "off", Tools: []ToolDef{{Name: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	if body["max_completion_tokens"] != 3000 || body["max_tokens"] != nil || body["store"] != false || body["stream_options"] != nil {
		t.Fatalf("compatibility request flags = %#v", body)
	}
	if body["reasoning_effort"] != "none" || body["thinking"] != nil {
		t.Fatalf("openai reasoning effort = %#v", body)
	}
	if body["messages"].([]any)[0].(map[string]any)["role"] != "developer" {
		t.Fatalf("developer role not selected: %#v", body["messages"])
	}
	if _, hasStrict := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["strict"]; hasStrict {
		t.Fatalf("unsupported strict field was sent: %#v", body["tools"])
	}
}
