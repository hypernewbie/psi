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
	"testing"

	"psi/internal/session"
)

// TestActualAppReloadRetriesOriginalInput proves busy /reload cancels the run,
// reloads local instructions at settlement, then replays the original prompt
// as a new turn with the updated system prompt.
func TestActualAppReloadRetriesOriginalInput(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("reload replay needs a Unix PTY")
	}
	bodies := make(chan map[string]any, 8)
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
			// Original run: stream a partial, then hold until /reload cancels it.
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"RELOAD_PARTIAL\"}}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-req.Context().Done():
				return
			}
		case 2:
			// Retry after reload: complete immediately.
			fmt.Fprint(w, liveTextTurn("RETRIED DONE"))
		default:
			fmt.Fprint(w, liveTextTurn("EXTRA"))
		}
	}))
	defer server.Close()
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)
	writeAppFile(t, filepath.Join(ws, "AGENTS.md"), "# Instructions\n\nINITIAL_INSTRUCTIONS_MARKER\n")
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}

	q := startReviewProcess(t, home, ws, agentDir, *reviewBinary, "", "--deny-project", "--session", store.File())
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})
	q.prompt()
	q.write("original work\r")
	q.wait("original work")
	q.wait("RELOAD_PARTIAL")
	// Update local instructions before reload; the retry must see them.
	writeAppFile(t, filepath.Join(ws, "AGENTS.md"), "# Instructions\n\nUPDATED_INSTRUCTIONS_MARKER\n")
	q.write("/reload\r")
	q.wait("Cancelling the run for reload.")
	q.wait("interrupted")
	q.wait("Reloaded")
	q.wait("original work")
	q.wait("RETRIED DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()

	mu.Lock()
	got := reqCount
	mu.Unlock()
	if got != 2 {
		t.Fatalf("request count = %d, want 2 (original + retry)", got)
	}
	first := <-bodies
	second := <-bodies
	firstText := requestText(t, first)
	secondText := requestText(t, second)
	if !strings.Contains(firstText, "original work") {
		t.Errorf("first request missing original input: %q", firstText)
	}
	if !strings.Contains(secondText, "original work") {
		t.Errorf("retry request missing repeated input: %q", secondText)
	}
	firstSys := requestSystem(t, first)
	secondSys := requestSystem(t, second)
	if !strings.Contains(firstSys, "INITIAL_INSTRUCTIONS_MARKER") {
		t.Errorf("first request system missing initial instructions")
	}
	if strings.Contains(firstSys, "UPDATED_INSTRUCTIONS_MARKER") {
		t.Errorf("first request system should not contain updated instructions")
	}
	if !strings.Contains(secondSys, "UPDATED_INSTRUCTIONS_MARKER") {
		t.Errorf("retry request system missing updated instructions")
	}
	assertReloadHistory(t, home, agentDir)
}

// requestText concatenates user message text from an Anthropic request body.
func requestText(t *testing.T, body map[string]any) string {
	t.Helper()
	var sb strings.Builder
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		obj, _ := m.(map[string]any)
		if obj["role"] != "user" {
			continue
		}
		switch c := obj["content"].(type) {
		case string:
			sb.WriteString(c)
			sb.WriteString("\n")
		case []any:
			for _, b := range c {
				blk, _ := b.(map[string]any)
				if blk["type"] == "text" {
					if s, _ := blk["text"].(string); s != "" {
						sb.WriteString(s)
						sb.WriteString("\n")
					}
				}
			}
		}
	}
	return sb.String()
}

// requestSystem extracts the system prompt from an Anthropic request body.
func requestSystem(t *testing.T, body map[string]any) string {
	t.Helper()
	switch s := body["system"].(type) {
	case string:
		return s
	case []any:
		var sb strings.Builder
		for _, b := range s {
			blk, _ := b.(map[string]any)
			if blk["type"] == "text" {
				if text, _ := blk["text"].(string); text != "" {
					sb.WriteString(text)
					sb.WriteString("\n")
				}
			}
		}
		return sb.String()
	default:
		return ""
	}
}

// assertReloadHistory checks the durable interrupted turn: original user,
// aborted partial, then the repeated original user message.
func assertReloadHistory(t *testing.T, home, agentDir string) {
	t.Helper()
	var files []string
	for _, dir := range []string{filepath.Join(home, "sessions"), filepath.Join(agentDir, "sessions")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(files) == 0 {
		t.Fatal("no session files found")
	}
	var combined strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		combined.Write(b)
		combined.WriteString("\n")
	}
	text := combined.String()
	first := strings.Index(text, "original work")
	partial := strings.Index(text, "RELOAD_PARTIAL")
	last := strings.LastIndex(text, "original work")
	if first < 0 || partial < 0 || last <= first {
		t.Fatalf("history must hold original, partial, then repeated input (first=%d partial=%d last=%d)", first, partial, last)
	}
	if !(first < partial && partial < last) {
		t.Fatalf("history order must be original, partial, repeated (first=%d partial=%d last=%d)", first, partial, last)
	}
}
