//go:build unix

package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pibridgepb "github.com/hypernewbie/psi/internal/pibridge/pb"
	"github.com/hypernewbie/psi/internal/terminal"
)

// drainOutputAfterExit closes the harness's extra slave descriptor, then
// collects output that arrived after the last wait. The child is already
// gone; closing the descriptor is what lets the master read reach EOF and
// the reader goroutine close the chunk channel.
func (q *reviewPTY) drainOutputAfterExit() string {
	q.t.Helper()
	_ = q.tty.Close()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				return q.raw.String()
			}
			q.raw.Write(chunk)
		case <-deadline.C:
			q.t.Fatal("output did not settle after process exit")
		}
	}
}

// quitFixture is a local provider that streams one visible partial response
// and then holds the turn until the process cancels it.
type quitFixture struct {
	home, work, agentDir string
	requests             atomic.Int32
}

// quitFixtureGate opts a fixture into holding the request body and the SSE
// partial until the test releases them. It lets a test observe the keyword
// confirmation while the same run is still active.
type quitFixtureGate struct {
	bodies  chan []byte
	release chan struct{}
	once    sync.Once
}

func (g *quitFixtureGate) open() {
	g.once.Do(func() { close(g.release) })
}

func newQuitFixture(t *testing.T) *quitFixture {
	t.Helper()
	return newQuitFixtureGated(t, nil)
}

func newGatedQuitFixture(t *testing.T) (*quitFixture, *quitFixtureGate) {
	t.Helper()
	gate := &quitFixtureGate{bodies: make(chan []byte, 1), release: make(chan struct{})}
	return newQuitFixtureGated(t, gate), gate
}

