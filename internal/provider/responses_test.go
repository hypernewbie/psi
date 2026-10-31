package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/modelmeta"
)

func responsesHaveStrictToolField(t *testing.T, body map[string]any) (map[string]any, bool) {
	t.Helper()
	input := body["input"].([]any)
	for _, raw := range input {
		item := responsesInputMap(t, raw)
		if item["type"] != "additional_tools" {
			continue
		}
		for _, rawTool := range item["tools"].([]any) {
			tool := responsesInputMap(t, rawTool)
			if _, ok := tool["strict"]; ok {
				return tool, true
			}
		}
	}
	return nil, false
}

func responsesTestModel(id string) ModelMeta {
	return ModelMeta{
		ID: id, API: "openai-responses", Provider: "opencode-go", BaseURL: "https://opencode.ai/inference/go/v1",
		MaxTokens: 8192, Reasoning: true, InputImage: true,
		ThinkingLevelMap: map[string]*string{"off": nil, "minimal": nil, "low": responsesString("low"), "medium": responsesString("medium"), "high": responsesString("high"), "xhigh": responsesString("xhigh"), "max": responsesString("max")},
		Compat: modelmeta.Compat{
			"sessionAffinityFormat":          json.RawMessage(`"openai-nosession"`),
			"supportsMidConvoSystemMessages": json.RawMessage(`true`),
			"supportsAdditionalTools":        json.RawMessage(`true`),
		},
	}
}

func responsesString(value string) *string { return &value }

func TestResponsesRequestMatchesOpenCodeCatalogCapabilities(t *testing.T) {
	model := responsesTestModel("synthetic-responses")
	tool := ToolDef{Name: "read", Description: "Read a local file", Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}
	writeTool := ToolDef{Name: "write", Description: "Write a local file", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}
	request := Request{Model: model, System: "effective system prompt", Thinking: "off", Tools: []ToolDef{writeTool}, Messages: []Message{
		{Role: "system", Content: []Block{{Type: "text", Text: "base instructions"}}, Sections: []SystemSection{{Name: "psi", Value: responsesString("workspace instructions")}}, ToolsAdded: []ToolDef{tool}},
		{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Provider: model.Provider, API: model.API, Model: model.ID, Content: []Block{{Type: "text", Text: "I will read the file", TextSignature: `{"v":1,"id":"msg_assistant","phase":"commentary"}`}}},
		{Role: "system", Sections: []SystemSection{{Name: "psi", Value: responsesString("updated workspace instructions")}}, ToolsAdded: []ToolDef{writeTool}, ToolsRemoved: []string{"read"}},
	}}
	body, err := BuildResponsesRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if body["stream"] != true || body["store"] != false || body["model"] != model.ID {
		t.Fatalf("request envelope = %#v", body)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("catalog additional_tools model also sent top-level tools: %#v", body)
	}
	if body["max_output_tokens"] != model.MaxTokens {
		t.Fatalf("default model output limit was lost: %#v", body)
	}
	if _, ok := responsesHaveStrictToolField(t, body); ok {
		t.Fatal("default Responses compatibility unexpectedly enabled strict tools")
	}
	input := body["input"].([]any)
	if len(input) != 7 {
		t.Fatalf("Responses input items = %#v", input)
	}
	firstTools := input[0].(map[string]any)
	if firstTools["type"] != "additional_tools" || firstTools["role"] != "developer" || firstTools["tools"].([]any)[0].(map[string]any)["name"] != "read" {
		t.Fatalf("initial tool declaration = %#v", firstTools)
	}
	firstSystem := input[1].(map[string]any)
	if firstSystem["role"] != "developer" || firstSystem["content"] != "base instructions\n\nworkspace instructions" {
		t.Fatalf("initial system message = %#v", firstSystem)
	}
	assistant := input[3].(map[string]any)
	if assistant["type"] != "message" || assistant["id"] != "msg_assistant" || assistant["phase"] != "commentary" {
		t.Fatalf("assistant item signature = %#v", assistant)
	}
	if !strings.Contains(input[4].(map[string]any)["content"].(string), "incremental tools update") {
		t.Fatalf("incremental tool notice = %#v", input[4])
	}
	if input[5].(map[string]any)["type"] != "additional_tools" || input[5].(map[string]any)["tools"].([]any)[0].(map[string]any)["name"] != "write" {
		t.Fatalf("subsequent tool declaration = %#v", input[5])
	}
	if input[6].(map[string]any)["content"] != "Updated system prompt section \"psi\":\n\nupdated workspace instructions\n\nThe following tools are no longer available. Do not call them:\n- read" {
		t.Fatalf("mid-conversation system/tool removal = %#v", input[6])
	}
}

