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
)

func TestActualAppLoopStopWorksWhileTurnIsRunning(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `event: message_start
data: {"message":{"id":"halt","model":"synthetic-model","usage":{"input_tokens":1}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"index":0,"delta":{"type":"text_delta","text":"HALTED_TURN"}}

`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-release
		fmt.Fprint(w, `event: content_block_stop
data: {"index":0}

event: message_delta
data: {"delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {}

`)
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-loop-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("/loop goal finish the current task\r")
	<-started
	q.wait("HALTED_TURN")
	q.write("/loop-stop\r")
	q.wait("Loop stopped")
	close(release)
	q.prompt()
	if requests.Load() != 1 {
		t.Fatalf("provider requests after loop stop = %d, want 1", requests.Load())
	}
	q.write("/quit\r")
	q.finish()
}

func TestActualAppExactCountLoopUsesDynamicControlTool(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var requests atomic.Int32
	var bodiesMu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		bodiesMu.Lock()
		bodies = append(bodies, body)
		bodiesMu.Unlock()
		turn := int(requests.Add(1))
		w.Header().Set("Content-Type", "text/event-stream")
		if turn < 3 {
			fmt.Fprintf(w, "event: message_start\ndata: {\"message\":{\"id\":\"loop-%d\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1}}}\n\n", turn)
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"control-%d\",\"name\":\"loop_control\",\"input\":{\"status\":\"next\",\"summary\":\"pass %d complete\"}}}\n\n", turn, turn)
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {}\n\n")
			return
		}
		fmt.Fprint(w, anthropicPTYText("LOOP_FINAL"))
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-loop-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("/loop 2 polish the implementation\r")
	q.wait("LOOP_FINAL")
	q.prompt()
	if requests.Load() != 3 {
		t.Fatalf("provider requests = %d, want 3", requests.Load())
	}
	q.write("/quit\r")
	q.finish()

	bodiesMu.Lock()
	defer bodiesMu.Unlock()
	var requestsJSON []map[string]any
	for _, body := range bodies {
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		requestsJSON = append(requestsJSON, request)
	}
	for index, step := range []string{"Pass 1 of 2", "Pass 2 of 2"} {
		if !strings.Contains(string(bodies[index]), "loop_control") || !strings.Contains(string(bodies[index]), step) {
			t.Fatalf("request %d lacks active loop instructions/tool: %s", index+1, bodies[index])
		}
		if len(requestsJSON[index]["tools"].([]any)) != 1 {
			t.Fatalf("request %d tools = %#v", index+1, requestsJSON[index]["tools"])
		}
	}
	if _, present := requestsJSON[2]["tools"]; present {
		t.Fatalf("completed loop still exposed control tools: %#v", requestsJSON[2]["tools"])
	}
	if strings.Contains(string(bodies[2]), "## Active Loop") {
		t.Fatalf("completed loop system prompt stayed active: %s", bodies[2])
	}
}

// TestActualAppLoopQueuesDuringRun proves busy /loop queues in the deferred
// FIFO, refuses a duplicate without replacement, and starts after the active
// run settles.
func TestActualAppLoopQueuesDuringRun(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	release := make(chan struct{})
	var mu sync.Mutex
	var bodies []string
	reqCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqCount++
		n := reqCount
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if n == 1 {
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"LOOP_HELD\"}}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, liveTextTurn("LOOP_TURN_DONE"))
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-loop-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("held work\r")
	q.wait("LOOP_HELD")
	q.write("/loop goal finish the queued task\r")
	q.wait("Loop queued until the active run settles.")
	q.write("/loop goal duplicate task\r")
	q.wait("A /loop request is already queued.")
	close(release)
	q.wait("LOOP_TURN_DONE")
	q.wait("LOOP_TURN_DONE")
	q.write("/loop-stop\r")
	q.wait("Loop stopped")
	q.prompt()
	q.write("/quit\r")
	q.finish()

	mu.Lock()
	defer mu.Unlock()
	if reqCount < 2 {
		t.Fatalf("request count = %d, want at least 2 (held run + loop start)", reqCount)
	}
	if strings.Contains(bodies[0], "finish the queued task") {
		t.Fatalf("first request should not contain loop goal: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], "finish the queued task") {
		t.Fatalf("loop start request missing queued goal: %s", bodies[1])
	}
	if strings.Contains(bodies[1], "duplicate task") {
		t.Fatalf("loop start request used duplicate goal (replacement): %s", bodies[1])
	}
}

// TestActualAppDeferredLoopStartsWithPickerOpen proves deferred FIFO work
// executes at settlement even when a picker is open, instead of stranding
// until a later settlement.
func TestActualAppDeferredLoopStartsWithPickerOpen(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	release := make(chan struct{})
	var mu sync.Mutex
	var bodies []string
	reqCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqCount++
		n := reqCount
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if n == 1 {
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"LOOP_HELD\"}}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if n == 1 {
			// Complete the held stream inside the same message, so the run
			// settles successfully instead of erroring on a second start.
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"LOOP_TURN_DONE\"}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n")
		} else {
			fmt.Fprint(w, liveTextTurn("LOOP_TURN_DONE"))
		}
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-loop-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("held work\r")
	q.wait("LOOP_HELD")
	q.write("/loop goal picker open task\r")
	q.wait("Loop queued until the active run settles.")
	// Open settings while the run is held so the picker is open at settlement.
	q.write("/settings\r")
	q.waitScreen("Auto-compact")
	close(release)
	// The held run completes, then the queued loop starts with the picker
	// still open: no close is needed and nothing strands.
	q.wait("LOOP_TURN_DONE")
	q.wait("LOOP_TURN_DONE")
	q.waitScreen("Auto-compact")
	q.write("\x1b")
	liveWaitScreenAbsence(t, q, "Auto-compact")
	q.write("/loop-stop\r")
	q.wait("Loop stopped")
	q.prompt()
	q.write("/quit\r")
	q.finish()

	mu.Lock()
	defer mu.Unlock()
	if reqCount < 2 {
		t.Fatalf("request count = %d, want at least 2 (held run + loop start)", reqCount)
	}
	if strings.Contains(bodies[0], "picker open task") {
		t.Fatalf("first request should not contain loop goal: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], "picker open task") {
		t.Fatalf("loop start request missing queued goal: %s", bodies[1])
	}
}