func newQuitFixtureGated(t *testing.T, gate *quitFixtureGate) *quitFixture {
	t.Helper()
	f := &quitFixture{home: t.TempDir(), work: t.TempDir()}
	f.agentDir = filepath.Join(f.home, "agent")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gate == nil {
			io.Copy(io.Discard, r.Body)
		} else {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read quit fixture request: %v", err)
				http.Error(w, "cannot read request", http.StatusBadRequest)
				return
			}
			select {
			case gate.bodies <- body:
			case <-r.Context().Done():
				return
			}
			select {
			case <-gate.release:
			case <-r.Context().Done():
				return
			}
		}
		f.requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `event: message_start
data: {"message":{"id":"quit","model":"synthetic-model","usage":{"input_tokens":1}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"index":0,"delta":{"type":"text_delta","text":%q}}

`, quitPartial)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Hold the stream until /quit cancels: the turn never completes.
		<-r.Context().Done()
	}))
	// Close the server after the child dies: httptest Close waits for the
	// held stream, and the child's kill-cleanup registers next (LIFO runs
	// it first). A direct defer would deadlock teardown on failure.
	t.Cleanup(server.Close)
	writeAppFile(t, filepath.Join(f.agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(f.agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(f.agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-quit-key"}}`)
	return f
}

func (f *quitFixture) assertSettled(t *testing.T, raw string, wantRequests int32) {
	t.Helper()
	_, tail, found := strings.Cut(raw, quitReleaseMarker)
	if !found {
		t.Fatal("no terminal release output")
	}
	if tail != "" {
		t.Fatalf("writes after terminal release: %q", tail)
	}
	if n := f.requests.Load(); n != wantRequests {
		t.Fatalf("provider requests = %d, want %d", n, wantRequests)
	}
	assertAbortedQuitSession(t, f.work, f.agentDir)
}

func TestActualAppQuitReleasesTTYAndPersistsPartial(t *testing.T) {
	f := newQuitFixture(t)
	q := startReviewPTY(t, f.home, f.work, f.agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("partial work\r")
	q.wait(quitPartial)
	q.write("/quit\r")
	q.wait(quitReleaseMarker)
	q.finish()
	f.assertSettled(t, q.drainOutputAfterExit(), 1)
}

// TestActualAppExitKeywordsBusy proves a normal prompt disarms confirmation,
// a keyword does not interrupt a live run, and a second keyword exits through
// the busy /quit path.
func TestActualAppExitKeywordsBusy(t *testing.T) {
	f, gate := newGatedQuitFixture(t)
	defer gate.open()
	q := startReviewPTY(t, f.home, f.work, f.agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("quit\r")
	q.waitScreen(terminal.ExitHintText)
	q.write("quit now\r")
	body := busyWaitBody(t, gate.bodies)
	if !strings.Contains(string(body), "quit now") {
		t.Fatalf("first request missing the original prompt: %s", body)
	}
	q.waitScreen("› quit now")
	if strings.Contains(q.screen.Text(), terminal.ExitHintText) {
		t.Fatalf("confirmation stayed visible after a normal prompt: %q", q.screen.Text())
	}

	// This quit must show a fresh hint, not confirm the one before the prompt.
	q.write("quit\r")
	q.waitScreen(terminal.ExitHintText)
	if screen := q.screen.Text(); strings.Contains(screen, "Steering: quit") || strings.Contains(screen, "Follow-up: quit") {
		t.Fatalf("first keyword was queued during the run: %q", screen)
	}
	gate.open()
	q.wait(quitPartial)
	if !strings.Contains(q.screen.Text(), terminal.ExitHintText) {
		t.Fatalf("confirmation disappeared before the second keyword: %q", q.screen.Text())
	}
	q.write("exit\r")
	q.wait(quitReleaseMarker)
	q.finish()
	f.assertSettled(t, q.drainOutputAfterExit(), 1)
}

// TestActualAppExitKeywordsIdle proves normalization, cancellation by another
// submission, the reverse spelling order, and that no prompt reaches a
// provider.
func TestActualAppExitKeywordsIdle(t *testing.T) {
	f := newQuitFixture(t)
	q := startReviewPTY(t, f.home, f.work, f.agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write(" EXIT \r")
	q.waitScreen(terminal.ExitHintText)
	q.write("/hotkeys\r")
	q.waitScreen("Keys (fixed, no Kitty required):")
	if strings.Contains(q.screen.Text(), terminal.ExitHintText) {
		t.Fatalf("confirmation stayed visible after another command: %q", q.screen.Text())
	}
	q.write("exit\r")
	q.waitScreen(terminal.ExitHintText)
	q.write("\r")
	const draft = "unsubmitted exit-state draft"
	q.write(draft)
	q.waitScreen(draft)
	if strings.Contains(q.screen.Text(), terminal.ExitHintText) {
		t.Fatalf("confirmation stayed armed after an empty submission: %q", q.screen.Text())
	}
	q.write("\x15") // Ctrl+U erases the draft.
	q.write("exit\r")
	q.waitScreen(terminal.ExitHintText)
	q.write("quit\r")
	q.finish()
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("provider requests = %d, want 0", n)
	}
}

func TestActualAppQuitWithBridgeConnected(t *testing.T) {
	f := newQuitFixture(t)
	phi := startBridgePhi(t)
	q := startReviewProcessEnv(t, f.home, f.work, f.agentDir, *reviewBinary, "", []string{
		"PHI_PI_BRIDGE_ENDPOINT=" + phi.target,
		"PHI_PI_BRIDGE_PANE_ID=pane-quit",
		"PHI_PI_BRIDGE_PROCESS_ID=proc-quit",
		"PHI_PI_BRIDGE_TOKEN=" + strings.Repeat("cd", 32),
		"TMPDIR=" + os.TempDir(),
	}, "--deny-project", "--no-tools")
	q.prompt()
	q.write("partial work\r")
	phi.waitFrame(t, "agent start", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetAgentStarted() != nil
	})
	q.wait(quitPartial)
	q.write("/quit\r")
	q.wait(quitReleaseMarker)
	q.finish()
	// The current bridge client does not guarantee delivery of a final
	// frame after process teardown, so this proof stops at the local
	// release, the request count, and the persisted partial.
	f.assertSettled(t, q.drainOutputAfterExit(), 1)
}