func TestResponsesThinkingAndCompatibilityDefaults(t *testing.T) {
	model := responsesTestModel("gpt-5.6-luna")
	body, err := BuildResponsesRequest(Request{Model: model, Thinking: "minimal", MaxTokens: 7})
	if err != nil {
		t.Fatal(err)
	}
	if body["max_output_tokens"] != 16 {
		t.Fatalf("output token minimum = %#v", body["max_output_tokens"])
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" || reasoning["summary"] != "auto" || body["include"].([]string)[0] != "reasoning.encrypted_content" {
		t.Fatalf("clamped reasoning options = %#v", body)
	}
	offBody, err := BuildResponsesRequest(Request{Model: model, Thinking: "off"})
	if err != nil {
		t.Fatal(err)
	}
	offReasoning, ok := offBody["reasoning"].(map[string]any)
	if !ok || offReasoning["effort"] != "low" {
		t.Fatalf("Pi-compatible off clamping for null mapping = %#v", offBody)
	}
	model.ThinkingLevelMap["off"] = responsesString("none")
	body, err = BuildResponsesRequest(Request{Model: model, Thinking: "off"})
	if err != nil || body["reasoning"].(map[string]any)["effort"] != "none" {
		t.Fatalf("mapped off reasoning = %#v, %v", body, err)
	}
	model.Compat["supportsDeveloperRole"] = json.RawMessage(`null`)
	if _, err := BuildResponsesRequest(Request{Model: model, Thinking: "off"}); err != nil {
		t.Fatalf("null compatibility field should use Pi's default: %v", err)
	}
	model.Compat["inventedFeature"] = json.RawMessage(`true`)
	if _, err := BuildResponsesRequest(Request{Model: model, Thinking: "off"}); err == nil || !strings.Contains(err.Error(), "inventedFeature") {
		t.Fatalf("unknown compatibility field was guessed: %v", err)
	}
}

func TestResponsesRejectsToolDefinitionsWithoutSchemas(t *testing.T) {
	model := responsesTestModel("schema-required")
	if _, err := BuildResponsesRequest(Request{Model: model, Thinking: "off", Tools: []ToolDef{{Name: "read"}}}); err == nil {
		t.Fatal("missing tool schema was replaced with an invented schema")
	}
}

