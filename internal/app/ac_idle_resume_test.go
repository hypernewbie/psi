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

// TestActualAppACIdleCompactResumesInterruptedWork pins the idle resume fix:
// a reopened aborted tail is unfinished (session.CanBump), so /ac compact
// must checkpoint and then continue once on the active model instead of
// going idle. A completed tip must stay idle after its checkpoint.
func TestActualAppACIdleCompactResumesInterruptedWork(t *testing.T) {
	for _, unfinished := range []bool{true, false} {
		name := "completed stays idle"
		if unfinished {
			name = "aborted resumes once"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var kinds []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Errorf("decode: %v", err)
					return
				}
				encoded, _ := json.Marshal(body["system"])
				kind, reply := "resume", "RESUME_REPLY"
				if strings.Contains(string(encoded), "context summarization assistant") {
					kind, reply = "summary", "CHECKPOINT_ONLY"
				}
				mu.Lock()
				kinds = append(kinds, kind)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, compactTextTurn(reply))
			}))
			defer server.Close()
			home, work := t.TempDir(), t.TempDir()
			agentDir := filepath.Join(home, "agent")
			writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false,"keepRecentTokens":100},"outputPad":0}`)
			writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":1024,"contextWindow":100000}]}}}`)
			writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-ac-key"}}`)
			store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
			if err != nil {
				t.Fatal(err)
			}
			append := func(msg session.AgentMessage) {
				t.Helper()
				if _, err := store.AppendMessage(msg); err != nil {
					t.Fatal(err)
				}
			}
			appendAssistant := func(text, stop string) {
				append(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: stop, Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: text}}}}})
			}
			append(agentMessageUser("older task"))
			appendAssistant(strings.Repeat("older answer ", 800), "stop")
			append(agentMessageUser("interrupted task"))
			if unfinished {
				appendAssistant("partial answer", "aborted")
			} else {
				appendAssistant("completed answer", "stop")
			}
			q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
			q.prompt()
			q.write("/ac compact\r")
			if unfinished {
				q.wait("RESUME_REPLY")
			} else {
				q.wait("Compacted from")
			}
			q.prompt()
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
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if checkpoints != 1 {
				t.Fatalf("checkpoints=%d kinds=%v; want 1", checkpoints, kinds)
			}
			if unfinished {
				if len(kinds) != 2 || kinds[0] != "summary" || kinds[1] != "resume" {
					t.Fatalf("unfinished kinds=%v; want summary then resume", kinds)
				}
			} else {
				if len(kinds) != 1 || kinds[0] != "summary" {
					t.Fatalf("completed kinds=%v; want summary only", kinds)
				}
			}
		})
	}
}
