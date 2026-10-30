//go:build unix

package app

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/term"

	"psi/internal/session"
	"psi/internal/terminal"
	"psi/internal/testutil/leakcheck"
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

func TestPickerSuspendResumePreservesQueryAndFocus(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	sessionDir := session.DefaultSessionDir(work, agentDir)
	var current *session.Store
	for _, prompt := range []string{"alpha session", "beta session"} {
		store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: sessionDir, AgentDir: agentDir, Persist: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: prompt}}); err != nil {
			t.Fatal(err)
		}
		current = store
	}
	q := startReviewPTY(t, home, work, agentDir, "--session", current.File())
	q.prompt()
	q.write("/resume\r")
	q.wait("Sessions · 2 matches")
	q.prompt()
	q.write("not-a-session")
	q.prompt()
	q.write("\x1a")
	q.wait("\x1b[?2004l")
	deadline := time.Now().Add(3 * time.Second)
	stopped := false
	for time.Now().Before(deadline) {
		out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(q.cmd.Process.Pid)).Output()
		if err == nil && len(out) > 0 && out[0] == 'T' {
			stopped = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !stopped {
		t.Fatal("picker suspension did not stop the process")
	}
	state, err := term.GetState(int(q.tty.Fd()))
	if err != nil || !sameTermState(state, q.original) {
		t.Fatalf("picker suspension did not restore terminal: %v", err)
	}
	if err := q.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	q.wait("Sessions · 0 matches")
	q.prompt()
	if !strings.Contains(q.screen.Text(), "not-a-session") {
		t.Fatalf("picker query was lost on resume: %q", q.screen.Text())
	}
	q.write("\x1b")
	q.wait("/resume")
	q.prompt()
	q.write("\x03")
	q.prompt()
	q.write("/quit\r")
	q.finish()
}

func TestShutdownDuringPickerRestoresTerminal(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-session")
	q.prompt()
	q.write("/model\r")
	q.wait("Models ·")
	if err := q.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	q.finish()
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

// A4-11: the session picker selects A → B → A across workspace sessions,
// stays bounded, and restores the submitted draft when it is cancelled.
func TestActualSessionPickerSelectsAndCancelsInWorkspace(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	dir := filepath.Join(home, "sessions")
	var files []string
	for _, prompt := range []string{"alpha synthetic prompt", "beta synthetic prompt", "gamma synthetic prompt"} {
		store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: dir, AgentDir: agentDir, Persist: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: prompt}}); err != nil {
			t.Fatal(err)
		}
		files = append(files, store.File())
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", files[0])
	q.prompt()
	q.write("/resume\r")
	q.wait("Sessions ·")
	q.prompt()
	screen := q.screen.Text()
	if !strings.Contains(screen, "alpha synthetic prompt") || !strings.Contains(screen, "beta synthetic prompt") {
		t.Fatalf("session picker did not list the workspace sessions: %q", screen)
	}
	q.write("\x1b[B") // A → B
	q.prompt()
	q.write("\x1b[A") // B → A
	q.prompt()
	q.write("\x03") // cancel restores the submitted draft
	q.wait("/resume")
	q.prompt()
	if !strings.Contains(q.screen.Text(), "/resume") {
		t.Fatalf("cancelled session picker did not restore its draft: %q", q.screen.Text())
	}
	q.write("\x03")
	q.prompt()
	q.write("/quit\r")
	q.finish()
}

func testPickerConsole(t *testing.T) (*terminal.Console, *os.File, *terminalLifecycle, func()) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	console := terminal.NewConsole(inR, io.Discard)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	lifecycle := &terminalLifecycle{console: console, signals: make(chan os.Signal, 10)}
	cleanup := func() {
		console.Close()
		inR.Close()
		inW.Close()
	}
	return console, inW, lifecycle, cleanup
}