func TestResponsesReplayPreservesOnlySameModelSignaturesAndLinks(t *testing.T) {
	model := responsesTestModel("model-a")
	messages := []Message{
		{Role: "assistant", Provider: model.Provider, API: model.API, Model: model.ID, Content: []Block{
			{Type: "thinking", Thinking: "plan", ThinkingSignature: `{"type":"reasoning","id":"rs_1","summary":[]}`},
			{Type: "text", Text: "calling read", TextSignature: `{"v":1,"id":"msg_1","phase":"commentary"}`},
			{Type: "toolCall", ID: "call_1|fc_item_1", Name: "read", Arguments: map[string]any{"path": "file.txt"}},
		}},
		{Role: "toolResult", ToolCallID: "call_1|fc_item_1", ToolName: "read", Content: []Block{{Type: "text", Text: "file body"}}},
	}
	body, err := BuildResponsesRequest(Request{Model: model, Thinking: "off", Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	items := body["input"].([]any)
	var reasoning, assistant, call, result map[string]any
	for _, raw := range items {
		item := responsesInputMap(t, raw)
		switch item["type"] {
		case "reasoning":
			reasoning = item
		case "message":
			assistant = item
		case "function_call":
			call = item
		case "function_call_output":
			result = item
		}
	}
	if reasoning == nil || assistant["phase"] != "commentary" || call["call_id"] != "call_1" || call["id"] != "fc_item_1" || result["call_id"] != "call_1" {
		t.Fatalf("same-model Responses replay = %#v", items)
	}
	model.ID = "model-b"
	body, err = BuildResponsesRequest(Request{Model: model, Thinking: "off", Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	items = body["input"].([]any)
	reasoning, assistant, call, result = nil, nil, nil, nil
	for _, raw := range items {
		item := responsesInputMap(t, raw)
		switch item["type"] {
		case "reasoning":
			reasoning = item
		case "message":
			assistant = item
		case "function_call":
			call = item
		case "function_call_output":
			result = item
		}
	}
	if reasoning != nil || assistant["id"] != "msg_pi_0" || call["call_id"] != "call_1_fc_item_1" || call["id"] != nil || result["call_id"] != call["call_id"] {
		t.Fatalf("cross-model Responses replay leaked signatures or broke tool linkage: %#v", items)
	}
}

func TestResponsesLoopbackPathHeadersStreamAndUsage(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference/go/openai/v1/responses" || r.Method != http.MethodPost {
			t.Errorf("Responses request path/method = %s %s", r.URL.Path, r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-responses-key" || r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Responses request headers = %#v", r.Header)
		}
		if r.Header.Get("X-OpenCode-Session") != "synthetic-session" || r.Header.Get("X-Client-Request-Id") != "synthetic-session" || r.Header.Get("Session_Id") != "" {
			t.Errorf("OpenCode affinity headers = %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode Responses request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responsesEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1", "model": "served-model"}}))
		_, _ = io.WriteString(w, responsesEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "reasoning", "id": "rs_1"}}))
		_, _ = io.WriteString(w, responsesEvent("response.reasoning_summary_text.delta", map[string]any{"output_index": 0, "delta": "plan"}))
		reasoning := map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{map[string]any{"type": "summary_text", "text": "plan"}}, "encrypted_content": "opaque-reasoning"}
		_, _ = io.WriteString(w, responsesEvent("response.output_item.done", map[string]any{"output_index": 0, "item": reasoning}))
		_, _ = io.WriteString(w, responsesEvent("response.output_item.added", map[string]any{"output_index": 1, "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "phase": "final_answer"}}))
		_, _ = io.WriteString(w, responsesEvent("response.output_text.delta", map[string]any{"output_index": 1, "delta": "hello "}))
		_, _ = io.WriteString(w, responsesEvent("response.output_text.delta", map[string]any{"output_index": 1, "delta": "world"}))
		message := map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "hello world"}}}
		_, _ = io.WriteString(w, responsesEvent("response.output_item.done", map[string]any{"output_index": 1, "item": message}))
		_, _ = io.WriteString(w, responsesEvent("response.output_item.added", map[string]any{"output_index": 2, "item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read", "arguments": ""}}))
		_, _ = io.WriteString(w, responsesEvent("response.function_call_arguments.delta", map[string]any{"output_index": 2, "delta": `{"path":"file.txt"}`}))
		call := map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read", "arguments": `{"path":"file.txt"}`}
		_, _ = io.WriteString(w, responsesEvent("response.output_item.done", map[string]any{"output_index": 2, "item": call}))
		output := []any{reasoning, message, call}
		usage := map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120, "input_tokens_details": map[string]any{"cached_tokens": 10, "cache_write_tokens": 5}, "output_tokens_details": map[string]any{"reasoning_tokens": 5}}
		_, _ = io.WriteString(w, responsesEvent("response.completed", map[string]any{"response": map[string]any{"id": "resp_1", "model": "served-model", "status": "completed", "output": output, "usage": usage}}))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL+"/inference/go/openai/v1", "synthetic-responses-key", 0, time.Second)
	client.SessionID = "synthetic-session"
	model := responsesTestModel("synthetic-responses")
	model.BaseURL = client.BaseURL
	var streamed strings.Builder
	response, err := client.Send(context.Background(), Request{Model: model, System: "rules", Thinking: "off", Tools: []ToolDef{{Name: "read", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}}, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "read file"}}}}}, func(delta string) { streamed.WriteString(delta) })
	if err != nil {
		t.Fatal(err)
	}
	if response.ResponseID != "resp_1" || response.Model != "served-model" || response.StopReason != "toolUse" || response.Text != "hello world" || response.Thinking != "plan" || streamed.String() != "hello world" {
		t.Fatalf("Responses result identity/content = %+v; streamed %q", response, streamed.String())
	}
	if len(response.Content) != 3 || response.Content[0].Type != "thinking" || !strings.Contains(response.Content[0].ThinkingSignature, "opaque-reasoning") || response.Content[1].TextSignature != `{"id":"msg_1","phase":"final_answer","v":1}` ||
		len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != "call_1|fc_1" || response.ToolCalls[0].Arguments["path"] != "file.txt" {
		t.Fatalf("Responses content/signatures/tool call = %+v", response.Content)
	}
	if response.Usage.Input != 85 || response.Usage.Output != 20 || response.Usage.CacheRead != 10 || response.Usage.CacheWrite != 5 || response.Usage.TotalTokens != 120 || response.Usage.Reasoning == nil || *response.Usage.Reasoning != 5 {
		t.Fatalf("Responses provider usage = %+v", response.Usage)
	}
	if requestBody["model"] != model.ID || requestBody["store"] != false || requestBody["stream"] != true || requestBody["prompt_cache_key"] != "synthetic-session" {
		t.Fatalf("Responses request body = %#v", requestBody)
	}
	if _, ok := requestBody["tools"]; ok {
		t.Fatalf("top-level tools were sent for in-place tool changes: %#v", requestBody)
	}
	client.CloseIdle()
}

