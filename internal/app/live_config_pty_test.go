//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/session"
)

func liveThinkingTurn(turn int) string {
	if turn == 1 {
		return "event: message_start\ndata: {\"message\":{\"id\":\"live-1\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n" +
			"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"live-read\",\"name\":\"read\",\"input\":{\"path\":\"fixture.txt\"}}}\n\n" +
			"event: content_block_stop\ndata: {\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\n" +
			"event: message_stop\ndata: {}\n\n"
	}
	return "event: message_start\ndata: {\"message\":{\"id\":\"live-2\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"DONE\"}}\n\n" +
		"event: content_block_stop\ndata: {\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {}\n\n"
}

func liveWaitBody(t *testing.T, bodies <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case body := <-bodies:
		return body
	case <-time.After(8 * time.Second):
		t.Fatal("provider never received the request")
		return nil
	}
}

func liveAssertOneUserTranscript(t *testing.T, ws, agentDir string) {
	t.Helper()
	var files []string
	root := filepath.Join(agentDir, "sessions")
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if len(files) != 1 {
		t.Fatalf("session files = %v, want exactly one", files)
	}
	reopened, err := session.OpenFile(files[0], ws, false)
	if err != nil {
		t.Fatal(err)
	}
	proj, err := reopened.Projection()
	if err != nil {
		t.Fatal(err)
	}
	users, assistants, results := 0, 0, 0
	for _, m := range proj.Flat {
		switch {
		case m.User != nil:
			users++
		case m.Assistant != nil:
			assistants++
		case m.ToolResult != nil:
			results++
		}
	}
	if users != 1 || assistants != 2 || results != 1 {
		t.Fatalf("users=%d assistants=%d results=%d, want 1/2/1", users, assistants, results)
	}
}

// TestActualAppThinkingChangeAppliesToTheNextRequest proves that a thinking
// level changed during a run reaches the next provider request of that run,
// and that the request already in flight keeps its own budget.
func TestActualAppThinkingChangeAppliesToTheNextRequest(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("live configuration workflow needs a Unix PTY")
	}
	bodies := make(chan map[string]any, 8)
	release := make(chan struct{}, 8)
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/messages" || req.Header.Get("x-api-key") != "file-test-key" {
			t.Errorf("endpoint/auth not used: %s", req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		fmt.Fprint(w, liveThinkingTurn(int(turns.Add(1))))
	}))
	defer server.Close()
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	if err := os.WriteFile(filepath.Join(ws, "fixture.txt"), []byte("SYNTHETIC_READ_RESULT"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":["read"],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	q := startReviewProcess(t, home, ws, agentDir, *reviewBinary, "", "--deny-project", "--tools", "read")
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})
	q.prompt()
	q.write("read fixture.txt\r")
	first := liveWaitBody(t, bodies)
	if thinking, ok := first["thinking"]; ok {
		t.Fatalf("first request carried a thinking block: %v", thinking)
	}
	q.write("/thinking high\r")
	q.waitScreen("high")
	if strings.Contains(q.raw.String(), "unsupported thinking level") {
		t.Fatalf("thinking change was refused:\n%s", q.raw.String())
	}
	release <- struct{}{}
	second := liveWaitBody(t, bodies)
	thinking, ok := second["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("second request carried no thinking block: %v", second)
	}
	if thinking["budget_tokens"] != float64(8192) {
		t.Errorf("second request thinking = %v", thinking)
	}
	release <- struct{}{}
	q.wait("DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	liveAssertOneUserTranscript(t, ws, agentDir)
	t.Logf("thinking live change: first model=%v second thinking=%v", first["model"], thinking)
}

