//go:build unix

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/session"
)

// The startup banner shows psi's Ψ mark with the version on its baseline,
// unless quietStartup is set. Like Pi's header, it is UI chrome: it never
// enters the session file.
func TestActualAppStartupBanner(t *testing.T) {
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
	screen := q.screen.Text()
	if n := strings.Count(screen, "▀█▀") + strings.Count(screen, "█ ▄ █"); n < 2 {
		t.Fatalf("startup banner missing the Psi mark: %q", screen)
	}
	if !strings.Contains(screen, " v") && !strings.Contains(screen, "psi v") {
		t.Fatalf("startup banner missing the version: %q", screen)
	}
	q.write("/quit\r")
	q.finish()

	quiet := filepath.Join(home, "agent-quiet")
	writeAppFile(t, filepath.Join(quiet, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","quietStartup":true,"compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(quiet, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(quiet, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	q2 := startReviewPTY(t, home, work, quiet, "--deny-project", "--session", store.File())
	q2.prompt()
	if screen2 := q2.screen.Text(); strings.Contains(screen2, "▀█▀") || strings.Contains(screen2, "█ ▄ █") || strings.Contains(screen2, "psi v") {
		t.Fatalf("quietStartup still printed the banner: %q", screen2)
	}
	q2.write("/quit\r")
	q2.finish()

	raw, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "▀█▀") {
		t.Fatal("startup banner leaked into the session file")
	}
}
