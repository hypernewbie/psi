//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	}
	return messages
}

func TestBangShellGateHelper(t *testing.T) {
	address := os.Getenv("PSI_SHELL_GATE_ADDR")
	if address == "" {
		return
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Println("PSI_SHELL_LIVE")
	_, _ = io.Copy(io.Discard, conn)
}

func TestBangShellLiveActualApp(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("live editor proof requires -psi-test-binary")
	}
	home, launch := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		_ = listener.Close()
	})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-release
	}()
	helper, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	shellCommand := fmt.Sprintf("%q -test.run=^TestBangShellGateHelper$", helper)
	id := "shell-live"
	q := startReviewProcessEnv(t, home, launch, agentDir, *reviewBinary, "",
		[]string{"PSI_SHELL_GATE_ADDR=" + listener.Addr().String()},
		"--deny-project", "--no-tools", "--session-dir", sessionDir, "--session-id", id)
	q.prompt()
	q.write("!" + shellCommand + "\r")
	q.waitScreen("PSI_SHELL_LIVE")
	sessionPath := filepath.Join(sessionDir, id+".jsonl")
	if data, err := os.ReadFile(sessionPath); err == nil && strings.Contains(string(data), `"role":"bashExecution"`) {
		t.Fatal("shell entry was saved before the held command exited")
	}
	unblock()
	q.prompt()
	q.write("/quit\r")
	q.finish()
	messages := reopenShellSession(t, sessionPath, launch)
	if len(messages) != 1 {
		t.Fatalf("shell entries = %d, want 1", len(messages))
	}
	if messages[0].Command != shellCommand || !strings.Contains(messages[0].Output, "PSI_SHELL_LIVE") {
		t.Fatalf("live shell entry = %+v", messages[0])
	}
	if messages[0].ExitCode == nil || *messages[0].ExitCode != 0 || messages[0].Cancelled {
		t.Fatalf("live shell outcome = %+v", messages[0])
	}
}

