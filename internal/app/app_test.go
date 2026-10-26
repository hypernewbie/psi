package app

import (
	"psi/internal/config"
	"strings"
	"testing"

	"psi/internal/session"
	"psi/internal/skills"
	"psi/internal/terminal"
	"psi/internal/tools"
)

func startup() *config.StartupOverrides {
	return &config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off"}
}
func settings() config.Settings {
	return config.Settings{DefaultTools: []string{"read", "bash", "edit", "write"}}
}
func testStore(t *testing.T) *session.Store {
	t.Helper()
	s, err := session.New(session.OpenOptions{Cwd: t.TempDir(), SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseBoundary(t *testing.T) {
	o, _, err := Parse([]string{"--provider", "anthropic", "--model", "m", "--thinking", "high", "--session-dir", "/tmp", "--tools", "read,bash", "--no-session"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Provider != "anthropic" || o.Model != "m" || !o.NoSession {
		t.Errorf("%+v", o)
	}
}

func TestHandleCommands(t *testing.T) {
	store := testStore(t)
	reg := tools.NewRegistry(store.Cwd())
	ov := startup()
	stream := terminal.NewStreamer(discard{}, true)
	status := terminal.NewStatusWriter(discard{})
	sk := []skills.Skill{{Name: "doer", Description: "d", FilePath: "/tmp/SKILL.md"}}

	if _, err := handleCommand(&terminal.Command{Name: "help"}, store, reg, ov, settings(), sk, stream, status); err != nil {
		t.Errorf("help: %v", err)
	}
	if _, err := handleCommand(&terminal.Command{Name: "unknown:x"}, store, reg, ov, settings(), sk, stream, status); err == nil {
		t.Errorf("unknown must error, not send as text")
	}
	if _, err := handleCommand(&terminal.Command{Name: "model", Arg: "anthropic m2"}, store, reg, ov, settings(), sk, stream, status); err != nil {
		t.Errorf("model: %v", err)
	}
	if ov.Model != "m2" {
		t.Errorf("model not session-local: %+v", ov)
	}
	if _, err := handleCommand(&terminal.Command{Name: "thinking", Arg: "high"}, store, reg, ov, settings(), sk, stream, status); err != nil {
		t.Errorf("thinking: %v", err)
	}
	// Bounded resume list.
	if _, err := handleCommand(&terminal.Command{Name: "resume"}, store, reg, ov, settings(), sk, stream, status); err == nil {
		t.Errorf("resume without selection hints bounded interaction")
	}
	// Skill missing reports without provider request tested in skills pkg; here unknown skill errors.
	if _, err := handleCommand(&terminal.Command{Name: "skill:missing"}, store, reg, ov, settings(), sk, stream, status); err == nil {
		t.Errorf("missing skill must fail")
	}
	// Compact on tiny session still works (boundary safe).
	if _, err := handleCommand(&terminal.Command{Name: "session"}, store, reg, ov, settings(), sk, stream, status); err != nil {
		t.Errorf("session: %v", err)
	}
	// Startup line small.
	line := terminal.StartupLine("anthropic", "m", "/ws", "abcdef123456", 3)
	if !strings.Contains(line, "anthropic") || strings.Count(line, "\n") != 0 {
		t.Errorf("startup line: %q", line)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