func TestStartupPickerBootstrapSignal(t *testing.T) {
	t.Cleanup(func() {
		leakcheck.Verify(t)
	})

	t.Run("resize signal triggers redraw and remains active", func(t *testing.T) {
		redrawn := false
		done, err := handleBootstrapSignal(nil, syscall.SIGWINCH, func() { redrawn = true })
		if done || err != nil || !redrawn {
			t.Errorf("SIGWINCH: done=%v err=%v redrawn=%v", done, err, redrawn)
		}
	})

	t.Run("resume signal triggers redraw and remains active", func(t *testing.T) {
		redrawn := false
		done, err := handleBootstrapSignal(nil, syscall.SIGCONT, func() { redrawn = true })
		if done || err != nil || !redrawn {
			t.Errorf("SIGCONT: done=%v err=%v redrawn=%v", done, err, redrawn)
		}
	})

	t.Run("interrupt signal terminates without error", func(t *testing.T) {
		redrawn := false
		done, err := handleBootstrapSignal(nil, os.Interrupt, func() { redrawn = true })
		if !done || err != nil || redrawn {
			t.Errorf("SIGINT: done=%v err=%v redrawn=%v", done, err, redrawn)
		}
	})

	t.Run("shutdown signals terminate with error", func(t *testing.T) {
		for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP} {
			done, err := handleBootstrapSignal(nil, sig, func() {})
			if !done || err == nil || !strings.Contains(err.Error(), "startup interrupted by") {
				t.Errorf("%s: done=%v err=%v", sig, done, err)
			}
		}
	})

	t.Run("suspend signal with unavailable lifecycle returns error", func(t *testing.T) {
		done, err := handleBootstrapSignal(nil, syscall.SIGTSTP, func() {})
		if !done || err == nil || !strings.Contains(err.Error(), "terminal lifecycle unavailable") {
			t.Errorf("SIGTSTP without lifecycle: done=%v err=%v", done, err)
		}
	})

	t.Run("unhandled signal is ignored", func(t *testing.T) {
		redrawn := false
		done, err := handleBootstrapSignal(nil, syscall.SIGURG, func() { redrawn = true })
		if done || err != nil || redrawn {
			t.Errorf("SIGURG: done=%v err=%v redrawn=%v", done, err, redrawn)
		}
	})
}

func TestStartupPickerSessionNavigationAndPaging(t *testing.T) {
	t.Cleanup(func() {
		leakcheck.Verify(t)
	})

	makeSessions := func(n int) []session.SessionInfo {
		list := make([]session.SessionInfo, n)
		for i := 0; i < n; i++ {
			list[i] = session.SessionInfo{ID: fmt.Sprintf("session-%02d", i)}
		}
		return list
	}

	t.Run("selection navigation down and up", func(t *testing.T) {
		console, inW, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		selector := NewSelector(makeSessions(10), "")
		// Down twice, Up once, Enter -> should select index 1 (session-01)
		if _, err := inW.Write([]byte("\x1b[B\x1b[B\x1b[A\r")); err != nil {
			t.Fatal(err)
		}

		selected, cancelled, err := runSessionPicker(console, lifecycle, selector)
		if err != nil || cancelled || selected == nil {
			t.Fatalf("runSessionPicker failed: cancelled=%v err=%v", cancelled, err)
		}
		if selected.ID != "session-01" {
			t.Errorf("selected ID = %q, want session-01", selected.ID)
		}
	})

	t.Run("paging with page down and page up prevpage", func(t *testing.T) {
		console, inW, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		selector := NewSelector(makeSessions(20), "")
		// Page down (\x1b[6~), then page up / prevpage (\x1b[5~), down (\x1b[B), Enter
		if _, err := inW.Write([]byte("\x1b[6~\x1b[5~\x1b[B\r")); err != nil {
			t.Fatal(err)
		}

		selected, cancelled, err := runSessionPicker(console, lifecycle, selector)
		if err != nil || cancelled || selected == nil {
			t.Fatalf("runSessionPicker failed: cancelled=%v err=%v", cancelled, err)
		}
		if selected.ID != "session-01" {
			t.Errorf("selected ID = %q, want session-01 after paging and down", selected.ID)
		}
	})

	t.Run("search query typing and filter match", func(t *testing.T) {
		console, inW, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		selector := NewSelector(makeSessions(20), "")
		// Type query "session-15" then Enter
		if _, err := inW.Write([]byte("session-15\r")); err != nil {
			t.Fatal(err)
		}

		selected, cancelled, err := runSessionPicker(console, lifecycle, selector)
		if err != nil || cancelled || selected == nil {
			t.Fatalf("runSessionPicker failed: cancelled=%v err=%v", cancelled, err)
		}
		if selected.ID != "session-15" {
			t.Errorf("selected ID = %q, want session-15", selected.ID)
		}
	})

	t.Run("cancellation with escape", func(t *testing.T) {
		console, inW, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		selector := NewSelector(makeSessions(5), "")
		if _, err := inW.Write([]byte("\x1b")); err != nil {
			t.Fatal(err)
		}

		selected, cancelled, err := runSessionPicker(console, lifecycle, selector)
		if err != nil || !cancelled || selected != nil {
			t.Fatalf("expected cancel: cancelled=%v err=%v selected=%v", cancelled, err, selected)
		}
	})

	t.Run("cancellation with ctrl-c", func(t *testing.T) {
		console, inW, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		selector := NewSelector(makeSessions(5), "")
		if _, err := inW.Write([]byte{3}); err != nil {
			t.Fatal(err)
		}

		selected, cancelled, err := runSessionPicker(console, lifecycle, selector)
		if err != nil || !cancelled || selected != nil {
			t.Fatalf("expected cancel: cancelled=%v err=%v selected=%v", cancelled, err, selected)
		}
	})

	t.Run("resize event preserves state and does not exit", func(t *testing.T) {
		console, inW, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		selector := NewSelector(makeSessions(5), "")
		// Send resize signal, then Enter
		lifecycle.signals <- syscall.SIGWINCH
		if _, err := inW.Write([]byte("\r")); err != nil {
			t.Fatal(err)
		}

		selected, cancelled, err := runSessionPicker(console, lifecycle, selector)
		if err != nil || cancelled || selected == nil {
			t.Fatalf("runSessionPicker failed after resize: cancelled=%v err=%v", cancelled, err)
		}
		if selected.ID != "session-00" {
			t.Errorf("selected ID = %q, want session-00", selected.ID)
		}
	})

	t.Run("shutdown signal terminates picker with error", func(t *testing.T) {
		console, _, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		selector := NewSelector(makeSessions(5), "")
		lifecycle.signals <- syscall.SIGTERM

		selected, cancelled, err := runSessionPicker(console, lifecycle, selector)
		if err == nil || !cancelled || selected != nil {
			t.Fatalf("expected shutdown error: cancelled=%v err=%v", cancelled, err)
		}
	})
}

