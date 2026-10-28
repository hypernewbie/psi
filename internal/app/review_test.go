//go:build unix

package app

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"psi/internal/session"
	"psi/internal/terminal"
	"psi/internal/testutil"
)

// Explicit test flag, never an inherited environment switch. Every launch
// still uses synthetic configuration, isolated HOME, and loopback endpoints.
var reviewBinary = flag.String("psi-test-binary", "", "Run actual-app PTY regressions against this executable instead of the test helper")

func TestReviewAppProcess(t *testing.T) {
	if os.Getenv("PSI_REVIEW_PROCESS") != "1" || flag.Lookup("test.run").Value.String() != "^TestReviewAppProcess$" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("PSI_REVIEW_ARGS")), &args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := Run(args, "review"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type reviewPTY struct {
	t        *testing.T
	cmd      *exec.Cmd
	master   *os.File
	tty      *os.File
	original *term.State
	chunks   chan []byte
	pending  string
	raw      bytes.Buffer
	screen   *testutil.VTModel
}

func startReviewPTY(t *testing.T, home, work, agentDir string, args ...string) *reviewPTY {
	t.Helper()
	return startReviewProcess(t, home, work, agentDir, *reviewBinary, "", args...)
}

func startReviewProcess(t *testing.T, home, work, agentDir, binary, searchPath string, args ...string) *reviewPTY {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReviewAppProcess$")
	if binary != "" {
		executable, err := filepath.Abs(binary)
		if err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command(executable, args...)
	}
	cmd.Dir = work
	encoded, _ := json.Marshal(args)
	cmd.Env = append(appTestEnv(home, agentDir), "PSI_APP_TEST_PROCESS=", "PSI_REVIEW_PROCESS=1", "PSI_REVIEW_ARGS="+string(encoded))
	if searchPath != "" {
		for i, value := range cmd.Env {
			if strings.HasPrefix(value, "PATH=") {
				cmd.Env[i] = "PATH=" + searchPath
			}
		}
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	original, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	// Watch the slave side, not the master: macOS gives the master a
	// PENDIN flag the slave never has. A dup observes slave termios
	// without disturbing the child stdio.
	dup, err := syscall.Dup(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	tty := os.NewFile(uintptr(dup), "slave-watch")
	if err := pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		slave.Close()
		master.Close()
		t.Fatal(err)
	}
	slave.Close()
	q := &reviewPTY{t: t, cmd: cmd, master: master, tty: tty, original: original, chunks: make(chan []byte, 256), screen: testutil.NewVTModel(80, 40)}
	go func() {
		defer close(q.chunks)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				q.chunks <- bytes.Clone(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
		tty.Close()
		master.Close()
	})
	return q
}

func (q *reviewPTY) write(s string) {
	q.t.Helper()
	if _, err := q.master.WriteString(s); err != nil {
		q.t.Fatal(err)
	}
}
func (q *reviewPTY) wait(want string) {
	q.t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if i := strings.Index(q.pending, want); i >= 0 {
			q.pending = q.pending[i+len(want):]
			return
		}
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				q.t.Fatalf("process exited waiting for %q: %q", want, q.raw.String())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timer.C:
			q.t.Fatalf("timeout waiting for %q: %q", want, q.raw.String())
		}
	}
}
func (q *reviewPTY) prompt() { q.t.Helper(); q.wait("> \r\x1b[2C") }
func (q *reviewPTY) finish() {
	q.t.Helper()
	done := make(chan error, 1)
	go func() { done <- q.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			q.t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		q.cmd.Process.Kill()
		<-done
		q.t.Fatal("process did not exit")
	}
	// Post-exit slave ioctls return ENOTTY on macOS once the session
	// leader is gone. The suspend test verifies restore on the live
	// slave; here Linux keeps the exact check.
	if goruntime.GOOS != "darwin" {
		state, err := term.GetState(int(q.tty.Fd()))
		if err != nil || !reflect.DeepEqual(state, q.original) {
			q.t.Fatalf("terminal not restored: %v", err)
		}
	}
}

