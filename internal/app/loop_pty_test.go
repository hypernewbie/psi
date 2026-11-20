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

	"github.com/hypernewbie/psi/internal/loop"
	"github.com/hypernewbie/psi/internal/session"
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

func TestActualAppGoalLoopConfirmsClaimBeforeClosing(t *testing.T) {
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
		switch turn {
		case 1:
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"loop-1\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"done-1\",\"name\":\"loop_done\",\"input\":{\"reason\":\"first iteration complete\"}}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {}\n\n")
		case 2:
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"loop-2\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"approve-1\",\"name\":\"loop_approve\",\"input\":{\"verdict\":\"reached\",\"reason\":\"verified the work\"}}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {}\n\n")
		default:
			fmt.Fprint(w, anthropicPTYText("LOOP_FINAL"))
		}
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-loop-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("/loop goal polish the implementation\r")
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
	// Request 1 exposes the goal loop's single verb and the always-available
	// history tool. A goal loop has no loop_next: the controller advances it.
	if !strings.Contains(string(bodies[0]), "loop_done") || !strings.Contains(string(bodies[0]), "Iteration 1") {
		t.Fatalf("request 1 lacks the working-phase tool/instructions: %s", bodies[0])
	}
	if tools := requestsJSON[0]["tools"].([]any); len(tools) != 2 ||
		tools[0].(map[string]any)["name"] != "loop_done" ||
		tools[1].(map[string]any)["name"] != "read_session" {
		t.Fatalf("request 1 tools = %#v", tools)
	}
	// Request 2 replaces loop_done with loop_approve.
	if !strings.Contains(string(bodies[1]), "loop_approve") || !strings.Contains(string(bodies[1]), "Confirmation check") {
		t.Fatalf("request 2 lacks the checking-phase tool/instructions: %s", bodies[1])
	}
	if tools := requestsJSON[1]["tools"].([]any); len(tools) != 2 || tools[0].(map[string]any)["name"] != "loop_approve" || tools[1].(map[string]any)["name"] != "read_session" {
		t.Fatalf("request 2 tools = %#v", tools)
	}
	// The confirmed loop is closed: only the history tool remains.
	if tools := requestsJSON[2]["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["name"] != "read_session" {
		t.Fatalf("completed loop tool set: %#v", tools)
	}
	if strings.Contains(string(bodies[2]), "## Active Loop") {
		t.Fatalf("completed loop system prompt stayed active: %s", bodies[2])
	}
}

// A count loop advances on loop_next, does not count that iteration again at
// the turn end, and closes itself after the last pass.
func TestActualAppCountLoopCompletesAfterLoopNext(t *testing.T) {
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
		switch turn {
		case 1:
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"pass-1\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"next-1\",\"name\":\"loop_next\",\"input\":{\"summary\":\"first pass done\"}}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {}\n\n")
		case 2:
			fmt.Fprint(w, anthropicPTYText("PASS_TWO_WORK"))
		default:
			fmt.Fprint(w, anthropicPTYText("PASS_TWO_CONTINUED"))
		}
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-loop-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("/loop 2 polish the implementation\r")
	q.wait("PASS_TWO_CONTINUED")
	q.prompt()
	if requests.Load() != 3 {
		t.Fatalf("provider requests = %d, want 3", requests.Load())
	}
	q.write("/quit\r")
	q.finish()

	// The completion notice is durable in the transcript (visible when the
	// session is reopened) even though the run ends without another turn.
	sessions, err := session.Discover(session.DefaultSessionDir(work, agentDir))
	if err != nil || len(sessions) != 1 {
		t.Fatalf("session discover = %v, %v", sessions, err)
	}
	reopened, err := session.OpenFile(sessions[0].Path, work, true)
	if err != nil {
		t.Fatal(err)
	}
	path, err := reopened.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	var state loop.State
	for _, entry := range path {
		if entry.CustomMessage != nil && entry.CustomMessage.CustomType == loop.CompleteMessageType {
			notices++
			if !entry.CustomMessage.Display {
				t.Fatal("completion notice is hidden")
			}
		}
		if entry.Custom != nil && entry.Custom.CustomType == loop.StateEntryType {
			if err := json.Unmarshal(entry.Custom.Data, &state); err != nil {
				t.Fatal(err)
			}
		}
	}
	if notices != 1 || state.Active || !state.Done || state.MaxSteps != 2 || state.CurrentStep != 2 {
		t.Fatalf("notices=%d state=%+v", notices, state)
	}

	bodiesMu.Lock()
	defer bodiesMu.Unlock()
	for index, want := range []string{"Pass 1 of 2", "Pass 2 of 2", "The loop is still active — pass 2/2."} {
		if !strings.Contains(string(bodies[index]), want) {
			t.Fatalf("request %d lacks %q: %s", index+1, want, bodies[index])
		}
	}
}
