//go:build unix

package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hypernewbie/psi/internal/session"
)

func TestACSummaryFailureAndCancellationStatus(t *testing.T) {
	for _, cancelSummary := range []bool{false, true} {
		name := "failure pauses until enabled"
		if cancelSummary {
			name = "cancellation does not pause"
		}
		t.Run(name, func(t *testing.T) {
			home, work := t.TempDir(), t.TempDir()
			agentDir := filepath.Join(home, "agent")
			arrived := make(chan map[string]any, 1)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				arrived <- map[string]any{}
				if cancelSummary {
					w.Header().Set("Content-Type", "text/event-stream")
					w.(http.Flusher).Flush()
					<-req.Context().Done()
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"synthetic failure"}}`)
			}))
			defer server.Close()
			writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"keepRecentTokens":50},"outputPad":0}`)
			writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":100000}]}}}`)
			writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-ac-key"}}`)
			store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(agentMessageUser("old task")); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
				Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop",
				Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("old answer ", 500)}}},
			}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(agentMessageUser("unfinished task")); err != nil {
				t.Fatal(err)
			}
			q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
			q.prompt()
			q.write("/ac compact\r")
			compactWaitBody(t, arrived)
			if cancelSummary {
				q.write("\x03")
				q.wait("Request cancelled.")
			} else {
				q.wait("summary failed; original context is unchanged")
			}
			q.write("/ac status\r")
			if cancelSummary {
				q.wait("pi-ac: automatic compaction enabled")
			} else {
				q.wait("pi-ac: automatic compaction paused after an error")
				q.wait("Pi auto-compaction: enabled")
				q.write("/ac enable\r")
				q.wait("Automatic compaction enabled.")
				q.write("/ac status\r")
				q.wait("pi-ac: automatic compaction enabled")
			}
			q.write("/quit\r")
			q.finish()
			_, entries, _, err := session.ReadFileEntries(store.File())
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Compaction != nil {
					t.Fatal("failed or cancelled summary wrote a checkpoint")
				}
				if entry.Message != nil && entry.Message.Message.User != nil && strings.Contains(fmt.Sprint(entry.Message.Message.User.Content), "Continue the current task from the compacted context") {
					t.Fatal("failed or cancelled summary injected a continuation")
				}
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("provider requests = %d, want one summary only", got)
			}
		})
	}
}
