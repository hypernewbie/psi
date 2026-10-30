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
