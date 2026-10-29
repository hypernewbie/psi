package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMiniMaxAndOpenCodeAnthropicEndpointKeyAndSessionHeaders(t *testing.T) {
	var mu sync.Mutex
	var paths, keys, sessions, auths, versions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		keys = append(keys, r.Header.Get("x-api-key"))
		sessions = append(sessions, r.Header.Get("x-opencode-session"))
		auths = append(auths, r.Header.Get("Authorization"))
		versions = append(versions, r.Header.Get("anthropic-version"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"synthetic reasoning\"}}\n\n"+
			"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"synthetic-signature\"}}\n\n"+
			"event: content_block_stop\ndata: {\"index\":0}\n\n"+
			"event: content_block_start\ndata: {\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"synthetic answer\"}}\n\n"+
			"event: content_block_stop\ndata: {\"index\":1}\n\n"+
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n"+
			"event: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()

	client := NewClient(server.URL+"/anthropic", "synthetic-api-key", 0, time.Second)
	client.SessionID = "synthetic-session-id"
	for _, model := range []ModelMeta{
		{ID: "MiniMax-M3.1-Flash", API: "anthropic-messages", Provider: "minimax", MaxTokens: 1024},
		{ID: "qwen3.8-flash", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 1024},
	} {
		response, err := client.Send(context.Background(), Request{Model: model, Thinking: "off"}, nil)
		if err != nil {
			t.Fatalf("%s request failed: %v", model.Provider, err)
		}
		if response.Text != "synthetic answer" || response.Thinking != "synthetic reasoning" || response.Content[0].ThinkingSignature != "synthetic-signature" {
			t.Fatalf("%s text/thinking stream changed: %+v", model.Provider, response)
		}
	}
	client.Headers = map[string]string{"X-OpenCode-Session": "explicit-session-id"}
	if _, err := client.Send(context.Background(), Request{Model: ModelMeta{ID: "qwen3.8-flash", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 1024}, Thinking: "off"}, nil); err != nil {
		t.Fatalf("explicit-header request failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 3 {
		t.Fatalf("request count = %d", len(paths))
	}
	for i, path := range paths {
		if path != "/anthropic/v1/messages" || keys[i] != "synthetic-api-key" || auths[i] != "" || versions[i] != "2023-06-01" {
			t.Errorf("request %d path/auth = %q %q %q", i, path, keys[i], auths[i])
		}
	}
	if sessions[0] != "" || sessions[1] != "synthetic-session-id" || sessions[2] != "explicit-session-id" {
		t.Fatalf("OpenCode session headers = %q", sessions)
	}
}

func TestAnthropicAdaptiveThinkingUsesModelEffortMap(t *testing.T) {
	mappedEffort := "medium"
	model := ModelMeta{ID: "MiniMax-M3.1-Flash", API: "anthropic-messages", Provider: "minimax", MaxTokens: 4096, Reasoning: true, AdaptiveThinking: true, ThinkingLevelMap: map[string]*string{"high": &mappedEffort, "off": nil}}
	body, err := BuildAnthropicRequest(Request{Model: model, Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	thinking := body["thinking"].(map[string]any)
	output := body["output_config"].(map[string]any)
	if thinking["type"] != "adaptive" || output["effort"] != "medium" {
		t.Fatalf("adaptive metadata mapping lost: thinking=%+v output=%+v", thinking, output)
	}
	off, err := BuildAnthropicRequest(Request{Model: model, Thinking: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := off["thinking"]; present {
		t.Fatalf("off:null was not preserved as the model's default thinking mode: %+v", off["thinking"])
	}
}

func TestMiniMaxAndOpenCodeAnthropicShareBoundedToolAndImageTranslation(t *testing.T) {
	for _, providerID := range []string{"minimax", "opencode-go"} {
		model := ModelMeta{ID: "synthetic-model", API: "anthropic-messages", Provider: providerID, MaxTokens: 16384, InputImage: true, Reasoning: true, AdaptiveThinking: providerID == "minimax", AllowEmptySignature: providerID == "opencode-go"}
		thinkingBlock := Block{Type: "thinking", Thinking: "synthetic reasoning"}
		if providerID == "minimax" {
			thinkingBlock.ThinkingSignature = "synthetic-signature"
		}
		body, err := BuildAnthropicRequest(Request{
			Model: model, Thinking: "high", System: "synthetic instructions",
			Tools: []ToolDef{{Name: "read", Description: "read a file", Schema: map[string]any{"type": "object"}}, {Name: "bash", Description: "run a command", Schema: map[string]any{"type": "object"}}},
			Messages: []Message{
				{Role: "user", Content: []Block{{Type: "text", Text: "inspect this"}, {Type: "image", MimeType: "image/png", Data: "c3ludGhldGlj"}}},
				{Role: "assistant", Provider: providerID, API: "anthropic-messages", Model: model.ID, Content: []Block{thinkingBlock, {Type: "toolCall", ID: "call_a", Name: "read", Arguments: map[string]any{"path": "a"}}, {Type: "toolCall", ID: "call_b", Name: "bash", Arguments: map[string]any{"command": "true"}}}},
				{Role: "toolResult", ToolCallID: "call_a", ToolName: "read", Content: []Block{{Type: "text", Text: "synthetic file"}}},
				{Role: "toolResult", ToolCallID: "call_b", ToolName: "bash", IsError: true, Content: []Block{{Type: "text", Text: "synthetic failure"}}},
			},
		})
		if err != nil {
			t.Fatalf("%s translation: %v", providerID, err)
		}
		messages := body["messages"].([]map[string]any)
		if len(messages) != 3 || body["model"] != model.ID || len(body["tools"].([]map[string]any)) != 2 {
			t.Fatalf("%s request lost model, tools, or grouped tool results: %+v", providerID, body)
		}
		userContent := messages[0]["content"].([]any)
		if len(userContent) != 2 || userContent[1].(map[string]any)["type"] != "image" {
			t.Fatalf("%s request lost image input: %+v", providerID, userContent)
		}
		assistantContent := messages[1]["content"].([]any)
		if assistantContent[0].(map[string]any)["type"] != "thinking" || assistantContent[1].(map[string]any)["id"] != "call_a" || assistantContent[2].(map[string]any)["id"] != "call_b" {
			t.Fatalf("%s request lost reasoning or tool IDs: %+v", providerID, assistantContent)
		}
		resultContent := messages[2]["content"].([]any)
		if len(resultContent) != 2 || resultContent[0].(map[string]any)["tool_use_id"] != "call_a" || resultContent[1].(map[string]any)["tool_use_id"] != "call_b" {
			t.Fatalf("%s request lost tool-result links: %+v", providerID, resultContent)
		}
	}
}

func TestMiniMaxAndOpenCodeAnthropicPartialToolStreamsNeverRetryOrDispatch(t *testing.T) {
	for _, providerID := range []string{"minimax", "opencode-go"} {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"synthetic-model\"}}\n\n"+
				"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_partial\",\"name\":\"read\"}}\n\n"+
				"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\"}}\n\n")
		}))
		client := NewClient(server.URL+"/anthropic", "synthetic-key", 3, time.Second)
		_, err := client.Send(context.Background(), Request{Model: ModelMeta{ID: "synthetic-model", API: "anthropic-messages", Provider: providerID, MaxTokens: 1024}}, nil)
		server.Close()
		var interrupted *StreamInterrupt
		if !errors.As(err, &interrupted) || attempts.Load() != 1 {
			t.Fatalf("%s partial stream error/retries = %v/%d", providerID, err, attempts.Load())
		}
		if interrupted.Partial != nil && len(interrupted.Partial.ToolCalls) != 0 {
			t.Fatalf("%s partial tool call escaped settlement: %+v", providerID, interrupted.Partial.ToolCalls)
		}
	}
}

func TestAnthropicAllowEmptySignatureCompatMatchesPi(t *testing.T) {
	build := func(allow bool, thinking string) map[string]any {
		t.Helper()
		body, err := BuildAnthropicRequest(Request{
			Model:    ModelMeta{ID: "qwen3.8-flash", API: "anthropic-messages", Provider: "opencode-go", AllowEmptySignature: allow},
			Messages: []Message{{Role: "assistant", Provider: "opencode-go", API: "anthropic-messages", Model: "qwen3.8-flash", Content: []Block{{Type: "thinking", Thinking: thinking}}}},
			Thinking: "off",
		})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	allowed := build(true, "unsigned historical reasoning")
	allowedMessages := allowed["messages"].([]map[string]any)
	if len(allowedMessages) != 1 {
		t.Fatalf("allowed message count = %d", len(allowedMessages))
	}
	allowedBlock := allowedMessages[0]["content"].([]any)[0].(map[string]any)
	if allowedBlock["type"] != "thinking" || allowedBlock["signature"] != "" || allowedBlock["thinking"] != "unsigned historical reasoning" {
		t.Fatalf("allowEmptySignature did not preserve an empty signature: %+v", allowedBlock)
	}
	blocked := build(false, "unsigned historical reasoning")
	blockedBlock := blocked["messages"].([]map[string]any)[0]["content"].([]any)[0].(map[string]any)
	if blockedBlock["type"] != "text" || blockedBlock["text"] != "unsigned historical reasoning" {
		t.Fatalf("unmarked model received an unsigned thinking block: %+v", blockedBlock)
	}
	empty := build(true, "")
	if got := empty["messages"].([]map[string]any); len(got) != 0 {
		t.Fatalf("empty unsigned thinking block was not dropped: %+v", got)
	}
}