func TestBangShellReplayActualApp(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("replay proof requires -psi-test-binary")
	}
	home, launch := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := session.New(session.OpenOptions{Cwd: launch, AgentDir: agentDir, SessionDir: sessionDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	output.WriteString("EARLY_REPLAY_MARKER\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&output, "replay line %02d\n", i)
	}
	output.WriteString("LATE_REPLAY_MARKER\n")
	zero := 0
	included := false
	if _, err := store.AppendMessage(session.AgentMessage{Role: "bashExecution", BashExecution: &session.BashExecutionMessage{
		Role: "bashExecution", Command: "printf replay", Output: output.String(), ExitCode: &zero,
		Timestamp: 1, ExcludeFromContext: &included,
	}}); err != nil {
		t.Fatal(err)
	}
	sessionFile := store.File()
	store.Unlock()

	check := func(q *reviewPTY) {
		q.prompt()
		if strings.Contains(q.raw.String(), "EARLY_REPLAY_MARKER") {
			t.Fatalf("collapsed replay exposed the early marker: %q", q.raw.String())
		}
		q.write("\x0f")
		q.wait("EARLY_REPLAY_MARKER")
		// The expansion status keeps the cursor hidden until the next draw;
		// the editor still owns input, so quit without waiting for a prompt.
		q.write("/quit\r")
		q.finish()
	}

	q := startReviewProcess(t, home, launch, agentDir, *reviewBinary, "",
		"--deny-project", "--no-tools", "--session", sessionFile)
	check(q)
	messages := reopenShellSession(t, sessionFile, launch)
	if len(messages) != 1 {
		t.Fatalf("shell entries after collapsed/expanded replay = %d, want 1", len(messages))
	}

	q = startReviewProcess(t, home, launch, agentDir, *reviewBinary, "",
		"--deny-project", "--no-tools", "--session", sessionFile)
	check(q)
	messages = reopenShellSession(t, sessionFile, launch)
	if len(messages) != 1 {
		t.Fatalf("shell entries after exact-file reopen = %d, want 1", len(messages))
	}
}

func TestBangShellOverlapActualApp(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("overlap proof requires -psi-test-binary")
	}
	home, launch := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launch, "fixture.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := make(chan map[string]any, 4)
	releaseTool := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseTool) }) }
	t.Cleanup(unblock)
	var turn atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		switch turn.Add(1) {
		case 1:
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"overlap-1\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"overlap-read\",\"name\":\"read\",\"input\":{\"path\":\"fixture.txt\"}}}\n\n")
			w.(http.Flusher).Flush()
			<-releaseTool
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {}\n\n")
		case 2:
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"overlap-2\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OVERLAP_MODEL_DONE\"}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
		case 3:
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"overlap-3\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"CONTEXT_DONE\"}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
		default:
			http.Error(w, "unexpected turn", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	reviewSettings(t, agentDir, server.URL)
	id := "shell-overlap"
	q := startReviewProcess(t, home, launch, agentDir, *reviewBinary, "",
		"--deny-project", "--session-dir", sessionDir, "--session-id", id)
	q.prompt()
	q.write("hold tool\r")
	first := <-requests
	_ = first
	q.write("!printf INCLUDED_OVERLAP\r")
	q.waitScreen("INCLUDED_OVERLAP")
	q.waitScreenGone("Running... (Esc to cancel)")
	q.write("!!printf EXCLUDED_OVERLAP\r")
	q.waitScreen("EXCLUDED_OVERLAP")
	q.waitScreenGone("Running... (Esc to cancel)")
	sessionPath := filepath.Join(sessionDir, id+".jsonl")
	if data, err := os.ReadFile(sessionPath); err == nil && strings.Contains(string(data), `"role":"bashExecution"`) {
		t.Fatal("a shell entry was saved inside the held tool exchange")
	}
	unblock()
	q.wait("OVERLAP_MODEL_DONE")
	q.prompt()
	_ = <-requests // the tool continuation must not see the pending shell yet
	q.write("follow up\r")
	q.wait("CONTEXT_DONE")
	q.prompt()
	third := <-requests
	encoded, _ := json.Marshal(third["messages"])
	if !strings.Contains(string(encoded), "INCLUDED_OVERLAP") {
		t.Fatalf("follow-up request lost included shell output: %s", encoded)
	}
	if strings.Contains(string(encoded), "EXCLUDED_OVERLAP") {
		t.Fatalf("follow-up request leaked excluded shell output: %s", encoded)
	}
	q.write("/quit\r")
	q.finish()

	store, err := session.OpenFile(sessionPath, launch, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Unlock()
	roles := []string{}
	shells := []*session.BashExecutionMessage{}
	assistant, toolResult, firstShell := -1, -1, -1
	for _, entry := range store.Entries() {
		if entry.Message == nil {
			continue
		}
		message := entry.Message.Message
		role := message.Role
		if message.Assistant != nil {
			role = "assistant"
			if assistant < 0 {
				assistant = len(roles)
			}
		}
		if message.ToolResult != nil {
			role = "toolResult"
			toolResult = len(roles)
		}
		if message.BashExecution != nil {
			role = "bashExecution"
			shells = append(shells, message.BashExecution)
			if firstShell < 0 {
				firstShell = len(roles)
			}
		}
		roles = append(roles, role)
	}
	if assistant < 0 || toolResult < 0 || firstShell < 0 || !(assistant < toolResult && toolResult < firstShell) {
		t.Fatalf("shell entry order = %v, want assistant < toolResult < bashExecution", roles)
	}
	if len(shells) != 2 {
		t.Fatalf("shell entries = %d, want 2", len(shells))
	}
	if shells[0].Command != "printf INCLUDED_OVERLAP" || shells[1].Command != "printf EXCLUDED_OVERLAP" {
		t.Fatalf("shell completion order = %q, %q", shells[0].Command, shells[1].Command)
	}
	if shells[1].ExcludeFromContext == nil || !*shells[1].ExcludeFromContext {
		t.Fatal("excluded overlap shell lost its flag")
	}
}

func TestBangShellMVPActualApp(t *testing.T) {
	home, launch := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
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
	q.write("!!printf '%s%s\\n' PSI_ PRIVATE_SHELL\r")
	q.waitScreen("PSI_PRIVATE_SHELL")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	sessionPath := filepath.Join(sessionDir, id+".jsonl")
	messages := reopenShellSession(t, sessionPath, launch)
	if len(messages) != 2 {
		t.Fatalf("shell entries = %d, want 2", len(messages))
	}
	for i, want := range []struct {
		command string
		output  string
		exclude bool
	}{
		{"printf '%s%s\\n' PSI_ SHELL_MVP", "PSI_SHELL_MVP\n", false},
		{"printf '%s%s\\n' PSI_ PRIVATE_SHELL", "PSI_PRIVATE_SHELL\n", true},
	} {
		got := messages[i]
		if got.Command != want.command || got.Output != want.output || got.ExcludeFromContext == nil || *got.ExcludeFromContext != want.exclude {
			t.Fatalf("shell entry %d = %+v, want %+v", i, got, want)
		}
		if got.ExitCode == nil || *got.ExitCode != 0 || got.Cancelled {
			t.Fatalf("shell entry %d outcome = %+v", i, got)
		}
	}
	if _, err := os.Stat(bashEnvMarker); !os.IsNotExist(err) {
		t.Fatal("parent BASH_ENV leaked into the shell")
	}
}

func TestBangShellStalledActualApp(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("native process cleanup acceptance requires -psi-test-binary")
	}
	home, launch := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(home, "stalled.pid")
	id := "shell-stalled"
	q := startReviewProcess(t, home, launch, agentDir, *reviewBinary, "",
		"--deny-project", "--no-tools", "--session-dir", sessionDir, "--session-id", id)
	q.prompt()
	// Rename the readiness file so the reader never sees a half-written PID.
	q.write(fmt.Sprintf("!sleep 30 & child=$!; printf PARTIAL_MARKER; echo $child > %q && mv %q %q; wait\r", pidPath+".tmp", pidPath+".tmp", pidPath))
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
		if err := os.MkdirAll(sessionDir, 0o700); err != nil {
			t.Fatal(err)
		}
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
