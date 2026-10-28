//go:build unix

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

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
	q.write("/model\r")
	q.wait("Models (page 1/2")
	q.prompt()
	q.write("/gpt-5.6-luna\r")
	q.wait("1 matches")
	q.prompt()
	q.write("1\r")
	q.wait("openai-codex/gpt-5.6-luna thinking off (session-local)")
	q.prompt()
	q.write("/thinking minimal\r")
	q.wait("thinking minimal (effective effort low) (session-local)")
	q.prompt()
	q.write("/session\r")
	q.wait("requested model gpt-5.6-luna")
	q.wait("effective effort low")
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
	if proj.State.Provider != "openai-codex" || proj.State.ModelID != "gpt-5.6-luna" || proj.State.ThinkingLevel != "minimal" {
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
