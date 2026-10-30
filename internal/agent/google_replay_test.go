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

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

func TestGoogleToolRoundTripAndThoughtSignatureReplayAfterRestart(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "fixture.txt"), []byte("synthetic Google tool result"), 0600); err != nil {
		t.Fatal(err)
	}
	var turns atomic.Int32
	secondRequest := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-3-flash-preview:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" ||
			r.Header.Get("x-goog-api-key") != "synthetic-google-key" {
			t.Errorf("Google request path/auth = %s?%s %#v", r.URL.Path, r.URL.RawQuery, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Google request: %v", err)
			return
		}
		turn := turns.Add(1)
		if turn == 2 {
			secondRequest <- body
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var chunk any
		if turn == 1 {
			chunk = map[string]any{"responseId": "google-response-1", "candidates": []any{map[string]any{
				"content": map[string]any{"parts": []any{map[string]any{
					"functionCall":     map[string]any{"id": "call|read", "name": "read", "args": map[string]any{"path": "fixture.txt"}},
					"thoughtSignature": "AAAAAAAAAAAAAAAAAAAAAA==",
				}}}, "finishReason": "STOP",
			}}}
		} else {
			chunk = map[string]any{"responseId": "google-response-2", "candidates": []any{map[string]any{
				"content":      map[string]any{"parts": []any{map[string]any{"text": "Google resumed and completed the task"}}},
				"finishReason": "STOP",
			}}}
		}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			t.Errorf("encode synthetic Google response: %v", err)
			return
		}
		_, _ = io.WriteString(w, "data: "+string(encoded)+"\n\n")
	}))
	defer server.Close()

	store, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	client := provider.NewGoogleClient(server.URL+"/v1beta", "synthetic-google-key", 0, time.Second)
	model := provider.ModelMeta{ID: "gemini-3-flash-preview", API: "google-generative-ai", Provider: "google", BaseURL: server.URL + "/v1beta",
		MaxTokens: 8192, Reasoning: true, InputImage: true, ThinkingLevelMap: map[string]*string{"off": nil, "minimal": googleReplayString("minimal"), "low": googleReplayString("low")}}
	cfg := Config{Model: model, Thinking: "off", Tools: toolDefs(), System: "synthetic Google system"}
	loop := New(store, tools.NewRegistry(cwd), client, nil)
	if err := loop.Submit(context.Background(), "read fixture.txt", cfg); err != nil {
		t.Fatalf("Google tool cycle did not settle at the run boundary: %v", err)
	}
	if turns.Load() != 2 {
		t.Fatalf("first Google turn count = %d", turns.Load())
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
		t.Fatalf("continued Google turn count = %d", turns.Load())
	}
	select {
	case body := <-secondRequest:
		contents := body["contents"].([]any)
		var modelTurn, functionResponse map[string]any
		for _, raw := range contents {
			content := raw.(map[string]any)
			if content["role"] == "model" {
				for _, partRaw := range content["parts"].([]any) {
					part := partRaw.(map[string]any)
					if part["functionCall"] != nil {
						modelTurn = content
						if part["thoughtSignature"] != "AAAAAAAAAAAAAAAAAAAAAA==" {
							t.Fatalf("Google tool-call signature was not replayed: %#v", part)
						}
						if part["functionCall"].(map[string]any)["id"] != "call_read" {
							t.Fatalf("Google tool call ID normalization = %#v", part)
						}
					}
				}
			}
			if content["role"] == "user" {
				for _, partRaw := range content["parts"].([]any) {
					if part, ok := partRaw.(map[string]any); ok && part["functionResponse"] != nil {
						functionResponse = part["functionResponse"].(map[string]any)
					}
				}
			}
		}
		if modelTurn == nil || functionResponse == nil || functionResponse["id"] != "call_read" ||
			!strings.Contains(functionResponse["response"].(map[string]any)["output"].(string), "synthetic Google tool result") {
			t.Fatalf("Google replayed function call/result linkage incorrectly: %#v", contents)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restarted Google request was not observed")
	}
	projection, err := reopened.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var storedGoogleResponse bool
	for _, message := range projection.Flat {
		if message.Assistant != nil && message.Assistant.Provider == "google" && message.Assistant.ResponseID != nil &&
			*message.Assistant.ResponseID == "google-response-2" && message.Assistant.RawStopReason != nil && *message.Assistant.RawStopReason == "STOP" {
			storedGoogleResponse = true
		}
	}
	if !storedGoogleResponse {
		t.Fatal("Google response ID and raw finish reason were not persisted")
	}
	client.CloseIdle()
}

func googleReplayString(value string) *string { return &value }
