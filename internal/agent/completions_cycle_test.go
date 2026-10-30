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

func TestDeepSeekCompletionsToolCycleUsesCompleteCallsAndReplaysReasoning(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "fixture.txt"), []byte("synthetic file body"), 0600); err != nil {
		t.Fatal(err)
	}
	var turns atomic.Int32
	secondRequest := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-deepseek-key" {
			t.Errorf("Completions request path/auth = %s %#v", r.URL.Path, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		turn := turns.Add(1)
		if turn == 2 {
			secondRequest <- body
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			_, _ = io.WriteString(w,
				"data: {\"id\":\"completion-1\",\"model\":\"deepseek-served\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"planning\"},\"finish_reason\":null}]}\n\n"+
					"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-read\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"fixture.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\n"+
					"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w,
			"data: {\"id\":\"completion-2\",\"model\":\"deepseek-served\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"read completed\"},\"finish_reason\":null}]}\n\n"+
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n")
	}))
	defer server.Close()

	store, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	client := provider.NewCompletionsClient(server.URL, "synthetic-deepseek-key", 0, time.Second)
	model := provider.ModelMeta{ID: "synthetic-deepseek", API: "openai-completions", Provider: "deepseek", BaseURL: server.URL, MaxTokens: 4096, Reasoning: true,
		Compat: modelmeta.Compat{
			"maxTokensField": json.RawMessage(`"max_tokens"`),
			"thinkingFormat": json.RawMessage(`"deepseek"`),
			"requiresReasoningContentOnAssistantMessages": json.RawMessage(`true`),
		}}
	loop := New(store, tools.NewRegistry(cwd), client, nil)
	cfg := Config{Model: model, Thinking: "high", Tools: toolDefs(), System: "synthetic system"}
	if err := loop.Submit(context.Background(), "read fixture.txt", cfg); err != nil {
		t.Fatal(err)
	}
	if turns.Load() != 2 {
		t.Fatalf("provider turn count = %d", turns.Load())
	}
	select {
	case body := <-secondRequest:
		messages := body["messages"].([]any)
		var assistant, result map[string]any
		for _, raw := range messages {
			message := raw.(map[string]any)
			if message["role"] == "assistant" && message["tool_calls"] != nil {
				assistant = message
			}
			if message["role"] == "tool" {
				result = message
			}
		}
		if assistant == nil || assistant["reasoning_content"] != "planning" || result == nil || result["tool_call_id"] != "call-read" || !strings.Contains(result["content"].(string), "synthetic file body") {
			t.Fatalf("tool round trip/request replay = %#v", messages)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second tool-cycle request was not observed")
	}
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var persistedThinking bool
	for _, message := range proj.Flat {
		if message.Role == "assistant" && message.Assistant != nil && message.Assistant.Provider == "deepseek" {
			for _, block := range message.Assistant.Content {
				if block.Type == "thinking" && block.Thinking != nil && block.Thinking.ThinkingSignature != nil && *block.Thinking.ThinkingSignature == "reasoning_content" {
					persistedThinking = true
				}
			}
		}
	}
	if !persistedThinking {
		t.Fatal("DeepSeek reasoning field signature was not persisted in the session")
	}
	client.CloseIdle()
}
