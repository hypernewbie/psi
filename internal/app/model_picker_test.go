//go:build unix

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"psi/internal/session"
)

func TestActualOfflineModelPickerThinkingAndRestart(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "synthetic recalled prompt"}}); err != nil {
		t.Fatal(err)
	}
	prefix, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("draft survives picker")
	q.write("\x0c")
	q.wait("Models ·")
	q.write("\x1b")
	q.wait("draft survives picker")
	q.prompt()
	if !strings.Contains(q.screen.Text(), "draft survives picker") {
		t.Fatalf("Ctrl+L/Escape lost editor draft: %q", q.screen.Text())
	}
	q.write("\x03")
	q.prompt()
	q.write("/model\r")
	q.wait("Models ·")
	q.prompt()
	if screen := q.screen.Text(); !strings.Contains(screen, "→ ✓ m [anthropic]") || strings.Count(screen, "context ") != 1 {
		t.Fatalf("actual model picker is not using compact Pi rows and selected-only detail: %q", screen)
	}
	q.write("\x1b[6~\x1b[5~") // bounded PageDown/PageUp navigation
	q.prompt()
	q.write("lunaa") // Not a substring: no fuzzy fallback or accidental selection.
	q.wait("0 matches")
	q.prompt()
	q.write("\x15gpt-5.6-luna")
	q.wait("1 matches")
	q.prompt()
	q.write("\r")
	q.wait("openai-codex/gpt-5.6-luna thinking off (session-local)")
	q.prompt()
	q.write("/thinking minimal\r")
	q.wait("thinking minimal (effective effort low) (session-local)")
	q.prompt()
	q.write("/thinking\r")
	q.wait("Thinking levels ·")
	q.prompt()
	q.write("\x1b[200~high\x1b[201~") // picker paste is a literal query, never a submission
	q.wait("2 matches")
	q.prompt()
	q.write("\r")
	q.wait("thinking high (effective effort high) (session-local)")
	q.prompt()
	q.write("/thinking minimal\r")
	q.wait("thinking minimal (effective effort low) (session-local)")
	q.prompt()
	q.write("\x1b[Z")
	q.wait("thinking low (effective effort low) (session-local)")
	q.prompt()
	q.write("/session\r")
	q.wait("requested model gpt-5.6-luna")
	q.wait("effective effort low")
	q.prompt()
	q.write("/resume\r")
	q.wait("Sessions ·")
	q.prompt()
	if screen := q.screen.Text(); !strings.Contains(screen, "› ✓ synthetic recalled prompt") {
		t.Fatalf("actual session picker did not prioritize the prompt preview: %q", screen)
	}
	q.write("\x03") // Ctrl+C cancels the picker and restores its submitted draft.
	q.wait("/resume")
	q.prompt()
	q.write("\x03")
	q.prompt()
	// Selector answers and slash commands must not replace durable prompt recall.
	q.write("\x10")
	q.wait("synthetic recalled prompt")
	q.write("\x03/quit\r")
	q.finish()
	after, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, prefix) {
		t.Fatal("model commands changed original session prefix")
	}
	reopened, err := session.OpenFile(store.File(), work, false)
	if err != nil {
		t.Fatal(err)
	}
	proj, err := reopened.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if proj.State.Provider != "openai-codex" || proj.State.ModelID != "gpt-5.6-luna" || proj.State.ThinkingLevel != "low" {
		t.Fatalf("session-local choices not durable: %+v", proj.State)
	}
	q = startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("/session\r")
	q.wait("requested model gpt-5.6-luna")
	q.wait("effective effort low")
	q.prompt()
	q.write("/quit\r")
	q.finish()
}

// The picker replaces the editor region, like Pi's showSelector. Navigation
// and filtering must not append another result page to native scrollback, and
// a cancelled picker must restore the submitted draft.
func TestActualPickerReplacesTheEditorWithoutFloodingScrollback(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "synthetic recalled prompt"}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("/model\r")
	q.wait("Models ·")
	q.prompt()
	screen := q.screen.Text()
	if strings.Contains(screen, "m • off") {
		t.Fatalf("footer shows a thinking level for a model without reasoning support: %q", screen)
	}
	if !strings.Contains(screen, "(anthropic) m") {
		t.Fatalf("footer lost the selected model identity: %q", screen)
	}
	if !hasQueryRow(screen) {
		t.Fatalf("picker did not own a search query row: %q", screen)
	}
	if borders := countSolidBorderRows(screen); borders != 2 {
		t.Fatalf("picker rendered an extra editor border pair: %d border rows in %q", borders, screen)
	}
	before := countRowsContaining(q.screen.Scrollback, "→ ")
	q.write("gpt")
	q.wait("matches")
	q.prompt()
	q.write("\x1b[B") // select the next model
	q.prompt()
	q.write("\x1b[A") // return to the previous model
	q.prompt()
	if after := countRowsContaining(q.screen.Scrollback, "→ "); after > before {
		t.Fatalf("picker navigation appended result rows to the scrollback: %d -> %d", before, after)
	}
	q.write("\x03") // cancel the picker and restore its submitted draft
	q.wait("/model")
	q.prompt()
	if !strings.Contains(q.screen.Text(), "/model") {
		t.Fatalf("cancelled picker did not restore its submitted draft: %q", q.screen.Text())
	}
	q.write("\x03") // clear the restored draft
	q.prompt()
	q.write("/quit\r")
	q.finish()
}

