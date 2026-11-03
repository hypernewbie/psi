//go:build unix

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/session"
)

// /version renders build identity in the transcript. It is display-only: the
// lines never enter the session file, and the command needs no provider.
func TestActualAppVersionCommand(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("/version\r")
	q.wait("build go")
	screen := q.screen.Text()
	for _, want := range []string{"psi review", "commit ", "build go"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("/version output missing %q: %q", want, screen)
		}
	}
	q.write("/quit\r")
	q.finish()

	raw, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "psi review") || strings.Contains(string(raw), "build go") {
		t.Fatal("/version output leaked into the session file")
	}
}