func reviewSettings(t *testing.T, agentDir, url string) {
	t.Helper()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+url+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-review-key"}}`)
}

func TestActualResumeScreenBackspaceAndNewSessionRecall(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	requests := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ANSWER\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	reviewSettings(t, agentDir, server.URL)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("old question")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "old answer"}}}}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	if !strings.Contains(q.screen.Text(), "You\nold question\n\nAssistant\nold answer") {
		t.Fatal("resumed history missing from screen: " + q.screen.Text())
	}
	q.write("abc\x7fd\r")
	q.wait("ANSWER")
	q.prompt()
	body := <-requests
	messages := body["messages"].([]any)
	last, _ := json.Marshal(messages[len(messages)-1])
	if !bytes.Contains(last, []byte(`"abd"`)) || bytes.Contains(last, []byte("abcd")) {
		t.Fatalf("backspace not reflected in request: %s", last)
	}
	if strings.Count(q.screen.Text(), "> abd") != 1 {
		t.Fatal("accepted input duplicated: " + q.screen.Text())
	}
	q.write("/history all\r")
	q.wait("Historical messages (display only, not model input)")
	q.prompt()
	q.write("/new\r")
	q.wait("new session")
	q.prompt()
	q.write("\x10\r/session\r")
	q.wait("context messages 0")
	q.prompt()
	select {
	case <-requests:
		t.Fatal("new session retained a prior prompt")
	default:
	}
	q.write("/quit\r")
	q.finish()
}

func TestActualSuspendRestoresTerminalAndResumesInput(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	// No provider request is needed. The endpoint is deliberately unused.
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-session")
	q.prompt()
	q.write("unfinished")
	q.wait("unfinished")
	q.write("\x1a")
	q.wait("\x1b[?2004l")
	deadline := time.Now().Add(3 * time.Second)
	stopped := false
	for time.Now().Before(deadline) {
		// /proc exists on Linux only. ps reports the stopped (T) state
		// on Linux and macOS alike.
		out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(q.cmd.Process.Pid)).Output()
		if err == nil && len(out) > 0 && out[0] == 'T' {
			stopped = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !stopped {
		t.Fatal("suspend did not stop the process")
	}
	state, err := term.GetState(int(q.tty.Fd()))
	if err != nil || !reflect.DeepEqual(state, q.original) {
		t.Fatalf("terminal state during suspend: before=%#v after=%#v error=%v", q.original, state, err)
	}
	if err := q.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	q.wait("unfinished")
	q.wait("\r\x1b[12C")
	q.write("\x03/quit\r")
	q.finish()
}

func TestOfflineImportedSelectionKeepsItsIdentity(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.store.AppendModelChange("unsupported-history-provider", "historical-model"); err != nil {
		t.Fatal(err)
	}
	if err := r.restore(); err != nil {
		t.Fatal(err)
	}
	if r.ov.Provider != "unsupported-history-provider" || r.ov.Model != "historical-model" || r.prov != nil || r.authErr == nil {
		t.Fatal("imported selection silently replaced or discarded")
	}
	if _, err := r.command(&terminal.Command{Name: "model", Arg: "anthropic m"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestReplayCancellationStopsTheActualTimelineWalk(t *testing.T) {
	items := []session.TranscriptItem{{Kind: session.KindUser, Text: "first"}, {Kind: session.KindAssistant, Text: "must not print"}}
	calls := 0
	complete := renderTimelineChecked(func(s string) bool { calls++; return calls < 2 }, items)
	if complete || calls != 2 {
		t.Fatalf("cancelled walk kept printing: complete=%v calls=%d", complete, calls)
	}
}

func TestStartupLineDoesNotConsumeFollowingEditorInput(t *testing.T) {
	in := strings.NewReader("yes\nfirst prompt\n")
	line, err := readStartupLine(in)
	if err != nil || line != "yes" {
		t.Fatal(line, err)
	}
	rest := make([]byte, in.Len())
	in.Read(rest)
	if string(rest) != "first prompt\n" {
		t.Fatal("startup stole editor input")
	}
}
