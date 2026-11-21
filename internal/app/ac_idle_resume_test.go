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
	"sync/atomic"
	"testing"

	"github.com/hypernewbie/psi/internal/session"
)

func TestACCompactWithoutCutSuggestsBump(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
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
	if _, err := store.AppendMessage(agentMessageUser("unfinished task")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("/ac compact\r")
	q.wait("Nothing to compact yet.")
	q.wait("Use /bump to continue the unfinished task.")
	q.write("/quit\r")
	q.finish()
	if calls.Load() != 0 {
		t.Fatalf("no-work compaction made %d provider requests", calls.Load())
	}
}

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
			var bodies []string
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
				raw, _ := json.Marshal(body)
				mu.Lock()
				kinds = append(kinds, kind)
				bodies = append(bodies, string(raw))
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
			q.write("/ac compact FOCUS_IDLE\r")
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
			if checkpoints != 1 || len(bodies) == 0 || !strings.Contains(bodies[0], "FOCUS_IDLE") {
				t.Fatalf("checkpoints=%d kinds=%v bodies=%v; want checkpoint and summary focus", checkpoints, kinds, bodies)
			}
			if unfinished {
				if len(kinds) != 2 || kinds[0] != "summary" || kinds[1] != "resume" || resumes != 1 ||
					!strings.Contains(bodies[1], "CHECKPOINT_ONLY") || strings.Count(bodies[1], "Continue the current task from the compacted context") != 1 {
					t.Fatalf("unfinished kinds=%v resumes=%d bodies=%v; want summary then one continuation", kinds, resumes, bodies)
				}
			} else {
				if len(kinds) != 1 || kinds[0] != "summary" || resumes != 0 {
					t.Fatalf("completed kinds=%v resumes=%d; want summary only", kinds, resumes)
				}
			}
		})
	}
}
