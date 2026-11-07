//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/session"
)

func TestActualAppACCommandsAndAlias(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":1000,"keepRecentTokens":100}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"m","api":"anthropic-messages","maxTokens":1024,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-ac-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("/ac status\r")
	q.wait("pi-ac: automatic compaction enabled")
	q.wait("end-of-turn delay: 10 minutes")
	q.write("/ac threshold 80\r")
	q.wait("Adaptive compaction target set to 80%")
	q.write("/autocompact threshold off\r")
	q.wait("Adaptive percentage target disabled")
	q.write("/ac end-turn 75%\r")
	q.wait("End-of-turn compaction threshold set to 75%")
	q.write("/ac end-turn-delay 0\r")
	q.wait("delay set to 0 minutes")
	q.write("/ac compactor current\r")
	q.wait("Compactor set to anthropic/m")
	q.write("/ac compactor clear\r")
	q.wait("Compactor cleared")
	q.write("/ac disable\r")
	q.wait("Automatic compaction disabled")
	q.write("/ac enable\r")
	q.wait("Automatic compaction enabled.")
	q.write("/quit\r")
	q.finish()
}

func TestActualAppACEndOfTurnCompaction(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		requestNumber := len(requestBodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		text := "EOT_ANSWER"
		if requestNumber > 1 {
			text = "EOT_SUMMARY"
		}
		writeAnthropicTestResponse(w, fmt.Sprintf("eot-%d", requestNumber), text)
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":0,"keepRecentTokens":100},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":40000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-ac-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("older request")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("older context ", 3_500)}}}}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("/ac end-turn 10\r")
	q.wait("End-of-turn compaction threshold set to 10 tokens")
	q.write("/ac end-turn-delay 0\r")
	q.wait("delay set to 0 minutes")
	q.write("ASK_EOT_COMPACT\r")
	q.wait("EOT_ANSWER")
	q.wait("Compacting context...")
	q.wait("Compacted from")
	mu.Lock()
	if len(requestBodies) != 2 {
		t.Fatalf("provider requests = %d, want user turn and end-of-turn summary", len(requestBodies))
	}
	if !strings.Contains(requestBodies[0], "ASK_EOT_COMPACT") || !strings.Contains(requestBodies[1], "older request") || !strings.Contains(requestBodies[1], "context summarization assistant") {
		t.Fatalf("wrong provider request sequence: %q", requestBodies)
	}
	mu.Unlock()
	q.write("/quit\r")
	q.finish()
}

func TestActualAppQueuedACCompactResumesWithSteering(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		requestNumber := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		text := "FIRST_ACTIVE_PART"
		if requestNumber == 2 {
			text = "AC_COMPACTION_SUMMARY"
		} else if requestNumber >= 3 {
			text = "AFTER_AC_COMPACT"
		}
		write := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		write("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("ac-active-%d", requestNumber), "model": "m", "usage": map[string]any{"input_tokens": 2}}})
		write("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		write("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if requestNumber == 1 || requestNumber == 2 {
			time.Sleep(500 * time.Millisecond)
		}
		write("content_block_stop", map[string]any{"index": 0})
		write("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}})
		write("message_stop", map[string]any{})
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false,"keepRecentTokens":100},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":2048,"contextWindow":40000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-ac-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("older request")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("older context ", 1_000)}}}}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("ACTIVE_TASK_ASK\r")
	q.wait("FIRST_ACTIVE_PART")
	q.write("/ac compact keep the current task\r")
	q.wait("Compaction queued until the active run settles")
	q.wait("Compacting context")
	q.write("STEER_AC_COMPACT\r")
	q.wait("Steering: STEER_AC_COMPACT")
	q.wait("AFTER_AC_COMPACT")
	mu.Lock()
	if len(bodies) != 3 {
		t.Fatalf("provider requests = %d, want active answer, summary, and resume", len(bodies))
	}
	last := bodies[2]
	mu.Unlock()
	for _, expected := range []string{"STEER_AC_COMPACT", "Continue the current task from the compacted context"} {
		if !strings.Contains(last, expected) {
			t.Fatalf("resume request missing %q: %s", expected, last)
		}
	}
	q.prompt()
	q.write("/quit\r")
	q.finish()
}

func writeAnthropicTestResponse(w http.ResponseWriter, id, text string) {
	write := func(name string, data any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
	}
	write("message_start", map[string]any{"message": map[string]any{"id": id, "model": "m", "usage": map[string]any{"input_tokens": 2}}})
	write("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	write("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
	write("content_block_stop", map[string]any{"index": 0})
	write("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 3}})
	write("message_stop", map[string]any{})
}
