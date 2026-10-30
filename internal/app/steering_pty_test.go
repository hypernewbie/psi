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
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/session"
)

// Pi's editor stays active while the agent runs. Enter queues a steering
// message that is delivered at the next turn boundary and rendered as a normal
// user message; Escape cancels and restores undelivered steering to the editor.
func TestActualAppSteeringWhileRunning(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		text := "FIRST_REPLY"
		index := int(requests.Add(1))
		if index > 1 {
			text = "AFTER_STEER"
		}
		emit("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("steer-%d", index), "model": "m", "usage": map[string]any{"input_tokens": 1}}})
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if index == 1 {
			// Hold the stream open so the steering message is queued while the
			// first response is still in flight.
			time.Sleep(600 * time.Millisecond)
		}
		emit("content_block_stop", map[string]any{"index": 0})
		emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}})
		emit("message_stop", map[string]any{})
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("ASK_UX\r")
	// While the first response streams, the editor must accept typing and Enter
	// must queue a steering message instead of being ignored.
	q.wait("FIRST_REPLY")
	q.write("STEER_ME\r")
	q.wait("Steering: STEER_ME")
	q.wait("AFTER_STEER")
	q.prompt()
	rows := append(append([]string{}, q.screen.Scrollback...), strings.Split(q.screen.Text(), "\n")...)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "› STEER_ME") {
		t.Fatalf("delivered steering was not rendered as a user message: %q", rows)
	}
	if strings.Contains(joined, "Steering: STEER_ME") {
		t.Fatalf("delivered steering kept its queued row: %q", rows)
	}
	if requests.Load() < 2 {
		t.Fatalf("steering did not continue the run: %d requests", requests.Load())
	}
	q.write("/quit\r")
	q.finish()
}

func TestActualAppCancelRestoresSteeringToEditor(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		mu.Lock()
		defer mu.Unlock()
		emit("message_start", map[string]any{"message": map[string]any{"id": "slow", "model": "m", "usage": map[string]any{"input_tokens": 1}}})
		time.Sleep(3 * time.Second)
		emit("message_stop", map[string]any{})
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("SLOW_ASK\r")
	q.write("CANCEL_STEER\r")
	q.wait("Steering: CANCEL_STEER")
	q.write("\x1b")
	q.wait("cancelled")
	q.prompt()
	if draft := q.screen.Text(); !strings.Contains(draft, "CANCEL_STEER") {
		t.Fatalf("undelivered steering was lost on cancel: %q", draft)
	}
	rows := append(append([]string{}, q.screen.Scrollback...), strings.Split(q.screen.Text(), "\n")...)
	if strings.Contains(strings.Join(rows, "\n"), "Steering: CANCEL_STEER") {
		t.Fatalf("cancelled steering kept its queued row: %q", rows)
	}
	q.write("\x03\x03")
	q.finish()
}

// Compaction runs inside a busy run: the editor stays active, and a steering
// message queued while compaction streams is delivered at the next turn
// boundary, after the summary, before the next model request.
func TestActualAppSteeringDuringCompaction(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(500 * time.Millisecond)
		emit("message_start", map[string]any{"message": map[string]any{"id": "c", "model": "m", "usage": map[string]any{"input_tokens": 1}}})
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "AFTER_COMPACT"}})
		emit("content_block_stop", map[string]any{"index": 0})
		emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}})
		emit("message_stop", map[string]any{})
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":15000,"keepRecentTokens":50},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":22000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("prior ", 6000)}}}}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("ASK_COMPACT\r")
	q.wait("Auto-compacting")
	q.write("STEER_COMPACT\r")
	q.wait("Steering: STEER_COMPACT")
	q.wait("AFTER_COMPACT")
	q.prompt()
	rows := append(append([]string{}, q.screen.Scrollback...), strings.Split(q.screen.Text(), "\n")...)
	if !strings.Contains(strings.Join(rows, "\n"), "› STEER_COMPACT") {
		t.Fatalf("steering queued during compaction was never delivered: %q", rows)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("provider requests = %d, want 2 (one compaction summary and one post-compaction request with steering)", len(bodies))
	}
	last := bodies[len(bodies)-1]
	if !strings.Contains(last, "STEER_COMPACT") || !strings.Contains(last, "ASK_COMPACT") {
		t.Fatalf("final request lost the steered message or the ask: %q", last)
	}
	q.write("/quit\r")
	q.finish()
}
