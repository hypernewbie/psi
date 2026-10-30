package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReviewGoogleThinkingMappings(t *testing.T) {
	t.Run("off remains off", func(t *testing.T) {
		m := ModelMeta{Provider: "google", API: "google-generative-ai", ID: "gemini-4-synthetic", Reasoning: true}
		b, e := BuildGoogleRequest(Request{Model: m, Thinking: "off"})
		if e != nil {
			t.Fatal(e)
		}
		if got := googleGenerationConfig(b)["thinkingConfig"].(map[string]any)["thinkingBudget"]; got != 0 {
			t.Fatalf("off budget=%v", got)
		}
	})
	t.Run("clamps to off", func(t *testing.T) {
		m := ModelMeta{Provider: "google", API: "google-generative-ai", ID: "gemini-4-synthetic", Reasoning: true, ThinkingLevelMap: map[string]*string{"minimal": nil, "low": nil, "medium": nil, "high": nil, "xhigh": nil, "max": nil}}
		b, e := BuildGoogleRequest(Request{Model: m, Thinking: "high"})
		if e != nil {
			t.Fatal(e)
		}
		if got := googleGenerationConfig(b)["thinkingConfig"].(map[string]any)["thinkingBudget"]; got != 0 {
			t.Fatalf("clamped off budget=%v", got)
		}
	})
	t.Run("mapped token budget", func(t *testing.T) {
		m := ModelMeta{Provider: "google", API: "google-generative-ai", ID: "gemini-2.5-pro", Reasoning: true, ThinkingLevelMap: map[string]*string{"high": completionString("low")}}
		b, e := BuildGoogleRequest(Request{Model: m, Thinking: "high"})
		if e != nil {
			t.Fatal(e)
		}
		if got := googleGenerationConfig(b)["thinkingConfig"].(map[string]any)["thinkingBudget"]; got != 2048 {
			t.Fatalf("mapped budget=%v", got)
		}
	})
}

func TestReviewCompletionsThinkingClamping(t *testing.T) {
	for _, p := range []string{"deepseek", "zai", "opencode-go"} {
		t.Run(p, func(t *testing.T) {
			m := ModelMeta{Provider: p, API: "openai-completions", ID: "synthetic", Reasoning: true, ThinkingLevelMap: map[string]*string{"off": nil, "minimal": nil, "low": completionString("low")}}
			b, e := BuildCompletionsRequest(Request{Model: m, Thinking: "off"})
			if e != nil {
				t.Fatal(e)
			}
			if p == "opencode-go" {
				if b["reasoning_effort"] != "low" {
					t.Fatalf("off not clamped: %#v", b)
				}
			} else if thinking, _ := b["thinking"].(map[string]any); thinking["type"] != "enabled" {
				t.Fatalf("mandatory thinking disabled: %#v", b)
			}
			if levels := strings.Join(SupportedThinking(m), ","); strings.Contains(levels, "off") || strings.Contains(levels, "max") || strings.Contains(levels, "xhigh") {
				t.Fatalf("unsupported levels advertised: %s", levels)
			}
		})
	}
}

func TestReviewGoogleBufferedBlocksAndPartialSettlement(t *testing.T) {
	data := `data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"a","thoughtSignature":"thought-sig"},{"thought":true,"text":"b"},{"text":"c","thoughtSignature":"text-sig"},{"functionCall":{"name":"read","args":{}}},{"text":"d","thoughtSignature":"last-sig"}]}}]}` + "\n\n"
	for _, interrupted := range []bool{false, true} {
		end := `data: {"candidates":[{"finishReason":"STOP"}]}` + "\n\n"
		if interrupted {
			end = "data: {malformed\n\n"
		}
		r, err := parseGoogleSSE(strings.NewReader(data+end), time.Second, context.Background(), nil, Request{})
		if (err != nil) != interrupted || r.Text != "cd" || r.Thinking != "ab" {
			t.Fatalf("buffer settlement interrupted=%v: %+v %v", interrupted, r, err)
		}
		last := len(r.Content) - 1
		if r.Content[0].ThinkingSignature != "thought-sig" || r.Content[1].TextSignature != "text-sig" || r.Content[last].TextSignature != "last-sig" {
			t.Fatalf("block boundaries or signatures lost: %+v", r.Content)
		}
		if interrupted && (len(r.Content) != 3 || len(r.ToolCalls) != 0) {
			t.Fatalf("partial tool call survived: %+v", r)
		}
		if !interrupted && (len(r.Content) != 4 || len(r.ToolCalls) != 1 || r.Content[2].Type != "toolCall") {
			t.Fatalf("tool/text ordering changed: %+v", r)
		}
	}
}

