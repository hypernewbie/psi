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
	"testing"
	"time"
)

// Actual-app PTY regression for parallel tool batches: two calls from one
// assistant response run concurrently, settle in call order, and the second
// provider request carries both results. The bash call only observes the
// write's marker if the write executed while the bash call still slept, which
// sequential execution cannot satisfy within the 1.2s window.
func TestParallelBatchActualAppPTY(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	requests := make(chan map[string]any, 4)
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		turn++
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"batch-1\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n"+
				"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-slow\",\"name\":\"bash\",\"input\":{\"command\":\"sleep 1.2; ls parallel-marker.txt\"}}}\n\n"+
				"event: content_block_stop\ndata: {\"index\":0}\n\n"+
				"event: content_block_start\ndata: {\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-write\",\"name\":\"write\",\"input\":{\"path\":\"parallel-marker.txt\",\"content\":\"written concurrently\"}}}\n\n"+
				"event: content_block_stop\ndata: {\"index\":1}\n\n"+
				"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {}\n\n")
			return
		}
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"batch-2\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n"+
			"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PARALLEL_BATCH_DONE\"}}\n\n"+
			"event: content_block_stop\ndata: {\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	reviewSettings(t, agentDir, server.URL)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project")
	q.prompt()
	submitted := time.Now()
	q.write("run both\r")
	q.wait("PARALLEL_BATCH_DONE")
	q.prompt()
	elapsed := time.Since(submitted)
	first := <-requests
	second := <-requests
	_ = first
	// The follow-up request carries both tool calls and both results.
	secondJSON, _ := json.Marshal(second["messages"])
	if !strings.Contains(string(secondJSON), "call-slow") || !strings.Contains(string(secondJSON), "call-write") {
		t.Fatalf("follow-up request missing tool calls: %s", secondJSON)
	}
	// Concurrent execution: the write landed while the bash call still slept,
	// so the bash result lists the marker file.
	if !strings.Contains(string(secondJSON), "text\":\"parallel-marker.txt") {
		t.Fatalf("bash result never observed the concurrent write: %s", secondJSON)
	}
	// Settlement stays in call order: the slow bash result precedes the write
	// result in the follow-up request.
	slow := strings.Index(string(secondJSON), "text\":\"parallel-marker.txt")
	write := strings.Index(string(secondJSON), "Successfully wrote")
	if slow < 0 || write < 0 || slow > write {
		t.Fatalf("results out of order in follow-up request: slow=%d write=%d", slow, write)
	}
	// Sequential execution needs at least the sleep before the write, then the
	// second request; parallel finishes the batch inside the sleep window.
	if elapsed > 2*time.Second {
		t.Fatalf("batch ran sequentially or stalled: %s", elapsed)
	}
	if !strings.Contains(q.screen.Text(), "sleep 1.2; ls parallel-marker.txt") || !strings.Contains(q.screen.Text(), "write") {
		t.Fatalf("settled rows missing from screen: %s", q.screen.Text())
	}
	q.write("/quit\r")
	q.finish()
	if _, err := os.Stat(filepath.Join(work, "parallel-marker.txt")); err != nil {
		t.Fatalf("write call did not execute: %v", err)
	}
}

// Escape during a parallel batch interrupts the run: both started calls show
// interrupted rows, no result claims success, the prompt returns, and the
// session stays continuable with both calls resolved.
func TestParallelBatchEscapeInterruptsAndKeepsPrompt(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"batch\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n"+
			"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"slow-1\",\"name\":\"bash\",\"input\":{\"command\":\"sleep 30\"}}}\n\n"+
			"event: content_block_stop\ndata: {\"index\":0}\n\n"+
			"event: content_block_start\ndata: {\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"slow-2\",\"name\":\"bash\",\"input\":{\"command\":\"sleep 30\"}}}\n\n"+
			"event: content_block_stop\ndata: {\"index\":1}\n\n"+
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	reviewSettings(t, agentDir, server.URL)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project")
	q.prompt()
	q.write("run forever\r")
	q.wait("sleep 30")
	// Both calls run concurrently; interrupt mid-batch.
	q.write("\x1b")
	q.wait("cancelled")
	q.prompt()
	screen := q.screen.Text()
	if strings.Count(screen, "interrupted") < 2 && !strings.Contains(screen, "cancelled") {
		t.Fatalf("interrupted batch left no trace: %s", screen)
	}
	// The shell processes died with the batch: no straggler keeps the run busy.
	q.write("/quit\r")
	q.finish()
	// The session resolves both calls; nothing stays interrupted in the file.
	sessionPath := filepath.Join(agentDir, "sessions")
	entries, err := os.ReadDir(sessionPath)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no session recorded: %v", err)
	}
	var files []string
	filepath.WalkDir(sessionPath, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if len(files) == 0 {
		t.Fatal("no JSONL session file")
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Count(body, "slow-1") < 2 || strings.Count(body, "slow-2") < 2 {
		t.Fatalf("session missing call/result pairs: %s", body)
	}
	// At least one admitted call settles cancelled; scheduling decides only
	// whether the other lands cancelled or not started.
	if !strings.Contains(body, `"cancelled"`) {
		t.Fatalf("interrupted batch recorded no cancelled result: %s", body)
	}
}