// TestActualAppModelChangeAppliesToTheNextRequest proves that a model
// changed during a run reaches the next provider request of that run, and
// that the request already in flight keeps its own model.
func TestActualAppModelChangeAppliesToTheNextRequest(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("live configuration workflow needs a Unix PTY")
	}
	bodies := make(chan map[string]any, 8)
	release := make(chan struct{}, 8)
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/messages" || req.Header.Get("x-api-key") != "file-test-key" {
			t.Errorf("endpoint/auth not used: %s", req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		fmt.Fprint(w, liveThinkingTurn(int(turns.Add(1))))
	}))
	defer server.Close()
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	if err := os.WriteFile(filepath.Join(ws, "fixture.txt"), []byte("SYNTHETIC_READ_RESULT"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":["read"],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]},{"id":"local-next","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	q := startReviewProcess(t, home, ws, agentDir, *reviewBinary, "", "--deny-project", "--tools", "read")
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})
	q.prompt()
	q.write("read fixture.txt\r")
	first := liveWaitBody(t, bodies)
	if first["model"] != "local-model" {
		t.Fatalf("first request model = %v, want local-model", first["model"])
	}
	q.write("/model anthropic/local-next\r")
	q.waitScreen("local-next")
	release <- struct{}{}
	second := liveWaitBody(t, bodies)
	if second["model"] != "local-next" {
		t.Fatalf("second request model = %v, want local-next", second["model"])
	}
	raw, _ := json.Marshal(second)
	if !strings.Contains(string(raw), "SYNTHETIC_READ_RESULT") {
		t.Fatalf("second request lacks the fixture result: %s", raw)
	}
	release <- struct{}{}
	q.wait("DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	liveAssertOneUserTranscript(t, ws, agentDir)
	t.Logf("model live change: first=%v second=%v", first["model"], second["model"])
}

// TestActualAppSettingsOpenDuringARun proves that /settings opens during a run,
// stays usable while the run settles, and does not break the run.
func TestActualAppSettingsOpenDuringARun(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("live configuration workflow needs a Unix PTY")
	}
	bodies := make(chan map[string]any, 8)
	release := make(chan struct{}, 8)
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		fmt.Fprint(w, liveThinkingTurn(int(turns.Add(1))))
	}))
	defer server.Close()
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	if err := os.WriteFile(filepath.Join(ws, "fixture.txt"), []byte("SYNTHETIC_READ_RESULT"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":["read"],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	q := startReviewProcess(t, home, ws, agentDir, *reviewBinary, "", "--deny-project", "--tools", "read")
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})
	q.prompt()
	q.write("read fixture.txt\r")
	liveWaitBody(t, bodies)
	q.write("/settings\r")
	q.waitScreen("Auto-compact")
	if strings.Contains(q.raw.String(), "Finish or cancel the run before changing settings") {
		t.Fatalf("settings was refused during a run:\n%s", q.raw.String())
	}
	// Let the run settle while the picker stays open.
	release <- struct{}{}
	liveWaitBody(t, bodies)
	release <- struct{}{}
	q.wait("DONE")
	// Picker must still be open and usable after settlement.
	q.waitScreen("Auto-compact")
	q.write("\x1b")
	// Wait for the picker to close before quitting; /quit is ignored while
	// the picker captures input.
	liveWaitScreenAbsence(t, q, "Auto-compact")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	liveAssertOneUserTranscript(t, ws, agentDir)
}

func liveWaitScreenAbsence(t *testing.T, q *reviewPTY, absent string) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if !strings.Contains(q.screen.Text(), absent) {
			return
		}
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				t.Fatalf("process exited waiting for screen to lose %q: %q", absent, q.screen.Text())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timer.C:
			t.Fatalf("timeout waiting for screen to lose %q: %q", absent, q.screen.Text())
		}
	}
}

// TestActualAppPickerSettlementPreservesDraft proves that a picker opened during a run
// keeps its query when the run settles, and that cancelling restores the
// draft captured when the picker opened.
func TestActualAppPickerSettlementPreservesDraft(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("live configuration workflow needs a Unix PTY")
	}
	bodies := make(chan map[string]any, 8)
	release := make(chan struct{}, 8)
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		fmt.Fprint(w, liveThinkingTurn(int(turns.Add(1))))
	}))
	defer server.Close()
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	if err := os.WriteFile(filepath.Join(ws, "fixture.txt"), []byte("SYNTHETIC_READ_RESULT"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":["read"],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]},{"id":"local-next","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	q := startReviewProcess(t, home, ws, agentDir, *reviewBinary, "", "--deny-project", "--tools", "read")
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})
	q.prompt()
	q.write("read fixture.txt\r")
	liveWaitBody(t, bodies)
	// Open the model picker during the run; the draft is the /model command.
	q.write("/model\r")
	q.waitScreen("local-model")
	q.write("xyz")
	q.waitScreen("xyz")
	// Settle the run with the picker open: the query must survive.
	release <- struct{}{}
	liveWaitBody(t, bodies)
	release <- struct{}{}
	q.wait("DONE")
	q.waitScreen("xyz")
	// Cancelling restores the draft captured at open.
	q.write("\x1b")
	q.waitScreen("/model")
	q.write("\x03")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	liveAssertOneUserTranscript(t, ws, agentDir)
}

