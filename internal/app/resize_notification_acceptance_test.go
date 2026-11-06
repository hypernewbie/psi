//go:build acceptance && unix

package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"psi/internal/session"
)

const (
	resizeLedgerSentinel = "RESIZE_LEDGER_SENTINEL"
	resizeDraftText      = "RESIZE_DRAFT"
	networkStableMarker  = "NETWORK_STABLE_SENTINEL"
	networkActiveMarker  = "NETWORK_ACTIVE_TAIL"
	networkDraftText     = "NETWORK_DRAFT"
	// SIGWINCH has no acknowledgement. Keep the bounded silence check out of
	// default CI; deterministic console tests cover the input behavior there.
	resizeObservationWindow = 500 * time.Millisecond
	fullClearSequence       = "\x1b[2J\x1b[H\x1b[3J"
)

func TestResizeNotificationPTY(t *testing.T) {
	var requests atomic.Int32
	releaseStream := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) != 1 {
			http.Error(w, "unexpected extra provider request", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("provider request = %s %s, want POST /v1/messages", r.Method, r.URL.Path)
			http.Error(w, "unexpected provider endpoint", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"resize-1\",\"model\":\"m\",\"usage\":{\"input_tokens\":5}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		// Settle one paragraph and leave the second active, with no stop event.
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"%s stable text.\\n\\n%s pending\"}}\n\n", networkStableMarker, networkActiveMarker)
		w.(http.Flusher).Flush()
		select {
		case <-releaseStream:
		case <-r.Context().Done():
			close(cancelled) // Only the first request reaches this branch.
		}
	}))
	// This cleanup runs once, after the child's later-registered cleanup. The
	// release also prevents server.Close from blocking if cancellation fails.
	t.Cleanup(func() {
		close(releaseStream)
		server.Close()
	})

	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, server.URL)
	store, err := session.New(session.OpenOptions{
		Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Unlock()
	message := resizeLedgerSentinel + " retained transcript paragraph long enough to wrap" +
		" across a narrow terminal row while keeping every word readable."
	if _, err := store.AppendMessage(agentMessageUser(message)); err != nil {
		t.Fatal(err)
	}
	store.Unlock()

	q := startReviewPTY(t, home, work, agentDir,
		"--deny-project", "--no-skills", "--no-context-files", "--no-tools", "--session", store.File())
	// The helper starts at 80x40. Wait for that render before establishing the
	// test size, or startup could consume the resize without a rebuild.
	q.wait(resizeLedgerSentinel)
	q.prompt()
	q.resizeTo(t, 80, 24)
	q.prompt()
	q.write(resizeDraftText)
	q.waitScreen(resizeDraftText)

	mark := q.raw.Len()
	for attempt := 1; attempt <= 2; attempt++ {
		q.signalResize(t)
		q.observeWindow()
		// Scan cumulatively so a clear split across observation windows is
		// still detected, regardless of PTY chunk boundaries.
		if window := q.raw.String()[mark:]; cleared(window) {
			t.Fatalf("same-size notification %d cleared the screen: %q", attempt, window)
		}
		q.requireResizeState(t, resizeDraftText, resizeLedgerSentinel)
	}

	mark = q.resizeTo(t, 100, 30)
	q.prompt()
	q.requireSingleClear(t, q.raw.String()[mark:])
	q.requireResizeState(t, resizeDraftText, resizeLedgerSentinel)

	mark = q.raw.Len()
	q.signalResize(t)
	q.observeWindow()
	if window := q.raw.String()[mark:]; cleared(window) {
		t.Fatalf("same-size notification after a real resize cleared the screen: %q", window)
	}
	q.requireResizeState(t, resizeDraftText, resizeLedgerSentinel)

	q.write("\x15resize under a stalled stream\r")
	q.waitScreen(networkActiveMarker)
	q.write(networkDraftText)
	q.waitScreen(networkDraftText)

	mark = q.raw.Len()
	for attempt := 1; attempt <= 2; attempt++ {
		q.signalResize(t)
		q.observeWindow()
		if window := q.raw.String()[mark:]; cleared(window) {
			t.Fatalf("stalled-run notification %d cleared the screen: %q", attempt, window)
		}
		q.requireResizeState(t, networkDraftText, resizeLedgerSentinel, networkStableMarker, networkActiveMarker)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("same-size notifications started %d provider requests, want 1", got)
	}
	requirePendingCancellation(t, cancelled, "a same-size notification cancelled the stalled request")

	// A busy editor hides its cursor. Wait for the rebuild and text, not the
	// idle-only prompt predicate.
	mark = q.resizeTo(t, 60, 20)
	q.requireSingleClear(t, q.raw.String()[mark:])
	q.requireResizeState(t, networkDraftText, resizeLedgerSentinel, networkStableMarker, networkActiveMarker)
	requirePendingCancellation(t, cancelled, "a real resize cancelled the stalled request")

	q.pending = ""
	q.write("\x1b")
	q.wait("interrupted")
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case <-cancelled:
	case <-timer.C:
		t.Fatal("provider did not observe cancellation")
	}
	q.prompt()
	q.requireResizeState(t, networkDraftText, resizeLedgerSentinel, networkStableMarker, networkActiveMarker)
	if got := requests.Load(); got != 1 {
		t.Fatalf("cancellation used %d provider requests, want 1", got)
	}
	q.write("\x15/quit\r")
	q.finish()
}

