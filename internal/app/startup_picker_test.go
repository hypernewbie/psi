//go:build unix

package app

import (
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"

	"psi/internal/session"
)

func TestInlineProjectTrustPickerDefaultsToDeny(t *testing.T) {
	for _, allow := range []bool{false, true} {
		name := "deny"
		if allow {
			name = "allow"
		}
		t.Run(name, func(t *testing.T) {
			home, work := t.TempDir(), t.TempDir()
			agentDir := filepath.Join(home, "agent")
			reviewSettings(t, agentDir, "http://127.0.0.1:1")
			writeAppFile(t, filepath.Join(work, ".pi", "SYSTEM.md"), "PROJECT_RESOURCE_CANARY")
			writeAppFile(t, filepath.Join(work, ".pi", "skills", "project", "SKILL.md"), "---\nname: project\ndescription: synthetic\n---\nPROJECT_SKILL_BODY")
			store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "synthetic trust fixture"}}); err != nil {
				t.Fatal(err)
			}
			q := startReviewPTY(t, home, work, agentDir, "--session", store.File())
			q.wait("Project trust")
			if allow {
				q.write("\x1b[A") // select the explicit one-run trust choice
			}
			q.write("\r")
			q.wait("esc interrupt")
			q.prompt()
			if allow {
				q.write("/skill:pro")
				q.prompt()
				if !strings.Contains(q.screen.Text(), "/skill:project") {
					t.Fatalf("trusted project skill was not loaded: %q", q.screen.Text())
				}
				q.write("\x03")
				q.prompt()
			} else {
				q.write("/skill:project\r")
				q.wait("skill \"project\" not found")
				q.prompt()
			}
			if strings.Contains(q.raw.String(), "PROJECT_RESOURCE_CANARY") || strings.Contains(q.raw.String(), "PROJECT_SKILL_BODY") {
				t.Fatal("project resources escaped the consent boundary")
			}
			q.write("/quit\r")
			q.finish()
		})
	}
}

func TestStartupConfirmationPreservesPendingEditorInput(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	writeAppFile(t, filepath.Join(work, ".pi", "SYSTEM.md"), "PROJECT_RESOURCE_CANARY")
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "pending input fixture"}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--session", store.File())
	q.wait("Project trust")
	q.write("\r/quit\r") // one terminal write: consent first, next prompt input second
	q.wait("esc interrupt")
	q.finish()
	if strings.Contains(q.raw.String(), "PROJECT_RESOURCE_CANARY") {
		t.Fatal("untrusted resource was read or displayed")
	}
}

func TestStartupResumeUsesSearchableInlinePicker(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: session.DefaultSessionDir(work, agentDir), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "resume searchable marker"}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--resume", "--deny-project")
	q.wait("Sessions ·")
	q.prompt()
	q.write("resume searchable marker")
	q.prompt()
	q.write("\r")
	q.wait("resume searchable marker")
	q.prompt()
	q.write("/quit\r")
	q.finish()
}

func TestWorkspaceMismatchConfirmationDefaultsToCancelAndRestoresTerminal(t *testing.T) {
	home, work, other := t.TempDir(), t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	store, err := session.New(session.OpenOptions{Cwd: other, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "workspace mismatch fixture"}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--session", store.File(), "--deny-project")
	q.wait("Workspace confirmation")
	q.write("\r") // the default is Cancel
	q.finish()
	if err := store.TryLock(); err != nil {
		t.Fatalf("startup cancellation leaked the session lock: %v", err)
	}
	store.Unlock()
}

func TestBootstrapConfigurationErrorRestoresTerminal(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	q := startReviewPTY(t, home, work, agentDir, "--provider", "not-a-provider")
	q.wait("unsupported provider")
	done := make(chan error, 1)
	go func() { done <- q.cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("invalid provider unexpectedly succeeded")
		}
	case <-time.After(8 * time.Second):
		q.cmd.Process.Kill()
		<-done
		t.Fatal("configuration error did not exit")
	}
	if goruntime.GOOS != "darwin" {
		state, err := term.GetState(int(q.tty.Fd()))
		if err != nil || !sameTermState(state, q.original) {
			t.Fatalf("configuration error left terminal raw: %v", err)
		}
	}
}
