//go:build unix

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/terminal"
	"psi/internal/testutil"
)

// Explicit test flag, never an inherited environment switch. Every launch
// still uses synthetic configuration, isolated HOME, and loopback endpoints.
var reviewBinary = flag.String("psi-test-binary", "", "Run actual-app PTY regressions against this executable instead of the test helper")

func TestRuntimeFrameMetricsForPacedText(t *testing.T) {
	r := testRuntime(t)
	p := &pacedFrameProvider{finished: make(chan struct{})}
	r.prov, r.authErr = p, nil
	in, input, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &lockedFrameOutput{}
	console := terminal.NewConsole(in, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	finished := make(chan error, 1)
	go func() { finished <- r.repl(console) }()

	cleanupDone := false
	t.Cleanup(func() {
		if !cleanupDone {
			_ = input.Close()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
			}
		}
		console.Close()
		_ = in.Close()
	})
	start := time.Now()
	if _, err := input.Write([]byte("frame-profile\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(8 * time.Second)
	for !strings.Contains(out.String(), "FRAME_DONE") {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("paced provider did not finish: %q", out.String())
		}
	}
	select {
	case <-p.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not settle after final text")
	}
	// Let settlement progress, then retry /quit if the first key arrives before
	// the REPL has returned to idle input ownership.
	time.Sleep(50 * time.Millisecond)
	elapsed := time.Since(start)
	quitDeadline := time.After(5 * time.Second)
	quitRetry := time.NewTicker(100 * time.Millisecond)
	defer quitRetry.Stop()
	for {
		select {
		case err := <-finished:
			cleanupDone = true
			if err != nil {
				t.Fatal(err)
			}
			goto replExited
		default:
		}
		if _, err := input.Write([]byte("/quit\r")); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-finished:
			cleanupDone = true
			if err != nil {
				t.Fatal(err)
			}
			goto replExited
		case <-quitRetry.C:
		case <-quitDeadline:
			t.Fatal("REPL did not exit after /quit")
		}
	}
replExited:
	writes, frames, bytesWritten, _ := console.FrameStats()
	maxTimerFrames := int((elapsed*time.Duration(30)+time.Second-1)/time.Second) + 2
	maxWrite := out.MaxWrite()
	t.Logf("paced 10k delta frame profile: elapsed=%s timer_frames=%d event_frames=%d writer_frames=%d writer_writes=%d bytes=%d max_write=%d", elapsed, r.timerFrames, r.eventFrames, frames, writes, bytesWritten, maxWrite)
	if elapsed < 750*time.Millisecond {
		t.Fatalf("provider did not span a controlled second: %s", elapsed)
	}
	if r.timerFrames > maxTimerFrames {
		t.Fatalf("timer frames %d exceed 30Hz budget for %s: max=%d", r.timerFrames, elapsed, maxTimerFrames)
	}
	if frames > 100 || writes > frames {
		t.Fatalf("unexpected frame/write storm: frames=%d writes=%d", frames, writes)
	}
	if maxWrite > 64*1024 {
		t.Fatalf("console wrote an oversized batch: %d bytes", maxWrite)
	}
}

type pacedFrameProvider struct{ finished chan struct{} }

func (p *pacedFrameProvider) Send(ctx context.Context, _ provider.Request, onText func(string)) (*provider.Response, error) {
	defer close(p.finished)
	var text strings.Builder
	for i := 0; i < 10_000; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text.WriteByte('a')
		onText("a")
		if (i+1)%333 == 0 {
			timer := time.NewTimer(33 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	text.WriteString("FRAME_DONE")
	onText("FRAME_DONE")
	return &provider.Response{Text: text.String(), StopReason: "end_turn", Usage: provider.Usage{Input: 1, Output: 10_001}}, nil
}

func (p *pacedFrameProvider) CloseIdle() {}

type lockedFrameOutput struct {
	mu sync.Mutex
	bytes.Buffer
	maxWrite int
}

func (w *lockedFrameOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.maxWrite = max(w.maxWrite, len(data))
	return w.Buffer.Write(data)
}

func (w *lockedFrameOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.String()
}

func (w *lockedFrameOutput) MaxWrite() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxWrite
}

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
func (q *reviewPTY) prompt() {
	q.t.Helper()
	ready := func() bool {
		return semanticEditorReady(q.screen)
	}
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	quiet := time.NewTimer(40 * time.Millisecond)
	defer quiet.Stop()
	for {
		if ready() {
			select {
			case chunk, ok := <-q.chunks:
				if !ok {
					q.t.Fatalf("process exited before editor became ready: %q", q.raw.String())
				}
				q.pending += string(chunk)
				q.raw.Write(chunk)
				q.screen.Feed(chunk)
				if !quiet.Stop() {
					select {
					case <-quiet.C:
					default:
					}
				}
				quiet.Reset(40 * time.Millisecond)
			case <-quiet.C:
				q.pending = "" // all bytes are represented in the VT screen model
				return
			case <-timer.C:
				q.t.Fatalf("timeout waiting for bordered editor: %q", q.raw.String())
			}
		} else {
			select {
			case chunk, ok := <-q.chunks:
				if !ok {
					q.t.Fatalf("process exited before editor became ready: %q", q.raw.String())
				}
				q.pending += string(chunk)
				q.raw.Write(chunk)
				q.screen.Feed(chunk)
			case <-timer.C:
				q.t.Fatalf("timeout waiting for bordered editor: %q", q.raw.String())
			}
		}
	}
}
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
		q.t.Fatalf("process did not exit; output=%q", q.raw.String())
	}
	// Post-exit slave ioctls return ENOTTY on macOS once the session
	// leader is gone, so the exact state check stays Linux-only there.
	// The suspend test verifies restore on the live slave instead.
	if goruntime.GOOS != "darwin" {
		state, err := term.GetState(int(q.tty.Fd()))
		if err != nil || !sameTermState(state, q.original) {
			q.t.Fatalf("terminal not restored: %v", err)
		}
	}
}

