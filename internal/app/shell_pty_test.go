//go:build unix

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/session"
)

func writeShellHelper(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func shellSessionStore(t *testing.T, cwd, agentDir, sessionDir string) *session.Store {
	t.Helper()
	store, err := session.New(session.OpenOptions{Cwd: cwd, AgentDir: agentDir, SessionDir: sessionDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	return store
}

func reopenShellSession(t *testing.T, path, cwd string) []*session.BashExecutionMessage {
	t.Helper()
	store, err := session.OpenFile(path, cwd, false)
	if err != nil {
		t.Fatal(err)
	}
	var messages []*session.BashExecutionMessage
	for _, entry := range store.Entries() {
		if entry.Message != nil && entry.Message.Message.BashExecution != nil {
			messages = append(messages, entry.Message.Message.BashExecution)
		}
		if entry.Message != nil && entry.Message.Message.Role == "user" && strings.Contains(sessionMessageText(entry), "seed") {
			continue
		}
	}
	return messages
}

func sessionMessageText(entry *session.Entry) string {
	if entry.Message == nil || entry.Message.Message.User == nil {
		return ""
	}
	if content, ok := entry.Message.Message.User.Content.(string); ok {
		return content
	}
	return ""
}

func TestBangShellMVPActualApp(t *testing.T) {
	home, launch := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	os.MkdirAll(sessionDir, 0o700)
	bashEnvMarker := filepath.Join(home, "bash-env-ran")
	bashEnv := filepath.Join(home, "bash-env.sh")
	writeAppFile(t, bashEnv, fmt.Sprintf("#!/bin/sh\ntouch %q\n", bashEnvMarker))
	t.Setenv("BASH_ENV", bashEnv)
	t.Setenv("PSI_SHELL", filepath.Join(home, "missing-shell"))
	id := "shell-mvp"
	q := startReviewProcess(t, home, launch, agentDir, *reviewBinary, "",
		"--deny-project", "--no-tools", "--session-dir", sessionDir, "--session-id", id)
	q.prompt()
	q.write("!printf '%s%s\\n' PSI_ SHELL_MVP\r")
	q.waitScreen("PSI_SHELL_MVP")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	sessionPath := filepath.Join(sessionDir, id+".jsonl")
	messages := reopenShellSession(t, sessionPath, launch)
	if len(messages) != 1 {
		t.Fatalf("shell entries = %d", len(messages))
	}
	message := messages[0]
	if message.Command != "printf '%s%s\\n' PSI_ SHELL_MVP" || !strings.Contains(message.Output, "PSI_SHELL_MVP") {
		t.Fatalf("saved shell = %+v", message)
	}
	if message.ExitCode == nil || *message.ExitCode != 0 || message.Cancelled {
		t.Fatalf("outcome = %+v", message)
	}
	if _, err := os.Stat(bashEnvMarker); !os.IsNotExist(err) {
		t.Fatal("parent BASH_ENV leaked into the shell")
	}
}

func TestBangShellStalledActualApp(t *testing.T) {
	home, launch := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	os.MkdirAll(sessionDir, 0o700)
	pidPath := filepath.Join(home, "stalled.pid")
	id := "shell-stalled"
	q := startReviewProcess(t, home, launch, agentDir, *reviewBinary, "",
		"--deny-project", "--no-tools", "--session-dir", sessionDir, "--session-id", id)
	q.prompt()
	q.write(fmt.Sprintf("!sleep 30 & child=$!; echo $child > %s; printf PARTIAL_MARKER; wait\r", pidPath))
	pidDeadline := time.Now().Add(3 * time.Second)
	var pid int
	for {
		pidText, err := os.ReadFile(pidPath)
		if err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(pidText)))
			if err != nil {
				t.Fatalf("stalled shell pid %q: %v", pidText, err)
			}
			break
		}
		if time.Now().After(pidDeadline) {
			t.Fatalf("stalled shell pid file: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	q.write("\x1b")
	q.waitScreen("(cancelled)")
	q.waitScreen("PARTIAL_MARKER")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	messages := reopenShellSession(t, filepath.Join(sessionDir, id+".jsonl"), launch)
	if len(messages) != 1 {
		t.Fatalf("shell entries = %d", len(messages))
	}
	if !messages[0].Cancelled || messages[0].ExitCode != nil || !strings.Contains(messages[0].Output, "PARTIAL_MARKER") {
		t.Fatalf("stalled result = %+v", messages[0])
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stalled shell pid %d still exists (kill0=%v)", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBangShellConfiguredActualApp(t *testing.T) {
	t.Run("relative shell path uses stored cwd and symlink semantics", func(t *testing.T) {
		home := t.TempDir()
		launch := filepath.Join(home, "launch")
		stored := filepath.Join(home, "stored")
		fixture := filepath.Join(home, "fixture")
		agentDir := filepath.Join(home, "agent")
		sessionDir := filepath.Join(home, "sessions")
		for _, dir := range []string{launch, stored, filepath.Join(fixture, "target", "child"), sessionDir} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		storedArgs := filepath.Join(home, "stored-args")
		decoyArgs := filepath.Join(home, "decoy-args")
		realHelper := filepath.Join(fixture, "target", "helper")
		writeShellHelper(t, realHelper, fmt.Sprintf("#!/bin/sh\nprintf 'ARG:%%s\\n' \"$@\" > %q\nprintf 'CONFIGURED_HELPER_OK\\n'\n", storedArgs))
		writeShellHelper(t, filepath.Join(stored, "helper"), fmt.Sprintf("#!/bin/sh\ntouch %q\nprintf 'DECOY_HELPER\\n'\n", decoyArgs))
		if err := os.Symlink(filepath.Join(fixture, "target", "child"), filepath.Join(stored, "link")); err != nil {
			t.Fatal(err)
		}
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"shellPath":"./link/../helper","shellCommandPrefix":"PREFIX_LINE","compaction":{"enabled":false}}`)
		store := shellSessionStore(t, stored, agentDir, sessionDir)
		sessionFile := store.File()
		store.Unlock()

		q := startReviewProcess(t, home, launch, agentDir, *reviewBinary, "",
			"--deny-project", "--no-tools", "--session", sessionFile)
		q.prompt()
		q.write("!echo configured\r")
		q.waitScreen("CONFIGURED_HELPER_OK")
		q.prompt()
		q.write("/quit\r")
		q.finish()
		args, err := os.ReadFile(storedArgs)
		if err != nil {
			t.Fatalf("stored helper did not run: %v", err)
		}
		if !strings.Contains(string(args), "ARG:-c\nARG:PREFIX_LINE\necho configured\n") {
			t.Fatalf("helper args = %q", args)
		}
		if _, err := os.Stat(decoyArgs); !os.IsNotExist(err) {
			t.Fatal("launch-directory decoy ran")
		}
	})

	t.Run("inherited SHELL", func(t *testing.T) {
		home, launch := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		sessionDir := filepath.Join(home, "sessions")
		os.MkdirAll(sessionDir, 0o700)
		argsPath := filepath.Join(home, "shell-args")
		helper := filepath.Join(home, "fixture-shell")
		writeShellHelper(t, helper, fmt.Sprintf("#!/bin/sh\nprintf 'ARG:%%s\\n' \"$@\" > %q\nprintf 'SHELL_HELPER_OK\\n'\n", argsPath))
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"compaction":{"enabled":false}}`)
		store := shellSessionStore(t, launch, agentDir, sessionDir)
		sessionFile := store.File()
		store.Unlock()
		q := startReviewProcessEnv(t, home, launch, agentDir, *reviewBinary, "", []string{"SHELL=" + helper},
			"--deny-project", "--no-tools", "--session", sessionFile)
		q.prompt()
		q.write("!echo from-inherited-shell\r")
		q.waitScreen("SHELL_HELPER_OK")
		q.prompt()
		q.write("/quit\r")
		q.finish()
		args, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatalf("SHELL helper did not run: %v", err)
		}
		if !strings.Contains(string(args), "ARG:-c\nARG:echo from-inherited-shell\n") {
			t.Fatalf("SHELL helper args = %q", args)
		}
	})
}
