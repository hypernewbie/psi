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
	"time"

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

// The delayed timer must fire with the end-of-turn reason, not a manual
// command, and must not inject a resume prompt. After the checkpoint the
// prompt returns and the next message continues from compacted context.
// A non-zero delay exercises the real timer fire path; the immediate
// (delay 0) path is already pinned by TestActualAppACEndOfTurnCompaction.
func TestACEndTurnTimerFiresWithReasonAndContinues(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var kinds []string
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
		mu.Lock()
		kinds = append(kinds, kind)
		n := len(kinds)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if kind == "answer" {
			if n == 1 {
				writeAnthropicTestResponseWithUsage(w, "timer-1", "TIMER_FIRST_ANSWER", 60_000, 10)
			} else {
				writeAnthropicTestResponse(w, "timer-2", "TIMER_SECOND_ANSWER")
			}
		} else {
			writeAnthropicTestResponse(w, "timer-sum", "TIMER_SUMMARY")
		}
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":0,"keepRecentTokens":100},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":100000}]}}}`)
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
	q.write("/ac end-turn 50000\r")
	q.wait("End-of-turn compaction threshold set to 50,000 tokens")
	q.write("/ac end-turn-delay 0.02\r")
	q.wait("delay set to 0.02")
	q.write("TRIGGER_TIMER\r")
	q.wait("TIMER_FIRST_ANSWER")
	q.wait("Compacted from")
	q.write("/ac status\r")
	q.wait("last compaction: end-of-turn")
	q.write("FOLLOW_UP_AFTER_TIMER\r")
	q.wait("TIMER_SECOND_ANSWER")
	q.write("/quit\r")
	q.finish()
	_, entries, _, err := session.ReadFileEntries(store.File())
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, resumes := 0, 0
	for _, e := range entries {
		if e.Compaction != nil {
			checkpoints++
		}
		if e.Message != nil && e.Message.Message.User != nil && strings.Contains(fmt.Sprint(e.Message.Message.User.Content), "Continue the current task from the compacted context") {
			resumes++
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if checkpoints != 1 || resumes != 0 {
		t.Fatalf("checkpoints=%d resumes=%d; want one end-turn checkpoint and no resume prompt", checkpoints, resumes)
	}
	if len(kinds) != 3 || kinds[0] != "answer" || kinds[1] != "summary" || kinds[2] != "answer" {
		t.Fatalf("requests=%v; want answer, end-turn summary, answer", kinds)
	}
}

// Steering typed while the end-turn summary is in flight must continue
// after the checkpoint. The continuation streams before the outer
// compaction box settles, so wait for the answer before the box.
func TestACEndTurnTimerSteeringContinues(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var kinds []string
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
		mu.Lock()
		kinds = append(kinds, kind)
		n := len(kinds)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if kind == "summary" {
			select {
			case summaryStarted <- struct{}{}:
			default:
			}
			<-releaseSummary
			writeAnthropicTestResponse(w, "steer-sum", "STEER_SUMMARY")
			return
		}
		if n == 1 {
			writeAnthropicTestResponseWithUsage(w, "steer-1", "STEER_FIRST_ANSWER", 60_000, 10)
		} else {
			writeAnthropicTestResponse(w, "steer-2", "STEER_SECOND_ANSWER")
		}
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":0,"keepRecentTokens":100},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":100000}]}}}`)
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
	q.write("/ac end-turn 50000\r")
	q.wait("End-of-turn compaction threshold set to 50,000 tokens")
	q.write("/ac end-turn-delay 0.02\r")
	q.wait("delay set to 0.02")
	q.write("TRIGGER_STEER_TIMER\r")
	q.wait("STEER_FIRST_ANSWER")
	select {
	case <-summaryStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("timer compaction never started")
	}
	q.write("STEER_DURING_TIMER\r")
	q.wait("Steering: STEER_DURING_TIMER")
	close(releaseSummary)
	q.wait("STEER_SECOND_ANSWER")
	q.wait("Compacted from")
	q.write("/quit\r")
	q.finish()
	_, entries, _, err := session.ReadFileEntries(store.File())
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, resumes := 0, 0
	for _, e := range entries {
		if e.Compaction != nil {
			checkpoints++
		}
		if e.Message != nil && e.Message.Message.User != nil && strings.Contains(fmt.Sprint(e.Message.Message.User.Content), "Continue the current task from the compacted context") {
			resumes++
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if checkpoints != 1 || resumes != 0 {
		t.Fatalf("checkpoints=%d resumes=%d; want one end-turn checkpoint and no resume prompt", checkpoints, resumes)
	}
	if len(kinds) != 3 || kinds[0] != "answer" || kinds[1] != "summary" || kinds[2] != "answer" {
		t.Fatalf("requests=%v; want answer, end-turn summary, steered answer", kinds)
	}
}

// A new submission cancels the armed timer so no spurious compaction runs.
// The long delay never fires; the test quits immediately after the second
// answer, keeping runtime bounded.
func TestACEndTurnTimerCancelledOnInput(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var kinds []string
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
		mu.Lock()
		kinds = append(kinds, kind)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if kind == "answer" {
			writeAnthropicTestResponseWithUsage(w, "cancel", "CANCEL_ANSWER", 60_000, 10)
		} else {
			writeAnthropicTestResponse(w, "cancel-sum", "CANCEL_SUMMARY")
		}
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":0,"keepRecentTokens":100},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":100000}]}}}`)
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
	q.write("/ac end-turn 50000\r")
	q.wait("End-of-turn compaction threshold set to 50,000 tokens")
	q.write("/ac end-turn-delay 10\r")
	q.wait("delay set to 10")
	q.write("FIRST_CANCEL\r")
	q.wait("CANCEL_ANSWER")
	q.write("SECOND_CANCEL\r")
	q.wait("CANCEL_ANSWER")
	q.write("/ac status\r")
	q.wait("pi-ac: automatic compaction enabled")
	q.write("/quit\r")
	q.finish()
	mu.Lock()
	defer mu.Unlock()
	for _, kind := range kinds {
		if kind == "summary" {
			t.Fatalf("cancelled timer still compacted: %v", kinds)
		}
	}
	if len(kinds) != 2 {
		t.Fatalf("requests=%v; want two answers and no summary", kinds)
	}
}

// The timer must re-arm after a compacted cycle when new work grows
// over threshold again. Both cycles use large text so a second cut is
// eligible; a tiny-text follow-up would correctly report no work.
func TestACEndTurnTimerRearmsAfterNewGrowth(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var kinds []string
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
		mu.Lock()
		kinds = append(kinds, kind)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if kind == "answer" {
			writeAnthropicTestResponseWithUsage(w, "rearm", strings.Repeat("rearm large answer ", 800), 60_000, 10)
		} else {
			writeAnthropicTestResponse(w, "rearm-sum", "REARM_SUMMARY")
		}
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":0,"keepRecentTokens":100},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":100000}]}}}`)
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
	q.write("/ac end-turn 50000\r")
	q.wait("End-of-turn compaction threshold set to 50,000 tokens")
	q.write("/ac end-turn-delay 0.02\r")
	q.wait("delay set to 0.02")
	q.write("FIRST_REARM " + strings.Repeat("x ", 500) + "\r")
	q.wait("rearm large answer")
	q.wait("Compacted from")
	q.write("SECOND_REARM " + strings.Repeat("y ", 500) + "\r")
	q.wait("rearm large answer")
	q.wait("Compacted from")
	q.write("/quit\r")
	q.finish()
	mu.Lock()
	defer mu.Unlock()
	// Large answers exceed the retained tail, so each end-turn checkpoint
	// is a split-turn pair. The pin is two full cycles: the timer re-arms
	// after new over-threshold growth and fires again.
	if len(kinds) != 6 || kinds[0] != "answer" || kinds[1] != "summary" || kinds[2] != "summary" || kinds[3] != "answer" || kinds[4] != "summary" || kinds[5] != "summary" {
		t.Fatalf("requests=%v; want answer,summary,summary,answer,summary,summary (timer re-arm)", kinds)
	}
}
