//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/session"
)

// TestActualAppModelSwitchAppliesToTheNextRequestInTurn covers Pi's model
// switch during a run: the request in flight keeps the model it was built
// with, and the next request of the same run uses the new model. Pi reads
// the selected model when it prepares each request, so a switch between two
// tool calls lands on the next assistant response.
func TestActualAppModelSwitchAppliesToTheNextRequestInTurn(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	release := make(chan struct{})
	firstReceived := make(chan struct{})
	var mu sync.Mutex
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		model, _ := body["model"].(string)
		mu.Lock()
		models = append(models, model)
		attempt := len(models)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			// Hold the first request open so the switch lands while it is
			// in flight. The response is a tool call, so the next request
			// belongs to the same run.
			select {
			case <-firstReceived:
			default:
				close(firstReceived)
			}
			<-release
			write := func(name string, data any) {
				b, _ := json.Marshal(data)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
			}
			write("message_start", map[string]any{"message": map[string]any{"id": "m1", "model": model, "usage": map[string]any{"input_tokens": 10}}})
			write("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": "tool-1", "name": "read", "input": map[string]any{}}})
			write("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"path":"fixture.txt"}`}})
			write("content_block_stop", map[string]any{"index": 0})
			write("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 5}})
			write("message_stop", map[string]any{})
			return
		}
		writeAnthropicTestResponse(w, "m2", "ANSWER_ON_THE_NEW_MODEL")
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m1","compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m1","maxTokens":1024,"contextWindow":100000},{"id":"m2","maxTokens":1024,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-switch-key"}}`)
	if err := os.WriteFile(filepath.Join(work, "fixture.txt"), []byte("SYNTHETIC_FIXTURE"), 0o600); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--tools", "read")
	q.prompt()
	q.write("START_SWITCH\r")
	// Wait for the request to reach the provider, so the switch lands while
	// it is in flight and not before it starts.
	select {
	case <-firstReceived:
	case <-time.After(10 * time.Second):
		t.Fatal("the first request never reached the provider")
	}
	q.write("/model anthropic/m2\r")
	q.wait("Model: m2")
	close(release)
	q.wait("ANSWER_ON_THE_NEW_MODEL")
	q.write("/quit\r")
	q.finish()
	mu.Lock()
	defer mu.Unlock()
	if len(models) != 2 || models[0] != "m1" || models[1] != "m2" {
		t.Fatalf("request models=%v; want the in-flight request on m1 and the next one on m2", models)
	}
}

// TestActualAppModelSwitchDuringCompaction covers the model switch while a
// compaction runs: the summary keeps the model the compaction started with,
// and the request after the checkpoint uses the new model. Pi captures the
// model when the compaction starts and reads the selection again for the
// next request.
func TestActualAppModelSwitchDuringCompaction(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	release := make(chan struct{})
	summaryReceived := make(chan struct{})
	var mu sync.Mutex
	var kinds, models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		encoded, _ := json.Marshal(body["system"])
		model, _ := body["model"].(string)
		kind := "answer"
		if strings.Contains(string(encoded), "context summarization assistant") {
			kind = "summary"
		}
		mu.Lock()
		kinds = append(kinds, kind)
		models = append(models, model)
		attempt := len(kinds)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if kind == "summary" {
			// Hold the summary open so the switch lands while it runs.
			select {
			case <-summaryReceived:
			default:
				close(summaryReceived)
			}
			<-release
			writeAnthropicTestResponse(w, "summary", "SUMMARY")
			return
		}
		writeAnthropicTestResponse(w, "answer", "ANSWER_AFTER_THE_SWITCH")
		_ = attempt
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m1","compaction":{"enabled":true,"reserveTokens":0,"keepRecentTokens":100},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m1","maxTokens":1024,"contextWindow":100000},{"id":"m2","maxTokens":1024,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-switch-compact-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("older request")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m1", StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("older context ", 3_500)}}}}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("/compact\r")
	// Wait for the summary request to reach the provider, so the switch
	// lands while the summary runs and not before it starts.
	select {
	case <-summaryReceived:
	case <-time.After(10 * time.Second):
		t.Fatal("the summary request never reached the provider")
	}
	q.write("/model anthropic/m2\r")
	q.wait("Model: m2")
	close(release)
	q.wait("Compacted from")
	// The next turn runs on the model that was selected after the
	// compaction started.
	q.write("AFTER_THE_SWITCH\r")
	q.wait("ANSWER_AFTER_THE_SWITCH")
	q.write("/quit\r")
	q.finish()
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) != 2 || kinds[0] != "summary" || kinds[1] != "answer" {
		t.Fatalf("requests=%v; want the summary and one answer", kinds)
	}
	if models[0] != "m1" {
		t.Fatalf("the summary used %q, want the model the compaction started with (m1)", models[0])
	}
	if models[1] != "m2" {
		t.Fatalf("the request after the checkpoint used %q, want m2", models[1])
	}
}
