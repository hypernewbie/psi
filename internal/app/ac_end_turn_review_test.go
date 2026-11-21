//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hypernewbie/psi/internal/session"
)

// A native compaction before a request must not suppress the end-turn timer
// when that request reports a new, independently over-target context.
func TestACEndTurnAfterEarlierThresholdCompaction(t *testing.T) {
	t.Run("provider usage", func(t *testing.T) { testACEndTurnAfterThreshold(t, true) })
	t.Run("estimated growth without usage", func(t *testing.T) { testACEndTurnAfterThreshold(t, false) })
}

func testACEndTurnAfterThreshold(t *testing.T, reportUsage bool) {
	t.Helper()
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		system, _ := json.Marshal(body["system"])
		kind := "answer"
		if strings.Contains(string(system), "context summarization assistant") {
			kind = "summary"
		}
		mu.Lock()
		requests = append(requests, kind)
		n := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if kind == "answer" {
			input, output := 0, 0
			if reportUsage {
				input, output = 60_000, 20
			}
			writeAnthropicTestResponseWithUsage(w, fmt.Sprintf("end-%d", n), strings.Repeat("EOT_NEW_ANSWER ", 300), input, output)
		} else {
			writeAnthropicTestResponse(w, fmt.Sprintf("end-%d", n), "SUMMARY")
		}
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":16384,"keepRecentTokens":10},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-ac-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("older task")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop",
		Usage:   session.Usage{TotalTokens: 95_000},
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("older context ", 800)}}},
	}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	threshold, thresholdLabel := "1000", "1,000"
	if reportUsage {
		threshold, thresholdLabel = "50000", "50,000"
	}
	q.write("/ac end-turn " + threshold + "\r")
	q.wait("End-of-turn compaction threshold set to " + thresholdLabel + " tokens")
	q.write("/ac end-turn-delay 0\r")
	q.wait("delay set to 0 minutes")
	q.write("finish this task\r")
	q.wait("EOT_NEW_ANSWER")
	q.wait("Compacted from 95,") // native pre-request checkpoint
	q.wait("Compacted from")     // end-of-turn checkpoint from the new reply
	q.write("/ac status\r")
	q.wait("last compaction: end-of-turn")
	q.write("/quit\r")
	q.finish()

	_, entries, _, err := session.ReadFileEntries(store.File())
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	for _, e := range entries {
		if e.Compaction != nil {
			checkpoints++
			if checkpoints == 2 && reportUsage && e.Compaction.TokensBefore != 60_020 {
				t.Fatalf("end-turn checkpoint tokensBefore = %v, want 60,020", e.Compaction.TokensBefore)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if checkpoints != 2 || len(requests) != 3 || requests[0] != "summary" || requests[1] != "answer" || requests[2] != "summary" {
		t.Fatalf("checkpoints=%d requests=%v; want summary, answer, end-turn summary", checkpoints, requests)
	}
}
