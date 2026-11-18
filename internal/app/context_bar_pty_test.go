//go:build unix

package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"

	"psi/internal/session"
	"psi/internal/terminal"
)

// contextBarTrackCells returns the track width between the bar brackets.
func contextBarTrackCells(line string) (int, bool) {
	open := strings.IndexByte(line, '[')
	if open < 0 {
		return 0, false
	}
	relativeClose := strings.IndexByte(line[open:], ']')
	if relativeClose < 0 {
		return 0, false
	}
	return terminal.CellWidth(line[open+1 : open+relativeClose]), true
}

// TestActualAppContextBarAdaptsToWidth proves the built app redraws the
// width-adaptive footer from a resumed session without asking the provider.
// The loopback endpoint answers any request with an immediate client error so
// a regression cannot sit in provider retry delays.
func TestActualAppContextBarAdaptsToWidth(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	var providerRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		http.Error(w, "unexpected provider request", http.StatusBadRequest)
	}))
	t.Cleanup(func() {
		// Close first so active handlers finish before the count is final.
		server.Close()
		if got := providerRequests.Load(); got != 0 {
			t.Errorf("provider requests=%d, want 0", got)
		}
	})

	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","api":"anthropic-messages","maxTokens":16384,"contextWindow":1000000,"reasoning":true}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-bar-key"}}`)

	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seeded question")); err != nil {
		t.Fatal(err)
	}
	assistant := session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop",
		Usage:   session.Usage{Input: 57000, TotalTokens: 57000},
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "seeded answer"}}},
	}}
	if _, err := store.AppendMessage(assistant); err != nil {
		t.Fatal(err)
	}

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--thinking", "high", "--session", store.File())

	const (
		usagePrefix = "↑57k "
		caption     = "5.7%/1.0M (auto)"
		fallback    = "↑57k 5.7%/1.0M (auto)"
		rightSuffix = "m • high"
		noBarTrack  = -1 // Text-only states have no track.
	)
	statsRow := func() string {
		rows := strings.Split(q.screen.Text(), "\n")
		// The bottom editor border is below the cursor and ends one cell
		// before the terminal edge. The folder and stats rows follow it.
		for i := len(rows) - 3; i > q.screen.CursorRow; i-- {
			if terminal.CellWidth(rows[i]) == q.screen.Width-1 && strings.Trim(rows[i], "─") == "" {
				return rows[i+2]
			}
		}
		return ""
	}
	footerReady := func(width, height, wantTrack int) bool {
		// Terminal mode 2026 stays set until the synchronized redraw ends.
		if q.screen.Width != width || q.screen.Height != height || !semanticEditorReady(q.screen) || q.screen.Modes["?2026"] {
			return false
		}
		stats := statsRow()
		right := "(anthropic) " + rightSuffix
		if width == 40 {
			right = rightSuffix
		}
		if !strings.HasSuffix(stats, right) || terminal.CellWidth(stats) != width-1 {
			return false
		}
		captionAt := strings.Index(stats, caption)
		if captionAt < 0 {
			return false
		}
		leftEnd := captionAt + len(caption)
		left := stats[:leftEnd]
		gap := strings.TrimSuffix(stats[leftEnd:], right)
		if terminal.CellWidth(gap) < 2 || strings.Trim(gap, " ") != "" {
			return false
		}
		if wantTrack < 0 {
			return !strings.ContainsAny(stats, "[]") && left == fallback
		}
		track, ok := contextBarTrackCells(stats)
		// Compared with text-only stats, the bar adds its track, two
		// brackets, and one extra separator.
		wantLeftWidth := terminal.CellWidth(fallback) + wantTrack + 3
		return ok && track == wantTrack && strings.HasPrefix(left, usagePrefix+"[") &&
			strings.HasSuffix(left, "] "+caption) && terminal.CellWidth(left) == wantLeftWidth && wantLeftWidth <= width/2
	}
	// A negative erase mark permits the initial paint without a resize.
	waitFooter := func(width, height, wantTrack, eraseMark int) {
		t.Helper()
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		for {
			// Resize retains old rows. Require this resize's scrollback erase
			// as well as a completed frame and the expected current footer.
			rebuilt := eraseMark < 0 || slices.Contains(q.screen.EraseOps[eraseMark:], "ED3")
			if rebuilt && footerReady(width, height, wantTrack) {
				return
			}
			select {
			case chunk, ok := <-q.chunks:
				if !ok {
					t.Fatalf("process exited at %dx%d: %q", width, height, q.screen.Text())
				}
				q.pending += string(chunk)
				q.raw.Write(chunk)
				q.screen.Feed(chunk)
			case <-deadline.C:
				t.Fatalf("timeout at %dx%d: %q", width, height, q.screen.Text())
			}
		}
	}

	// The startup footer establishes the seeded compact estimate.
	waitFooter(80, 40, 16, -1)

	for _, state := range []struct {
		width     int
		height    int
		wantTrack int
	}{
		{80, 12, 16},
		{70, 12, 11},
		{60, 12, 6},
		{56, 12, 4},
		{55, 12, noBarTrack},
		{40, 12, noBarTrack},
		{80, 12, 16},
		// Growing only the height retains a valid old footer in the VT model.
		// It must not satisfy the wait before the app redraws that footer.
		{80, 40, 16},
	} {
		height := state.height
		writesBefore := q.screen.WriteCalls
		eraseMark := len(q.screen.EraseOps)
		if err := pty.Setsize(q.master, &pty.Winsize{Rows: uint16(height), Cols: uint16(state.width)}); err != nil {
			t.Fatal(err)
		}
		q.screen.Resize(state.width, height)
		waitFooter(state.width, height, state.wantTrack, eraseMark)
		if q.screen.WriteCalls <= writesBefore {
			t.Fatalf("%dx%d: resize produced no output", state.width, height)
		}
		t.Logf("footer %dx%d: %q", state.width, height, statsRow())
	}

	q.write("/quit\r")
	q.finish()
}
