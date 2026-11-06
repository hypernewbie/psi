package app

import (
	"os"
	"path/filepath"
	"testing"

	"psi/internal/config"
	"psi/internal/session"
)

// Pi --session-id semantics: use the current directory's existing session
// with the ID, creating it otherwise. These checks cover the parts that fail
// silently - a flag that never reaches Options, a conflict that still
// launches, an invalid ID that creates a corrupt header, and an existing
// session that gets duplicated instead of reopened.
func TestParseSessionID(t *testing.T) {
	o, _, err := Parse([]string{"--session-id", "abc123"})
	if err != nil || o.SessionID != "abc123" {
		t.Fatalf("parsed session-id = %+v err=%v", o, err)
	}
	for _, args := range [][]string{
		{"--session-id", "a1", "--session", "b2"},
		{"--session-id", "a1", "--continue"},
		{"--session-id", "a1", "--resume"},
		{"--session-id", "-bad-"},
	} {
		if _, _, err := Parse(args); err == nil {
			t.Errorf("accepted conflicting/invalid session-id arguments: %v", args)
		}
	}
}

func TestOpenSessionIDReusesExactMatchOtherwiseCreates(t *testing.T) {
	cwd, agentDir, sessionDir := t.TempDir(), t.TempDir(), t.TempDir()
	ov := config.StartupOverrides{SessionDir: sessionDir}
	created, err := openSession(Options{SessionID: "my-session-1"}, cwd, agentDir, config.Settings{}, ov, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.Header().ID != "my-session-1" {
		t.Fatalf("created session ID = %s", created.Header().ID)
	}
	if _, err := created.AppendMessage(agentMessageUser("first run")); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSession(Options{SessionID: "my-session-1"}, cwd, agentDir, config.Settings{}, ov, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Header().ID != "my-session-1" || reopened.File() != created.File() {
		t.Fatalf("reopened wrong session: id=%s file=%s want %s", reopened.Header().ID, reopened.File(), created.File())
	}
	proj, err := reopened.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Messages) == 0 {
		t.Fatal("reopened session lost its transcript")
	}
	other, err := openSession(Options{SessionID: "my-session-2"}, cwd, agentDir, config.Settings{}, ov, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if other.Header().ID != "my-session-2" || other.File() == created.File() {
		t.Fatalf("second session-id collided: id=%s file=%s", other.Header().ID, other.File())
	}
	t.Run("LegacyFallback", func(t *testing.T) {
		cwd, agentDir, sessionDir := t.TempDir(), t.TempDir(), t.TempDir()
		ov := config.StartupOverrides{SessionDir: sessionDir}
		created, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: sessionDir, AgentDir: agentDir, Persist: true, ID: "legacy-session"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := created.AppendMessage(agentMessageUser("legacy seed")); err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(created.Dir(), "legacy-name.jsonl")
		if err := os.Rename(created.File(), legacy); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(created.Dir(), "legacy-session.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("canonical path should be absent, stat err = %v", err)
		}
		reopened, err := openSession(Options{SessionID: "legacy-session"}, cwd, agentDir, config.Settings{}, ov, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if reopened.File() != legacy {
			t.Fatalf("reopened file = %s, want %s", reopened.File(), legacy)
		}
		proj, err := reopened.Projection()
		if err != nil {
			t.Fatal(err)
		}
		if len(proj.Messages) == 0 {
			t.Fatal("legacy fallback lost its transcript")
		}
	})
	t.Run("OpenedHeaderMismatch", func(t *testing.T) {
		cwd, agentDir, sessionDir := t.TempDir(), t.TempDir(), t.TempDir()
		ov := config.StartupOverrides{SessionDir: sessionDir}
		created, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: sessionDir, AgentDir: agentDir, Persist: true, ID: "different-id"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := created.AppendMessage(agentMessageUser("seed")); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(created.File())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(created.File(), filepath.Join(created.Dir(), "requested-id.jsonl")); err != nil {
			t.Fatal(err)
		}
		if _, err := openSession(Options{SessionID: "requested-id"}, cwd, agentDir, config.Settings{}, ov, nil, nil); err == nil {
			t.Fatal("expected an error for a mismatched canonical candidate")
		}
		entries, err := os.ReadDir(created.Dir())
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected exactly one session file, found %d", len(entries))
		}
		after, err := os.ReadFile(filepath.Join(created.Dir(), "requested-id.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatal("mismatched candidate was modified")
		}
	})
}

func TestFindSessionIDDirectHitSkipsDiscovery(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	const id = "direct-hit-1"
	s, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: dir, Persist: true, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	got, err := findSessionID(dir, cwd, id, func(string, string) ([]session.SessionInfo, error) {
		t.Fatal("discovery must not run on a canonical hit")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, id+".jsonl"); got != want {
		t.Fatalf("resolved path = %s, want %s", got, want)
	}
}