func TestResponsesProviderErrorsAndMalformedStreamsDoNotLeakSecrets(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"http error", "provider echoed synthetic-responses-secret", http.StatusForbidden},
		{"malformed event", "data: synthetic-responses-secret\n\n", http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := NewResponsesClient(server.URL, "synthetic-responses-secret", 0, time.Second)
			_, err := client.Send(context.Background(), Request{Model: responsesTestModel("model"), Thinking: "off"}, nil)
			client.CloseIdle()
			if err == nil || strings.Contains(err.Error(), "synthetic-responses-secret") || strings.Contains(err.Error(), "provider echoed") {
				t.Fatalf("Responses error leaked provider data: %v", err)
			}
		})
	}
	limitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Your input exceeds the context window of this model", http.StatusBadRequest)
	}))
	defer limitServer.Close()
	client := NewResponsesClient(limitServer.URL, "synthetic-responses-secret", 0, time.Second)
	_, err := client.Send(context.Background(), Request{Model: responsesTestModel("model"), Thinking: "off"}, nil)
	client.CloseIdle()
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrContextLimit {
		t.Fatalf("Responses context-limit status was not retained: %v", err)
	}
}

func TestResponsesExplicitOpenCodeHeaderWinsCaseInsensitively(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-OpenCode-Session") != "explicit-session" {
			t.Errorf("explicit OpenCode session header was replaced: %#v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responsesEvent("response.completed", map[string]any{"response": map[string]any{"id": "r", "status": "completed", "output": []any{}}}))
	}))
	defer server.Close()
	client := NewResponsesClient(server.URL, "key", 0, time.Second)
	client.SessionID = "session-default"
	client.Headers = map[string]string{"X-OPENCode-SESSION": "explicit-session"}
	model := responsesTestModel("model")
	model.Compat = modelmeta.Compat{"sessionAffinityFormat": json.RawMessage(`"openai-nosession"`)}
	if _, err := client.Send(context.Background(), Request{Model: model, Thinking: "off"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesMalformedInterruptedAndIncompleteStreamsNeverDispatchTools(t *testing.T) {
	model := responsesTestModel("synthetic-responses")
	cases := []struct {
		name string
		body string
		want string
	}{
		{"missing terminal", responsesEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "m"}}) + responsesEvent("response.output_text.delta", map[string]any{"output_index": 0, "delta": "partial"}), "before a terminal"},
		{"bad arguments", responsesEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_x", "call_id": "call_x", "name": "read", "arguments": ""}}) + responsesEvent("response.function_call_arguments.delta", map[string]any{"output_index": 0, "delta": "[]"}) + responsesEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_x", "call_id": "call_x", "name": "read", "arguments": "[]"}}), "must be an object"},
		{"failed response", responsesEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_x", "call_id": "call_x", "name": "read"}}) + responsesEvent("response.failed", map[string]any{"response": map[string]any{"id": "r", "status": "failed", "usage": map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6}}}), "reported a failed"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			partial, err := parseResponsesSSE(strings.NewReader(test.body), time.Second, context.Background(), nil, Request{Model: model, Thinking: "off"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
			if partial != nil && len(partial.ToolCalls) != 0 {
				t.Fatalf("unsafe partial tool calls = %+v", partial.ToolCalls)
			}
			if test.name == "failed response" && (partial == nil || partial.Usage.TotalTokens != 6) {
				t.Fatalf("failed response did not retain reported usage: %+v", partial)
			}
		})
	}

	unfinished := responsesEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_x", "call_id": "call_x", "name": "read", "arguments": ""}}) +
		responsesEvent("response.incomplete", map[string]any{"response": map[string]any{"id": "r", "status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"}}})
	response, err := parseResponsesSSE(strings.NewReader(unfinished), time.Second, context.Background(), nil, Request{Model: model, Thinking: "off"})
	if err != nil || response.StopReason != "length" || len(response.ToolCalls) != 0 || len(response.Content) != 0 {
		t.Fatalf("incomplete function call became dispatchable: %+v, %v", response, err)
	}
}

