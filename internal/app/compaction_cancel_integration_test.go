//go:build unix

package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"psi/internal/session"
)

func TestActualCompactionCancellationKeepsSessionWithoutCheckpoint(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	requestReceived := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived <- struct{}{}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
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
	before, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("/compact\r")
	q.wait("Compacting context")
	select {
	case <-requestReceived:
	case <-time.After(8 * time.Second):
		t.Fatal("compaction did not reach the loopback provider")
	}
	q.write("\x03")
	q.wait("Request cancelled.")
	q.prompt()
	after, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("cancelled compaction changed session bytes (%d -> %d)", len(before), len(after))
	}
	for _, entry := range store.Entries() {
		if entry.Kind == "compaction" {
			t.Fatal("cancelled app compaction committed a checkpoint")
		}
	}
	q.write("/quit\r")
	q.finish()
}