func TestReviewGoogleMissingArgumentsBecomeObject(t *testing.T) {
	m := ModelMeta{Provider: "google", API: "google-generative-ai", ID: "gemini-3-synthetic"}
	b, e := BuildGoogleRequest(Request{Model: m, Messages: []Message{{Role: "assistant", Provider: m.Provider, API: m.API, Model: m.ID, Content: []Block{{Type: "toolCall", ID: "call", Name: "read"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(b)
	if !strings.Contains(string(raw), `"args":{}`) {
		t.Fatalf("nil arguments were not normalized: %s", raw)
	}
}

func TestReviewCompletionsReasoningDetailsReplay(t *testing.T) {
	m := ModelMeta{Provider: "opencode-go", API: "openai-completions", ID: "synthetic", Reasoning: true}
	s := `data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"a","signature":"sig","format":"opaque"}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"b"},{"type":"reasoning.encrypted","data":"encrypted","id":"rs1"}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}` + "\n\n"
	r, e := parseCompletionsSSE(strings.NewReader(s), time.Second, context.Background(), nil, Request{Model: m})
	if e != nil {
		t.Fatal(e)
	}
	if r.RawStopReason != "stop" {
		t.Fatalf("raw finish reason lost: %q", r.RawStopReason)
	}
	if len(r.Content) != 2 || r.Content[0].Type != "thinking" {
		t.Fatalf("reasoning replay data lost: %+v", r.Content)
	}
	var details []map[string]any
	if e := json.Unmarshal([]byte(r.Content[0].ThinkingSignature), &details); e != nil || len(details) != 2 || details[0]["text"] != "ab" || details[0]["signature"] != "sig" {
		t.Fatalf("reasoning details=%+v: %v", details, e)
	}
	b, e := BuildCompletionsRequest(Request{Model: m, Messages: []Message{{Role: "assistant", Provider: m.Provider, API: m.API, Model: m.ID, Content: r.Content}}})
	if e != nil {
		t.Fatal(e)
	}
	assistant := b["messages"].([]any)[0].(map[string]any)
	raw, _ := json.Marshal(assistant["reasoning_details"])
	if !strings.Contains(string(raw), "encrypted") {
		t.Fatalf("details not replayed: %#v", assistant)
	}
}

func TestReviewCompletionsLegacyReasoningSignature(t *testing.T) {
	m := ModelMeta{Provider: "opencode-go", API: "openai-completions", ID: "synthetic"}
	message := Message{Role: "assistant", Provider: m.Provider, API: m.API, Model: m.ID, Content: []Block{
		{Type: "toolCall", ID: "call", Name: "read", ThoughtSignature: `{"type":"reasoning.encrypted","data":"opaque","id":"rs1","format":"synthetic"}`},
	}}
	for _, sameModel := range []bool{true, false} {
		if !sameModel {
			message.Model = "foreign"
		}
		body, err := BuildCompletionsRequest(Request{Model: m, Messages: []Message{message}})
		if err != nil {
			t.Fatal(err)
		}
		assistant := body["messages"].([]any)[0].(map[string]any)
		raw, _ := json.Marshal(assistant["reasoning_details"])
		if strings.Contains(string(raw), "opaque") != sameModel {
			t.Fatalf("legacy signature replay sameModel=%v: %s", sameModel, raw)
		}
	}
}

func TestReviewCompletionsRepairsInterruptedToolHistory(t *testing.T) {
	m := ModelMeta{Provider: "deepseek", API: "openai-completions", ID: "synthetic"}
	b, err := BuildCompletionsRequest(Request{Model: m, Messages: []Message{
		{Role: "assistant", Provider: m.Provider, API: m.API, Model: m.ID, Content: []Block{{Type: "toolCall", ID: "call", Name: "read", Arguments: map[string]any{}}}},
		{Role: "user", Content: []Block{{Type: "text", Text: "continue"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	messages := b["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("missing Pi synthetic result: %#v", messages)
	}
	result := messages[1].(map[string]any)
	if result["role"] != "tool" || result["tool_call_id"] != "call" || result["content"] != "No result provided" {
		t.Fatalf("bad synthetic result: %#v", result)
	}
}

func TestReviewResponsesDefaultOutputLimit(t *testing.T) {
	m := ModelMeta{Provider: "opencode-go", API: "openai-responses", ID: "synthetic", MaxTokens: 8192}
	b, err := BuildResponsesRequest(Request{Model: m})
	if err != nil || b["max_output_tokens"] != 8192 {
		t.Fatalf("model output limit lost: %#v %v", b, err)
	}
	m.Compat = completionCompat(t, map[string]any{"supportsMaxOutputTokens": false})
	b, err = BuildResponsesRequest(Request{Model: m})
	if err != nil || b["max_output_tokens"] != nil {
		t.Fatalf("unsupported output limit sent: %#v %v", b, err)
	}
}

func TestReviewCompletionsErrorEventIsNotSuccess(t *testing.T) {
	m := ModelMeta{Provider: "opencode-go", API: "openai-completions", ID: "synthetic", Compat: completionCompat(t, map[string]any{"supportsFinishReason": false})}
	s := `data: {"error":{"message":"dummy-secret","code":"bad_request"}}` + "\n\n"
	r, err := parseCompletionsSSE(strings.NewReader(s), time.Second, context.Background(), nil, Request{Model: m})
	if err == nil || strings.Contains(err.Error(), "dummy-secret") || len(r.ToolCalls) > 0 {
		t.Fatalf("error event settled as success or leaked content: %+v %v", r, err)
	}
}

func TestReviewAnthropicUnsupportedCapabilities(t *testing.T) {
	for _, providerID := range []string{"minimax", "opencode-go"} {
		for _, compat := range []map[string]any{{"unknownFutureFlag": true}, {"supportsMidConvoSystemMessages": true}, {"forceAdaptiveThinking": "invalid"}} {
			m := ModelMeta{Provider: providerID, API: "anthropic-messages", ID: "synthetic", Compat: completionCompat(t, compat)}
			if _, err := BuildRequest(Request{Model: m}); err == nil {
				t.Errorf("%s silently accepted %#v", providerID, compat)
			}
		}
	}
}

func TestReviewCompletionsCrossAPIHash(t *testing.T) {
	id := "call_" + strings.Repeat("a", 32) + "|fc_" + strings.Repeat("b", 80)
	m := ModelMeta{Provider: "deepseek", API: "openai-completions", ID: "synthetic"}
	b, e := BuildCompletionsRequest(Request{Model: m, Messages: []Message{{Role: "assistant", Provider: "openai-codex", API: "openai-codex-responses", Model: "foreign", Content: []Block{{Type: "toolCall", ID: id, Name: "read", Arguments: map[string]any{}}}}, {Role: "toolResult", ToolCallID: id, ToolName: "read", Content: []Block{{Type: "text", Text: "ok"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	msgs := b["messages"].([]any)
	call := msgs[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"]
	want := "call_" + strings.Repeat("a", 26) + "_" + piShortHash(id)[:8]
	if call != want || msgs[1].(map[string]any)["tool_call_id"] != want {
		t.Fatalf("tool link differs from Pi: got=%v want=%s", call, want)
	}
}