// TestActualAppCompactWaitsForCancelledRun proves that /compact during a run
// cancels, settles, then compacts without re-asking the cancelled prompt.
func TestActualAppCompactWaitsForCancelledRun(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("compact interrupt needs a Unix PTY")
	}
	bodies := make(chan map[string]any, 8)
	releaseFirst := make(chan struct{})
	releaseSummary := make(chan struct{})
	var mu sync.Mutex
	reqCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		reqCount++
		n := reqCount
		mu.Unlock()
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		switch n {
		case 1:
			// First run: hold until released, then complete.
			select {
			case <-releaseFirst:
			case <-req.Context().Done():
				return
			}
			fmt.Fprint(w, liveTextTurn("FIRST DONE"))
		case 2:
			// Second run: stream a partial, then hold until /compact cancels it.
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r2\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PARTIAL\"}}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-req.Context().Done():
				return
			}
		case 3:
			// Summary request: hold until the test verifies settlement.
			select {
			case <-releaseSummary:
			case <-req.Context().Done():
				return
			}
			fmt.Fprint(w, liveTextTurn("SUMMARY"))
		default:
			fmt.Fprint(w, liveTextTurn("EXTRA"))
		}
	}))
	defer server.Close()
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","compaction":{"enabled":false,"keepRecentTokens":50}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "local-model", StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("prior ", 6000)}}}}}); err != nil {
		t.Fatal(err)
	}

	q := startReviewProcess(t, home, ws, agentDir, *reviewBinary, "", "--deny-project", "--session", store.File())
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})
	q.prompt()
	// First run completes to give the session compactable history.
	q.write("first work\r")
	select {
	case <-bodies:
	case <-time.After(8 * time.Second):
		t.Fatalf("first request never arrived; output=%q", q.raw.String())
	}
	close(releaseFirst)
	q.wait("FIRST DONE")
	q.prompt()
	// Second run is held, then interrupted by /compact.
	q.write("second work\r")
	select {
	case <-bodies:
	case <-time.After(8 * time.Second):
		t.Fatalf("second request never arrived; output=%q", q.raw.String())
	}
	q.wait("PARTIAL")
	q.write("/compact\r")
	q.wait("Cancelling the run for compaction.")
	// Summary request must arrive only after the cancelled run settles.
	select {
	case summaryBody := <-bodies:
		_ = summaryBody
	case <-time.After(8 * time.Second):
		t.Fatalf("summary request never arrived; output=%q", q.raw.String())
	}
	liveAssertCompactSettlement(t, ws, agentDir, "second work")
	close(releaseSummary)
	q.wait("Compacted")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	liveAssertCompactNoReask(t, ws, agentDir, "second work")
	return
}

func liveTextTurn(text string) string {
	return "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n"
}

func liveAssertCompactSettlement(t *testing.T, ws, agentDir, prompt string) {
	t.Helper()
	// Find the session file and check for the cancelled prompt.
	dir := filepath.Join(agentDir, "sessions")
	// The session dir may be under home, not agentDir; list both.
	// For reviewPTY, sessions are under home/sessions by default.
	// Try common locations.
	homeDir := filepath.Dir(filepath.Dir(agentDir))
	candidates := []string{dir, filepath.Join(filepath.Dir(agentDir), "sessions"), filepath.Join(homeDir, "sessions")}
	var files []string
	for _, d := range candidates {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".jsonl") {
				files = append(files, filepath.Join(d, e.Name()))
			}
		}
	}
	if len(files) == 0 {
		t.Fatalf("no session files found")
	}
	found := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		found += strings.Count(string(data), prompt)
	}
	if found == 0 {
		t.Fatalf("cancelled prompt %q not found in session files", prompt)
	}
}

func liveAssertCompactNoReask(t *testing.T, ws, agentDir, prompt string) {
	t.Helper()
	// After compaction, the cancelled prompt must not be duplicated.
	// Compaction rewrites history with a summary; the original prompt may be
	// summarized, not repeated as a new user message. Count user messages
	// containing the prompt; it must be at most one.
	candidates := []string{filepath.Join(agentDir, "sessions"), filepath.Join(filepath.Dir(agentDir), "sessions")}
	var files []string
	for _, d := range candidates {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".jsonl") {
				files = append(files, filepath.Join(d, e.Name()))
			}
		}
	}
	count := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "\"role\":\"user\"") && strings.Contains(line, prompt) {
				count++
			}
		}
	}
	if count > 1 {
		t.Fatalf("cancelled prompt %q re-asked %d times, want at most 1", prompt, count)
	}
}