func TestStartupPickerChoiceNavigationAndSignals(t *testing.T) {
	t.Cleanup(func() {
		leakcheck.Verify(t)
	})

	makeChoicePicker := func(count int, initial int) *terminal.ChoicePicker {
		options := make([]terminal.ChoiceOption, count)
		for i := 0; i < count; i++ {
			options[i] = terminal.ChoiceOption{
				Label:       fmt.Sprintf("Choice %d", i),
				Description: fmt.Sprintf("Description for %d", i),
			}
		}
		return &terminal.ChoicePicker{
			Title:    "Confirm",
			Options:  options,
			Selected: initial,
		}
	}

	t.Run("navigation down and enter selects item", func(t *testing.T) {
		console, inW, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		picker := makeChoicePicker(3, 0)
		if _, err := inW.Write([]byte("\x1b[B\r")); err != nil {
			t.Fatal(err)
		}

		idx, cancelled, err := runChoicePicker(console, lifecycle, picker)
		if err != nil || cancelled {
			t.Fatalf("runChoicePicker failed: cancelled=%v err=%v", cancelled, err)
		}
		if idx != 1 {
			t.Errorf("selected index = %d, want 1", idx)
		}
	})

	t.Run("paging with page down and page up", func(t *testing.T) {
		console, inW, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		picker := makeChoicePicker(10, 0)
		// Page down (\x1b[6~ moves +5), Page up (\x1b[5~ moves -5), Down (\x1b[B moves +1), Enter
		if _, err := inW.Write([]byte("\x1b[6~\x1b[5~\x1b[B\r")); err != nil {
			t.Fatal(err)
		}

		idx, cancelled, err := runChoicePicker(console, lifecycle, picker)
		if err != nil || cancelled {
			t.Fatalf("runChoicePicker failed: cancelled=%v err=%v", cancelled, err)
		}
		if idx != 1 {
			t.Errorf("selected index = %d, want 1", idx)
		}
	})

	t.Run("escape cancels choice", func(t *testing.T) {
		console, inW, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		picker := makeChoicePicker(3, 0)
		if _, err := inW.Write([]byte("\x1b")); err != nil {
			t.Fatal(err)
		}

		idx, cancelled, err := runChoicePicker(console, lifecycle, picker)
		if err != nil || !cancelled || idx != -1 {
			t.Fatalf("expected cancel: idx=%d cancelled=%v err=%v", idx, cancelled, err)
		}
	})

	t.Run("signal resize redraws and shutdown signal errors", func(t *testing.T) {
		console, _, lifecycle, cleanup := testPickerConsole(t)
		defer cleanup()

		picker := makeChoicePicker(3, 0)
		lifecycle.signals <- syscall.SIGWINCH
		lifecycle.signals <- syscall.SIGTERM

		idx, cancelled, err := runChoicePicker(console, lifecycle, picker)
		if err == nil || !cancelled || idx != -1 {
			t.Fatalf("expected shutdown error: idx=%d cancelled=%v err=%v", idx, cancelled, err)
		}
	})
}

func TestStartupPickerInputEOFReturnsThroughLifecycleCleanup(t *testing.T) {
	t.Cleanup(func() {
		leakcheck.Verify(t)
	})
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	console := terminal.NewConsole(input, io.Discard)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	defer console.Close()
	defer input.Close()
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	lifecycle := &terminalLifecycle{console: console, signals: make(chan os.Signal, 1)}
	selector := NewSelector([]session.SessionInfo{{ID: "synthetic"}}, "")
	_, cancelled, err := runSessionPicker(console, lifecycle, selector)
	if err == nil || !cancelled {
		t.Fatalf("input EOF did not cancel startup picker: cancelled=%v err=%v", cancelled, err)
	}
}