func TestResponsesInterruptedStreamIsNeverRetried(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responsesEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "m"}}))
		_, _ = io.WriteString(w, responsesEvent("response.output_text.delta", map[string]any{"output_index": 0, "delta": "partial"}))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	client := NewResponsesClient(server.URL, "key", 4, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	deltas := make(chan string, 1)
	go func() {
		_, err := client.Send(ctx, Request{Model: responsesTestModel("model"), Thinking: "off"}, func(delta string) {
			select {
			case deltas <- delta:
			default:
			}
		})
		done <- err
	}()
	<-started
	select {
	case delta := <-deltas:
		if delta != "partial" {
			t.Fatalf("streamed partial delta = %q", delta)
		}
	case <-time.After(3 * time.Second):
		cancel()
		server.Close()
		t.Fatal("Responses stream did not deliver a text delta")
	}
	cancel()
	select {
	case err := <-done:
		var interrupted *StreamInterrupt
		if !errors.As(err, &interrupted) || interrupted.Partial.Text != "partial" || !strings.Contains(err.Error(), "not retried") || requests.Load() != 1 {
			t.Fatalf("interrupted Responses result = %T %v, requests %d", err, err, requests.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled Responses stream did not return")
	}
	server.Close()
}

func responsesInputMap(t *testing.T, raw any) map[string]any {
	t.Helper()
	if item, ok := raw.(map[string]any); ok {
		return item
	}
	encoded, ok := raw.(json.RawMessage)
	if !ok {
		t.Fatalf("unexpected Responses input item type %T", raw)
	}
	var item map[string]any
	if err := json.Unmarshal(encoded, &item); err != nil {
		t.Fatalf("decode Responses input item: %v", err)
	}
	return item
}

func responsesEvent(event string, payload any) string {
	object, ok := payload.(map[string]any)
	if !ok {
		panic("Responses test event payload must be an object")
	}
	copy := make(map[string]any, len(object)+1)
	for key, value := range object {
		copy[key] = value
	}
	copy["type"] = event
	encoded, err := json.Marshal(copy)
	if err != nil {
		panic(fmt.Sprintf("marshal test event: %v", err))
	}
	return "event: " + event + "\ndata: " + string(encoded) + "\n\n"
}