func (q *reviewPTY) signalResize(t *testing.T) {
	t.Helper()
	if err := q.cmd.Process.Signal(syscall.SIGWINCH); err != nil {
		t.Fatalf("send SIGWINCH: %v", err)
	}
}

// Return a byte offset so callers include any later readiness output in their
// clear count. A busy run cannot use q.prompt(), which needs a visible cursor.
func (q *reviewPTY) resizeTo(t *testing.T, width, height int) int {
	t.Helper()
	q.pending = ""
	mark := q.raw.Len()
	if err := pty.Setsize(q.master, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)}); err != nil {
		t.Fatalf("resize PTY to %dx%d: %v", width, height, err)
	}
	q.screen.Resize(width, height)
	q.wait(fullClearSequence)
	q.observeWindow()
	return mark
}

func (q *reviewPTY) requireSingleClear(t *testing.T, rebuilt string) {
	t.Helper()
	if strings.Count(rebuilt, "\x1b[2J") != 1 || strings.Count(rebuilt, "\x1b[3J") != 1 {
		t.Fatalf("real resize must clear viewport and scrollback exactly once: %q", rebuilt)
	}
}

func cleared(window string) bool {
	return strings.Contains(window, "\x1b[2J") || strings.Contains(window, "\x1b[3J")
}

// Keep the single-consumer contract of reviewPTY: only the test goroutine
// consumes chunks and updates the raw log and VT model.
func (q *reviewPTY) observeWindow() {
	q.t.Helper()
	timer := time.NewTimer(resizeObservationWindow)
	defer timer.Stop()
	for {
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				q.t.Fatalf("process exited during the resize observation window: %q", q.raw.String())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timer.C:
			return
		}
	}
}

func (q *reviewPTY) requireResizeState(t *testing.T, draft string, markers ...string) {
	t.Helper()
	// Count retained text in screen plus scrollback, not the historical byte
	// log: a valid real resize re-emits it. Join words split by terminal wrap.
	screen := strings.Join(strings.Fields(q.screen.Text()), "")
	visible := strings.Join(strings.Fields(strings.Join(q.screen.Scrollback, "\n")), "") + screen
	for _, marker := range markers {
		if count := strings.Count(visible, marker); count != 1 {
			t.Fatalf("retained marker %q occurred %d times, want 1: %q", marker, count, visible)
		}
	}
	if !strings.Contains(screen, draft) {
		t.Fatalf("draft is missing from the screen (scrollback does not count): %q", q.screen.Text())
	}
}

func requirePendingCancellation(t *testing.T, cancelled <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-cancelled:
		t.Fatal(what)
	default:
	}
}
