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
	"sync/atomic"
	"testing"
	"time"

	pibridgepb "psi/internal/pibridge/pb"
	"psi/internal/session"
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

func newQuitFixture(t *testing.T) *quitFixture {
	t.Helper()
	f := &quitFixture{home: t.TempDir(), work: t.TempDir()}
	f.agentDir = filepath.Join(f.home, "agent")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		f.requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `event: message_start
data: {"message":{"id":"quit","model":"synthetic-model","usage":{"input_tokens":1}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"index":0,"delta":{"type":"text_delta","text":"QUIT_PARTIAL"}}

`)
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
	f.assertPartialPersisted(t)
}

func (f *quitFixture) assertPartialPersisted(t *testing.T) {
	t.Helper()
	dir := session.DefaultSessionDir(f.work, f.agentDir)
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one session in %s; got %v (%v)", dir, files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "QUIT_PARTIAL") {
		t.Fatal("aborted partial was not persisted")
	}
}

func TestActualAppQuitReleasesTTYAndPersistsPartial(t *testing.T) {
	f := newQuitFixture(t)
	q := startReviewPTY(t, f.home, f.work, f.agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("partial work\r")
	q.wait("QUIT_PARTIAL")
	q.write("/quit\r")
	q.wait(quitReleaseMarker)
	q.finish()
	f.assertSettled(t, q.drainOutputAfterExit(), 1)
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
	q.wait("QUIT_PARTIAL")
	q.write("/quit\r")
	q.wait(quitReleaseMarker)
	q.finish()
	// The current bridge client does not guarantee delivery of a final
	// frame after process teardown, so this proof stops at the local
	// release, the request count, and the persisted partial.
	f.assertSettled(t, q.drainOutputAfterExit(), 1)
}
