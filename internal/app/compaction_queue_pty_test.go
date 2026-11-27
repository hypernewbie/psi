//go:build unix

package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/session"
)

// TestActualAppQueuesMessageDuringCompaction covers Pi's compaction queue:
// a message typed while a compaction runs waits for the checkpoint. The
// summary is generated without it, the status says it waits, and the
// message runs with the turn after the checkpoint.
func TestActualAppQueuesMessageDuringCompaction(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var kinds []string
	var bodies []string
	summaryStarted := make(chan struct{}, 1)
	releaseSummary := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		encoded, _ := json.Marshal(body["system"])
		kind := "answer"
		if strings.Contains(string(encoded), "context summarization assistant") {
			kind = "summary"
		}
		raw, _ := json.Marshal(body)
		mu.Lock()
		kinds = append(kinds, kind)
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if kind == "summary" {
			select {
			case summaryStarted <- struct{}{}:
			default:
			}
			<-releaseSummary
			writeAnthropicTestResponse(w, "summary", "SUMMARY")
			return
		}
		writeAnthropicTestResponse(w, "answer", "ANSWER_AFTER_QUEUE")
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":0,"keepRecentTokens":100},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-compact-queue-key"}}`)
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
	q.write("/compact\r")
	select {
	case <-summaryStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the compaction never started")
	}
	q.write("QUEUED_DURING_COMPACTION\r")
	q.wait("Queued message for after compaction")
	q.wait("Steering: QUEUED_DURING_COMPACTION")
	close(releaseSummary)
	q.wait("ANSWER_AFTER_QUEUE")
	q.write("/quit\r")
	q.finish()
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) != 2 || kinds[0] != "summary" || kinds[1] != "answer" {
		t.Fatalf("requests=%v; want the summary and one answer", kinds)
	}
	if strings.Contains(bodies[0], "QUEUED_DURING_COMPACTION") {
		t.Fatalf("the summary carries the queued message: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], "QUEUED_DURING_COMPACTION") {
		t.Fatalf("the queued message did not run after the checkpoint: %s", bodies[1])
	}
}

// TestActualAppDequeueRestoresQueuedMessages covers Pi's dequeue action: the
// queued messages leave the run's queue and come back to the editor, so the
// user can edit them before they run.
func TestActualAppDequeueRestoresQueuedMessages(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Hold the response open so the run stays in flight while the
		// message is queued and taken back.
		compactHoldPartial(w, req, "HELD_PARTIAL", stop)
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-dequeue-key"}}`)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("START_DEQUEUE\r")
	q.wait("HELD_PARTIAL")
	// The request is in flight; queue a message and take it back.
	q.write("QUEUED_FOR_DEQUEUE\r")
	q.wait("Steering: QUEUED_FOR_DEQUEUE")
	q.write("/dequeue\r")
	// The restored draft is drawn before the status line.
	q.wait("QUEUED_FOR_DEQUEUE")
	q.wait("Restored 1 queued message to editor")
	// Release the held response so the run can settle before the quit.
	close(stop)
	// The restored draft is in the editor; clear it before the quit.
	q.write("\x15")
	q.write("/quit\r")
	q.finish()
}