func countSolidBorderRows(screen string) int {
	count := 0
	for _, row := range strings.Split(screen, "\n") {
		trimmed := strings.TrimSpace(row)
		if trimmed != "" && strings.Trim(trimmed, "─") == "" {
			count++
		}
	}
	return count
}

func countRowsContaining(rows []string, want string) int {
	count := 0
	for _, row := range rows {
		if strings.Contains(row, want) {
			count++
		}
	}
	return count
}

func hasQueryRow(screen string) bool {
	for _, row := range strings.Split(screen, "\n") {
		if strings.HasPrefix(row, ">") {
			return true
		}
	}
	return false
}

func TestActualShortCompactIsOfflineNoOpAndPreservesSessionBytes(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"keepRecentTokens":20000}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:1/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":100000}]}}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("short synthetic request")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("/compact\r")
	q.wait("Nothing to compact yet. Recent conversation is already within")
	q.wait("budget.")
	q.prompt()
	if strings.Contains(q.raw.String(), "Compacting context") {
		t.Fatalf("short-session no-op showed a compaction indicator: %q", q.raw.String())
	}
	q.write("/quit\r")
	q.finish()
	after, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("short-session no-op changed persisted session bytes")
	}
}

func TestActualEmptyCompactIsOfflineNoOpWithoutIndicator(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"keepRecentTokens":20000}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:1/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":100000}]}}}`)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session")
	q.prompt()
	q.write("/compact\r")
	q.wait("Nothing to compact yet. The session has no conversation history.")
	q.prompt()
	if strings.Contains(q.raw.String(), "Compacting context") {
		t.Fatalf("no-work command showed a compaction indicator: %q", q.raw.String())
	}
	q.write("/quit\r")
	q.finish()
}

// A manual compaction shows Pi's compaction indicator in the editor border and
// then settles with a committed summary. The provider request must be a real
// summary request, not a credential refresh.
func TestActualCompactionShowsPiIndicatorAndSettles(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	requests := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"SUMMARY_COMMITTED\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false,"keepRecentTokens":20000}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-compaction-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("old synthetic question")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		text := strings.Repeat(string(rune('a'+i)), 80_000)
		if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: text}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	initialPath, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	lastAssistantID := initialPath[len(initialPath)-1].ID()
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("/compact\r")
	q.wait("Compacting context")
	screen := q.screen.Text()
	if !strings.Contains(screen, "Compacting context") {
		t.Fatalf("compaction indicator is missing from the editor border: %q", screen)
	}
	if !strings.Contains(screen, "Esc/Ctrl+C cancel") {
		t.Fatalf("compaction indicator lost the cancellation hint: %q", screen)
	}
	select {
	case body := <-requests:
		system, _ := body["system"].(string)
		if !strings.Contains(system, "context summarization assistant") {
			t.Fatalf("compaction did not send a summary request: %v", body)
		}
		serialized, _ := json.Marshal(body)
		prompt := string(serialized)
		if !strings.Contains(prompt, strings.Repeat("a", 100)) || strings.Contains(prompt, strings.Repeat("b", 100)) {
			t.Fatalf("ordinary-budget split summary included the wrong span: request length=%d", len(prompt))
		}
	case <-time.After(8 * time.Second):
		t.Fatal("compaction did not reach the provider")
	}
	close(release)
	released = true
	q.wait("Context compacted")
	q.prompt()
	if !strings.Contains(q.screen.Text(), "[compaction]") || !strings.Contains(q.screen.Text(), "Context compacted") {
		t.Fatalf("compaction did not settle: %q", q.screen.Text())
	}
	after, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte("SUMMARY_COMMITTED")) {
		t.Fatalf("compaction summary was not committed: %s", after)
	}
	saved, err := session.OpenFile(store.File(), work, true)
	if err != nil {
		t.Fatal(err)
	}
	savedPath, err := saved.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	last := savedPath[len(savedPath)-1].Compaction
	if last == nil || last.FirstKeptEntryID != lastAssistantID {
		t.Fatalf("actual app chose wrong ordinary-budget continuation: %+v, want %s", last, lastAssistantID)
	}
	q.write("/quit\r")
	q.finish()

	restarted := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	restarted.prompt()
	restartedScreen := restarted.screen.Text()
	if !strings.Contains(restartedScreen, "earlier messages summarized") || strings.Contains(restartedScreen, "SUMMARY_COMMITTED") {
		t.Fatalf("restart replay exposed the full summary or lost its compact marker: %q", restartedScreen)
	}
	restarted.write("/history all\r")
	restarted.wait("SUMMARY_COMMITTED")
	restarted.prompt()
	restarted.write("/quit\r")
	restarted.finish()
}

// Ctrl+D stays with the focused picker. It must never end the session while
// a picker owns the input.
func TestActualCtrlDInPickerDoesNotExitTheSession(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	q := startReviewPTY(t, home, work, agentDir, "--deny-project")
	q.prompt()
	q.write("/model\r")
	q.wait("Models ·")
	q.prompt()
	q.write("\x04")
	q.prompt()
	if !strings.Contains(q.screen.Text(), "Models ·") {
		t.Fatalf("Ctrl+D closed the picker instead of staying in the query: %q", q.screen.Text())
	}
	q.write("\x03")
	q.prompt()
	q.write("\x03")
	q.prompt()
	q.write("/quit\r")
	q.finish()
}
