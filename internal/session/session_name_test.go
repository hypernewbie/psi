package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openNamedSessionFixture(t *testing.T, records ...string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "named.jsonl")
	lines := append([]string{`{"type":"session","version":3,"id":"named","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work"}`}, records...)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFile(path, "/work", false)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSessionNameUsesLatestEntryRegardlessOfBranch(t *testing.T) {
	store := openNamedSessionFixture(t,
		`{"type":"session_info","id":"root","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","name":"root name"}`,
		`{"type":"session_info","id":"branch-a","parentId":"root","timestamp":"2026-01-01T00:00:02.000Z","name":"branch A"}`,
		`{"type":"session_info","id":"branch-b","parentId":"root","timestamp":"2026-01-01T00:00:03.000Z","name":"  branch B  "}`,
	)
	check := func(want string) {
		t.Helper()
		if got := store.SessionName(); got != want {
			t.Fatalf("SessionName()=%q; want %q", got, want)
		}
	}
	check("branch B")
	if err := store.Branch("branch-a"); err != nil {
		t.Fatal(err)
	}
	check("branch B") // Pi reads the latest session_info record, not the active path.
	if err := store.Branch("root"); err != nil {
		t.Fatal(err)
	}
	check("branch B")
}

func TestSessionNameHonorsLatestClear(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear string
	}{
		{name: "whitespace", clear: `"  "`},
		{name: "null", clear: "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openNamedSessionFixture(t,
				`{"type":"session_info","id":"root","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","name":"root name"}`,
				`{"type":"session_info","id":"clear","parentId":"root","timestamp":"2026-01-01T00:00:02.000Z","name":`+tc.clear+`}`,
			)
			if got := store.SessionName(); got != "" {
				t.Fatalf("%s latest session name did not clear the footer: %q", tc.name, got)
			}
		})
	}
}
