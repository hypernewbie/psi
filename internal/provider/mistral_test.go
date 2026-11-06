package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/modelmeta"
)

func mistralTestModel() ModelMeta {
	return ModelMeta{ID: "devstral-medium-latest", API: "mistral-conversations", Provider: "mistral",
		MaxTokens: 131072, Reasoning: true,
		ThinkingLevelMap: map[string]*string{"off": strptr("none"), "low": strptr("low"), "medium": strptr("medium"), "high": strptr("high")}}
}

func TestMistralRequestReasoningEffortAndPromptMode(t *testing.T) {
	body, err := BuildMistralRequest(Request{Model: mistralTestModel(), Thinking: "high", System: "rules",
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if body["reasoningEffort"] != "high" || body["promptMode"] != nil {
		t.Fatalf("effort mapping = %v %v", body["reasoningEffort"], body["promptMode"])
	}
	body, err = BuildMistralRequest(Request{Model: mistralTestModel(), Thinking: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if body["reasoningEffort"] != "none" {
		t.Fatalf("off effort = %v", body["reasoningEffort"])
	}
	// Reasoning model without a thinking map uses prompt_mode.
	withoutMap := mistralTestModel()
	withoutMap.ThinkingLevelMap = nil
	body, err = BuildMistralRequest(Request{Model: withoutMap, Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if body["promptMode"] != "reasoning" || body["reasoningEffort"] != nil {
		t.Fatalf("prompt mode fallback = %v %v", body["promptMode"], body["reasoningEffort"])
	}
	// Unmapped requested level falls back to "high".
	withoutMap = mistralTestModel()
	withoutMap.ThinkingLevelMap = map[string]*string{"off": strptr("none")}
	body, err = BuildMistralRequest(Request{Model: withoutMap, Thinking: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if body["reasoningEffort"] != "high" {
		t.Fatalf("fallback effort = %v", body["reasoningEffort"])
	}
}

func TestMistralToolCallIDsAreNineAlphanumerics(t *testing.T) {
	var requests int64
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("endpoint path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Affinity") != "synthetic-session" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("headers = %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"id\":\"resp-mistral\",\"choices\":[{\"delta\":{\"content\":[{\"type\":\"thinking\",\"thinking\":[{\"text\":\"why\"}]}]},\"finish_reason\":null}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"a\\\"}\"}}]},\"finish_reason\":null}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\n"+
				"data: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewMistralClient(server.URL, "synthetic-key", 0, time.Second)
	client.SessionID = "synthetic-session"
	history := []Message{
		{Role: "assistant", Provider: "mistral", API: "mistral-conversations", Model: "devstral-medium-latest",
			Content: []Block{{Type: "toolCall", ID: "call-with-a-really-long-identifier", Name: "read", Arguments: map[string]any{"path": "a.txt"}}}},
		{Role: "toolResult", ToolCallID: "call-with-a-really-long-identifier", ToolName: "read", Content: []Block{{Type: "text", Text: "contents"}}},
		{Role: "user", Content: []Block{{Type: "text", Text: "continue"}}},
	}
	var streamed strings.Builder
	response, err := client.Send(context.Background(), Request{Model: mistralTestModel(), Thinking: "off", Messages: history}, TextOnly(func(delta string) { streamed.WriteString(delta) }))
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt64(&requests) != 1 || response.ResponseID != "resp-mistral" || response.StopReason != "toolUse" {
		t.Fatalf("response = %+v", response)
	}
	if response.Thinking != "why" || response.Text != "answer" || streamed.String() != "answer" {
		t.Fatalf("content = %+v", response.Content)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "read" || response.ToolCalls[0].Arguments["path"] != "a" {
		t.Fatalf("tool calls = %+v", response.ToolCalls)
	}
	if id := response.ToolCalls[0].ID; len(id) != 9 || !isAllASCIIAlnum(id) {
		t.Fatalf("streamed tool call ID %q is not nine alphanumerics", id)
	}
	if response.Usage.Input != 6 || response.Usage.Output != 4 || response.Usage.CacheRead != 4 || response.Usage.TotalTokens != 14 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	messages := requestBody["messages"].([]any)
	replayed := messages[0].(map[string]any)
	calls := replayed["tool_calls"].([]any)
	tool := calls[0].(map[string]any)
	if id, _ := tool["id"].(string); len(id) != 9 || !isAllASCIIAlnum(id) {
		t.Fatalf("replayed tool call ID %q is not nine alphanumerics", id)
	}
	if tool["index"] != float64(0) || replayed["prefix"] != false {
		t.Fatalf("replayed assistant shape = %#v", replayed)
	}
	result := messages[1].(map[string]any)
	if result["role"] != "tool" || result["name"] != "read" || result["tool_call_id"] != tool["id"] {
		t.Fatalf("replayed tool result = %#v", result)
	}
	if wire := requestBody["prompt_cache_key"]; wire != "synthetic-session" {
		t.Fatalf("prompt cache key = %v", wire)
	}
	if _, has := requestBody["promptCacheKey"]; has {
		t.Fatal("camelCase key leaked onto the wire")
	}
}

func isAllASCIIAlnum(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return len(s) > 0
}

func TestMistralStreamRequiresFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewMistralClient(server.URL, "synthetic-key", 0, time.Second)
	if _, err := client.Send(context.Background(), Request{Model: mistralTestModel(), Thinking: "off"}, nil); err == nil || !strings.Contains(err.Error(), "without a finish reason") {
		t.Fatalf("unfinished stream accepted: %v", err)
	}
}

func TestQwenAndOpenRouterThinkingFormats(t *testing.T) {
	qwen := ModelMeta{ID: "qwen3.8-max", API: "openai-completions", Provider: "qwen-token-plan", Reasoning: true,
		Compat: modelmeta.Compat{"thinkingFormat": json.RawMessage(`"qwen"`), "supportsReasoningEffort": json.RawMessage(`true`),
			"supportsDeveloperRole": json.RawMessage(`false`), "supportsStore": json.RawMessage(`false`)},
		ThinkingLevelMap: map[string]*string{"high": strptr("high"), "max": strptr("max")}}
	body, err := BuildCompletionsRequest(Request{Model: qwen, Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if body["enable_thinking"] != true || body["reasoning_effort"] != "high" {
		t.Fatalf("qwen params = %v %v", body["enable_thinking"], body["reasoning_effort"])
	}
	body, err = BuildCompletionsRequest(Request{Model: qwen, Thinking: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if body["enable_thinking"] != false {
		t.Fatalf("qwen off = %v", body["enable_thinking"])
	}
	if _, has := body["reasoning_effort"]; has {
		t.Fatal("qwen off still sends reasoning_effort")
	}

	router := ModelMeta{ID: "moonshotai/kimi-k2.7-code", API: "openai-completions", Provider: "openrouter", Reasoning: true,
		Compat:           modelmeta.Compat{"thinkingFormat": json.RawMessage(`"openrouter"`), "sendSessionAffinityHeaders": json.RawMessage(`true`)},
		ThinkingLevelMap: map[string]*string{"off": strptr("none")}}
	body, err = BuildCompletionsRequest(Request{Model: router, Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "high" {
		t.Fatalf("openrouter reasoning = %#v", body["reasoning"])
	}
	body, err = BuildCompletionsRequest(Request{Model: router, Thinking: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "none" {
		t.Fatalf("openrouter off reasoning = %#v", body["reasoning"])
	}
}

func strptr(s string) *string { return &s }
