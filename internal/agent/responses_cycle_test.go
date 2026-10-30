package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/modelmeta"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

func TestOpenCodeResponsesToolCycleReplaysAfterSessionRestart(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "fixture.txt"), []byte("synthetic Responses tool result"), 0600); err != nil {
		t.Fatal(err)
	}
	var turns atomic.Int32
	secondRequest := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference/go/openai/v1/responses" || r.Header.Get("Authorization") != "Bearer synthetic-opencode-key" ||
			r.Header.Get("X-OpenCode-Session") == "" || r.Header.Get("X-Client-Request-Id") != r.Header.Get("X-OpenCode-Session") || r.Header.Get("Session_Id") != "" {
			t.Errorf("Responses path/auth/affinity = %s %#v", r.URL.Path, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Responses request: %v", err)
			return
		}
		turn := turns.Add(1)
		if turn == 2 {
			secondRequest <- body
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			_, _ = io.WriteString(w,
				responsesCycleEvent("response.created", map[string]any{"response": map[string]any{"id": "responses-1", "model": "served-model"}})+
					responsesCycleEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "reasoning", "id": "rs_1"}})+
					responsesCycleEvent("response.reasoning_summary_text.delta", map[string]any{"output_index": 0, "delta": "plan"})+
					responsesCycleEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{map[string]any{"type": "summary_text", "text": "plan"}}, "encrypted_content": "opaque-responses-signature"}})+
					responsesCycleEvent("response.output_item.added", map[string]any{"output_index": 1, "item": map[string]any{"type": "function_call", "id": "fc_read_1", "call_id": "call_read_1", "name": "read", "arguments": ""}})+
					responsesCycleEvent("response.function_call_arguments.delta", map[string]any{"output_index": 1, "delta": `{"path":"fixture.txt"}`})+
					responsesCycleEvent("response.output_item.done", map[string]any{"output_index": 1, "item": map[string]any{"type": "function_call", "id": "fc_read_1", "call_id": "call_read_1", "name": "read", "arguments": `{"path":"fixture.txt"}`}})+
					responsesCycleEvent("response.completed", map[string]any{"response": map[string]any{"id": "responses-1", "model": "served-model", "status": "completed", "output": []any{
						map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{map[string]any{"type": "summary_text", "text": "plan"}}, "encrypted_content": "opaque-responses-signature"},
						map[string]any{"type": "function_call", "id": "fc_read_1", "call_id": "call_read_1", "name": "read", "arguments": `{"path":"fixture.txt"}`},
					}, "usage": map[string]any{"input_tokens": 12, "output_tokens": 5, "total_tokens": 17}}}),
			)
			return
		}
		_, _ = io.WriteString(w,
			responsesCycleEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_final", "role": "assistant", "phase": "final_answer"}})+
				responsesCycleEvent("response.output_text.delta", map[string]any{"output_index": 0, "delta": "Responses resumed and completed"})+
				responsesCycleEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_final", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Responses resumed and completed"}}}})+
				responsesCycleEvent("response.completed", map[string]any{"response": map[string]any{"id": "responses-2", "model": "served-model", "status": "completed", "output": []any{}, "usage": map[string]any{"input_tokens": 25, "output_tokens": 4, "total_tokens": 29}}}),
		)
	}))
	defer server.Close()

	store, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	client := provider.NewResponsesClient(server.URL+"/inference/go/openai/v1", "synthetic-opencode-key", 0, time.Second)
	client.SessionID = store.Header().ID
	model := provider.ModelMeta{ID: "synthetic-opencode-responses", API: "openai-responses", Provider: "opencode-go", BaseURL: client.BaseURL,
		MaxTokens: 4096, Reasoning: true, InputImage: true, ThinkingLevelMap: map[string]*string{"off": nil, "low": responsesCycleString("low")},
		Compat: modelmeta.Compat{"sessionAffinityFormat": json.RawMessage(`"openai-nosession"`), "supportsMidConvoSystemMessages": json.RawMessage(`true`), "supportsAdditionalTools": json.RawMessage(`true`)}}
	cfg := Config{Model: model, Thinking: "off", Tools: toolDefs(), System: "synthetic Responses system"}
	loop := New(store, tools.NewRegistry(cwd), client, nil)
	if err := loop.Submit(context.Background(), "read fixture.txt", cfg); err != nil {
		t.Fatalf("Responses tool round did not settle at the test boundary: %v", err)
	}
	if turns.Load() != 2 {
		t.Fatalf("first Responses turn count = %d", turns.Load())
	}

	reopened, err := session.OpenFile(store.File(), cwd, false)
	if err != nil {
		t.Fatal(err)
	}
	continued := New(reopened, tools.NewRegistry(cwd), client, nil)
	if err := continued.Submit(context.Background(), "continue after restart", cfg); err != nil {
		t.Fatal(err)
	}
	if turns.Load() != 3 {
		t.Fatalf("Responses restart turn count = %d", turns.Load())
	}
	select {
	case body := <-secondRequest:
		input := body["input"].([]any)
		var reasoning, assistantCall, result map[string]any
		for _, raw := range input {
			item := responsesCycleInput(t, raw)
			switch item["type"] {
			case "reasoning":
				reasoning = item
			case "function_call":
				assistantCall = item
			case "function_call_output":
				result = item
			}
		}
		if reasoning == nil || reasoning["encrypted_content"] != "opaque-responses-signature" || assistantCall == nil || assistantCall["call_id"] != "call_read_1" ||
			assistantCall["id"] != "fc_read_1" || result == nil || result["call_id"] != "call_read_1" || !strings.Contains(result["output"].(string), "synthetic Responses tool result") {
			t.Fatalf("Responses tool/reasoning history was not replayed after restart: %#v", input)
		}
		if _, ok := body["tools"]; ok {
			t.Fatalf("Responses additional tools leaked to top-level tools: %#v", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restarted Responses request was not observed")
	}
	projection, err := reopened.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var storedResponse, storedSignature bool
	for _, message := range projection.Flat {
		if message.Assistant == nil || message.Assistant.API != "openai-responses" || message.Assistant.Provider != "opencode-go" {
			continue
		}
		if message.Assistant.ResponseID != nil && *message.Assistant.ResponseID == "responses-1" {
			storedResponse = message.Assistant.ResponseModel != nil && *message.Assistant.ResponseModel == "served-model" &&
				message.Assistant.RawStopReason != nil && *message.Assistant.RawStopReason == "completed" &&
				message.Assistant.Usage.Input == 12 && message.Assistant.Usage.Output == 5 && message.Assistant.Usage.TotalTokens == 17
		}
		for _, block := range message.Assistant.Content {
			if block.Type == "thinking" && block.Thinking != nil && block.Thinking.ThinkingSignature != nil && strings.Contains(*block.Thinking.ThinkingSignature, "opaque-responses-signature") {
				storedSignature = true
			}
		}
	}
	if !storedResponse || !storedSignature {
		t.Fatal("Responses provider/model/response identity or reasoning signature did not persist in Pi v3 session")
	}
	client.CloseIdle()
}

func responsesCycleEvent(event string, payload map[string]any) string {
	payload["type"] = event
	encoded, _ := json.Marshal(payload)
	return "event: " + event + "\ndata: " + string(encoded) + "\n\n"
}

func responsesCycleInput(t *testing.T, raw any) map[string]any {
	t.Helper()
	if item, ok := raw.(map[string]any); ok {
		return item
	}
	var item map[string]any
	if err := json.Unmarshal(raw.(json.RawMessage), &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func responsesCycleString(value string) *string { return &value }
