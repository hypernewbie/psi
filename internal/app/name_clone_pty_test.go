//go:build unix

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"psi/internal/session"
)

func TestActualAppNameAndCloneCommands(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:9")
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed message")); err != nil {
		t.Fatal(err)
	}
	originalID, originalPath := store.Header().ID, store.File()
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", originalPath)
	q.prompt()

	q.write("/name Refactor auth\r")
	q.wait("Session name set: Refactor auth")
	q.prompt()
	q.write("/name\r")
	q.wait("Session name: Refactor auth")
	q.prompt()

	originalBeforeClone, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	q.write("/clone\r")
	q.wait("Cloned to new session")
	q.prompt()

	infos, err := session.Discover(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	var clone *session.SessionInfo
	for i := range infos {
		if infos[i].ID != originalID {
			clone = &infos[i]
		}
	}
	if clone == nil {
		t.Fatalf("clone session not discovered: %+v", infos)
	}
	if clone.Name != "Refactor auth" {
		t.Fatalf("clone name = %q, want Refactor auth", clone.Name)
	}
	cloneBytes, err := os.ReadFile(clone.Path)
	if err != nil {
		t.Fatal(err)
	}
	originalAfterClone, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(originalBeforeClone, originalAfterClone) {
		t.Fatal("/clone changed the source session")
	}
	originalLines := bytes.Split(originalBeforeClone, []byte("\n"))
	cloneLines := bytes.Split(cloneBytes, []byte("\n"))
	if len(originalLines) != len(cloneLines) || !bytes.Equal(bytes.Join(originalLines[1:], []byte("\n")), bytes.Join(cloneLines[1:], []byte("\n"))) {
		t.Fatal("clone did not preserve session entries byte-for-byte")
	}
	if bytes.Equal(originalLines[0], cloneLines[0]) {
		t.Fatal("clone reused the source session header")
	}
	q.write("/name\r")
	q.wait("Session name: Refactor auth")
	q.write("/quit\r")
	q.finish()
}
