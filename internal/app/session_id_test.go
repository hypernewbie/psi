package app

import (
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
	var _ = session.OpenOptions{}
}
