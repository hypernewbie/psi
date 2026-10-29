package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionNameUsesActiveBranchAndHonorsClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "named.jsonl")
	lines := []string{
		`{"type":"session","version":3,"id":"named","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work"}`,
		`{"type":"session_info","id":"root","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","name":"root name"}`,
		`{"type":"session_info","id":"branch-a","parentId":"root","timestamp":"2026-01-01T00:00:02.000Z","name":"branch A"}`,
		`{"type":"session_info","id":"branch-b","parentId":"root","timestamp":"2026-01-01T00:00:03.000Z","name":"branch B"}`,
		`{"type":"session_info","id":"clear","parentId":"branch-a","timestamp":"2026-01-01T00:00:04.000Z","name":null}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFile(path, "/work", false)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		got, err := store.SessionName()
		if err != nil || got != want {
			t.Fatalf("SessionName()=%q, %v; want %q", got, err, want)
		}
	}
	check("") // The last active record explicitly clears its name.
	if err := store.Branch("branch-b"); err != nil {
		t.Fatal(err)
	}
	check("branch B")
	if err := store.Branch("branch-a"); err != nil {
		t.Fatal(err)
	}
	check("branch A")
	if err := store.Branch("root"); err != nil {
		t.Fatal(err)
	}
	check("root name")
}
