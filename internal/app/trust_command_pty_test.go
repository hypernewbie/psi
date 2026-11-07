//go:build unix

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActualTrustCommandSavesDecision(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	writeAppFile(t, filepath.Join(work, ".pi", "settings.json"), `{}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project")
	q.prompt()
	q.write("/trust\r")
	q.wait("Project trust")
	q.write("\r")
	q.wait("Saved trust decision: trusted")
	q.write("/quit\r")
	q.finish()

	if _, err := os.Stat(filepath.Join(agentDir, "trust.json")); err != nil {
		t.Fatal(err)
	}
	if trusted, ok := newProjectTrustStore(agentDir).get(work); !ok || !trusted {
		t.Fatalf("saved trust decision = %v, %v", trusted, ok)
	}
}
