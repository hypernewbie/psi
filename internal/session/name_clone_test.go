package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pi's /name appends a session_info entry; the latest one wins and an empty
// name clears the display name.
func TestAppendSessionInfoLatestWinsAndClears(t *testing.T) {
	dir := t.TempDir()
	store, err := New(OpenOptions{Cwd: dir, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentUserForTest("first")); err != nil {
		t.Fatal(err)
	}
	if got := store.SessionName(); got != "" {
		t.Fatalf("fresh session has a name: %q", got)
	}
	if _, err := store.AppendSessionInfo("Refactor auth"); err != nil {
		t.Fatal(err)
	}
	if got := store.SessionName(); got != "Refactor auth" {
		t.Fatalf("name = %q", got)
	}
	if _, err := store.AppendSessionInfo("second name"); err != nil {
		t.Fatal(err)
	}
	if got := store.SessionName(); got != "second name" {
		t.Fatalf("latest name must win, got %q", got)
	}
	raw, err := readFileForTest(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(raw, `"type":"session_info"`) != 2 {
		t.Fatalf("session_info entries not persisted: %s", filepath.Base(store.File()))
	}
	reopened, err := OpenFile(store.File(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.SessionName(); got != "second name" {
		t.Fatalf("reopened name = %q", got)
	}
	if _, err := reopened.AppendSessionInfo(""); err != nil {
		t.Fatal(err)
	}
	if got := reopened.SessionName(); got != "" {
		t.Fatalf("empty name must clear, got %q", got)
	}
}

func agentUserForTest(text string) AgentMessage {
	return AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: text}}
}

func readFileForTest(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