// sameTermState compares every terminal field. macOS alone may change the
// driver-managed PENDIN bit while the foreground group is suspended.
func sameTermState(a, b *term.State) bool {
	return sameTermStateWithPENDIN(a, b, goruntime.GOOS == "darwin")
}

func sameTermStateWithPENDIN(a, b *term.State, allowPENDIN bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return sameTermStateDetails(fmt.Sprintf("%#v", a), fmt.Sprintf("%#v", b), allowPENDIN)
}

var lflagPattern = regexp.MustCompile(`Lflag:0x([0-9a-fA-F]+)`)

func sameTermStateDetails(a, b string, allowPENDIN bool) bool {
	if allowPENDIN {
		a, b = withoutPENDIN(a), withoutPENDIN(b)
	}
	return a == b
}

func withoutPENDIN(state string) string {
	match := lflagPattern.FindStringSubmatchIndex(state)
	if match == nil {
		return state
	}
	value, err := strconv.ParseUint(state[match[2]:match[3]], 16, 64)
	if err != nil {
		return state
	}
	normalized := fmt.Sprintf("Lflag:%#x", value&^uint64(0x20000000))
	return state[:match[0]] + normalized + state[match[1]:]
}

func TestTerminalStateComparisonMasksOnlyPENDIN(t *testing.T) {
	before := "termios{Iflag:0x1, Oflag:0x2, Lflag:0x100, Cflag:0x4}"
	pendin := "termios{Iflag:0x1, Oflag:0x2, Lflag:0x20000100, Cflag:0x4}"
	otherField := "termios{Iflag:0x1, Oflag:0x2, Lflag:0x20000100, Cflag:0x5}"
	if !sameTermStateDetails(before, pendin, true) {
		t.Fatal("PENDIN-only difference was not masked")
	}
	if sameTermStateDetails(before, otherField, true) {
		t.Fatal("comparison ignored a non-PENDIN terminal field")
	}
	if sameTermStateDetails(before, pendin, false) {
		t.Fatal("strict comparison masked a terminal difference")
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
	if !strings.Contains(q.screen.Text(), "  old question\n\n  old answer") {
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
	if strings.Count(q.screen.Text(), "  abd") != 1 {
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
	if err != nil || !sameTermState(state, q.original) {
		t.Fatalf("terminal state during suspend: before=%#v after=%#v error=%v", q.original, state, err)
	}
	if err := q.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	q.wait("unfinished")
	q.prompt()
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

// The startup header follows Pi's compact hint structure with psi's name.
func TestStartupHeaderUsesPiCompactHints(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	q := startReviewPTY(t, home, work, agentDir, "--deny-project")
	q.wait("psi v")
	q.wait("esc interrupt · ctrl+c/ctrl+d clear/exit · / commands")
	q.prompt()
	if strings.Contains(q.screen.Text(), "Pi can explain its own features") {
		t.Fatalf("startup kept Pi's onboarding paragraphs: %q", q.screen.Text())
	}
	q.write("/quit\r")
	q.finish()
}

// A4-06: cancelling inside a partial code fence keeps the partial text, ends
// the styles, and leaves the next prompt usable.
func TestActualCancelInsidePartialFenceKeepsTextAndNextPrompt(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"```go\\nfunc main() {\\n  println(\\\"partial\\\")\\n\"}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
	}))
	defer server.Close()
	reviewSettings(t, agentDir, server.URL)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("synthetic partial fence fixture")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("partial fence\r")
	q.wait("func main()")
	q.write("\x1b") // Pi's Escape binding cancels the active run.
	q.wait("interrupted")
	q.prompt()
	screen := q.screen.Text()
	if !strings.Contains(screen, "func main()") {
		t.Fatalf("cancelling inside a partial fence lost the visible text: %q", screen)
	}
	if !strings.Contains(screen, "interrupted") {
		t.Fatalf("cancelled run did not state its interrupted outcome: %q", screen)
	}
	if strings.Contains(screen, "\x1b[") {
		t.Fatalf("cancelled run leaked a style escape into the screen: %q", screen)
	}
	// The next prompt stays usable.
	q.write("/quit\r")
	q.finish()
	released = true
	after, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "partial fence") {
		t.Fatalf("cancelled run did not persist the partial exchange: %s", after)
	}
}
