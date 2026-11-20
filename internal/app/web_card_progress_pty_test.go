//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/hypernewbie/psi/internal/session"
)

// webCardResize changes both sides of the PTY regression: the kernel viewport
// and the VT model that interprets the app's frames.
func webCardResize(t *testing.T, q *reviewPTY, width, height int) {
	t.Helper()
	if err := pty.Setsize(q.master, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)}); err != nil {
		t.Fatal(err)
	}
	q.screen.Resize(width, height)
}

// longWebPage returns a page with a distinct first line and enough body text
// that the expanded card adds rows the collapsed card does not show.
func longWebPage(title, first string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><title>` + title + `</title></head><body><p>` + first + `</p>`)
	for i := 0; i < 8; i++ {
		b.WriteString(`<p>BODY LINE ` + fmt.Sprint(i) + ` with enough text to wrap at narrow widths</p>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

func TestWebCardPTYProgressAndExpansion(t *testing.T) {
	t.Run("released progress resize and expansion", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		const queryToken = "BATCH_QUERY_CANARY"
		var sawQuery atomic.Bool
		writeAppFile(t, filepath.Join(work, "sibling.txt"), "SIBLING_OK")

		holdRelease := make(chan struct{})
		var holdOnce sync.Once
		releaseHold := func() { holdOnce.Do(func() { close(holdRelease) }) }
		var pageRequests atomic.Int32
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pageRequests.Add(1)
			switch r.URL.Path {
			case "/hold":
				select {
				case <-holdRelease:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				io.WriteString(w, longWebPage("Held Page", "HELD PAGE BODY"))
			case "/ok":
				sawQuery.Store(r.URL.Query().Get("token") == queryToken)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				io.WriteString(w, longWebPage("OK Page", "OK PAGE FIRST LINE"))
			default:
				http.NotFound(w, r)
			}
		}))
		defer func() {
			releaseHold()
			page.Close()
		}()
		urls := []string{page.URL + "/hold", page.URL + "/ok?token=" + queryToken}
		args, err := json.Marshal(map[string]any{"urls": urls})
		if err != nil {
			t.Fatal(err)
		}
		provider := newWebCardProvider(t, []webCardTurn{
			{
				calls: []webCardCall{
					{id: "sibling-call", name: "read", args: `{"path":"sibling.txt"}`},
					{id: "web-call", name: "fetch_content", args: string(args)},
				},
			},
			{text: "RELEASE_DONE"},
		})
		webCardHome(t, agentDir, provider)
		store := webCardSession(t, home, work, agentDir)
		skillBlock := replayedSkillText("webexp", "SKILL_BODY_CANARY", "skill-args")
		if _, err := store.AppendMessage(agentMessageUser(skillBlock)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
			Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop",
			Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "AFTER_SKILL"}}},
		}}); err != nil {
			t.Fatal(err)
		}

		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(),
			"--tools", "read,fetch_content,get_search_content")
		q.prompt()
		q.write("GO\r")
		// One URL completes while the other is still held: the bar is half
		// filled and the web call owns the live fragment (it is last).
		q.waitScreen("[█████░░░░░]")
		if !strings.Contains(q.screen.Text(), "fetch 2 URLs") {
			t.Fatalf("web card does not own the live fragment: %q", q.screen.Text())
		}

		q.write("DRAFT_TEXT")
		q.waitScreen("DRAFT_TEXT")
		draftRows := q.screen.Text()
		if !strings.Contains(draftRows, "DRAFT_TEXT") {
			t.Fatalf("draft missing before resize: %q", draftRows)
		}
		webCardResize(t, q, 40, 6)
		q.waitScreen("DRAFT_TEXT")
		resized := q.screen.Text()
		if !strings.Contains(resized, "fetch 2 URLs") {
			t.Fatalf("card title lost at 40x6: %q", resized)
		}
		if strings.Contains(resized, "fetching URLs") {
			t.Fatalf("progress row survived the one-row budget: %q", resized)
		}
		webCardResize(t, q, 80, 40)
		q.waitScreen("[█████░░░░░]")

		releaseHold()
		q.wait("RELEASE_DONE")
		q.prompt()
		rows := webCardRows(q)
		joined := strings.Join(rows, "\n")
		if !strings.Contains(joined, "2/2 URLs (content stored)") {
			t.Fatalf("released card lacks the settled counts: %#v", rows)
		}
		if strings.Contains(joined, "fetching URLs") || strings.Contains(joined, "█████") {
			t.Fatalf("stale progress row after settlement: %#v", rows)
		}
		if !strings.Contains(q.screen.Text(), "DRAFT_TEXT") {
			t.Fatalf("draft lost during the run: %q", q.screen.Text())
		}
		sibling := rowIndex(t, rows, "sibling.txt")
		web := rowIndex(t, rows, "fetch 2 URLs")
		if sibling < 0 || web < 0 || sibling > web {
			t.Fatalf("assistant-order settlement broken (sibling=%d web=%d): %#v", sibling, web, rows)
		}
		// Every drawn progress row must be monotone. The final 2/2 snapshot is
		// optional: the coordinator may settle the completion before drawing it,
		// and the settled card is the source of truth.
		counts := webCardProgressCounts(q.raw.String())
		if len(counts) == 0 {
			t.Fatalf("no progress rows were drawn")
		}
		for i := 1; i < len(counts); i++ {
			if counts[i] < counts[i-1] {
				t.Fatalf("progress regressed: %v", counts)
			}
		}
		if provider.requests() != 2 || pageRequests.Load() != 2 {
			t.Fatalf("unexpected requests before expansion: provider=%d page=%d", provider.requests(), pageRequests.Load())
		}

		if !sawQuery.Load() {
			t.Fatal("page server did not receive the original query")
		}

		// Expansion renders from persisted metadata only: no new requests.
		q.write("\x0f")
		q.waitScreen("SKILL_BODY_CANARY")
		q.waitScreen("OK PAGE FIRST LINE")
		q.waitScreen("DRAFT_TEXT")
		expanded := q.screen.Text()
		if !strings.Contains(expanded, "DRAFT_TEXT") {
			t.Fatalf("expansion changed the draft: %q", expanded)
		}
		const safePreview = "/ok: OK PAGE FIRST LINE"
		if !strings.Contains(expanded, safePreview) || strings.Contains(expanded, queryToken) {
			t.Fatalf("expanded card did not strip the URL query: %q", expanded)
		}
		if metadata := string(webCardPsiWeb(t, store.File())); !strings.Contains(metadata, safePreview) || strings.Contains(metadata, queryToken) {
			t.Fatal("saved web card should contain the preview without the URL query")
		}
		if provider.requests() != 2 || pageRequests.Load() != 2 {
			t.Fatalf("expansion issued requests: provider=%d page=%d", provider.requests(), pageRequests.Load())
		}
		q.write("\x0f")
		q.waitScreenGone("OK PAGE FIRST LINE")
		q.waitScreenGone("SKILL_BODY_CANARY")
		q.waitScreen("DRAFT_TEXT")
		if !strings.Contains(q.screen.Text(), "DRAFT_TEXT") {
			t.Fatalf("collapse lost the draft: %q", q.screen.Text())
		}
		// The draft owns the editor, so clear it before submitting /quit.
		q.write(strings.Repeat("\x7f", len("DRAFT_TEXT")))
		q.write("/quit\r")
		q.finish()

		// A fresh process starts collapsed and expands the same bounded
		// metadata again.
		replay := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(),
			"--tools", "read,fetch_content,get_search_content")
		replay.prompt()
		if strings.Contains(replay.screen.Text(), "OK PAGE FIRST LINE") {
			t.Fatalf("replay started expanded: %q", replay.screen.Text())
		}
		replay.write("\x0f")
		replay.waitScreen("SKILL_BODY_CANARY")
		replay.waitScreen(safePreview)
		if strings.Contains(replay.screen.Text(), queryToken) {
			t.Fatal("replayed card exposed the URL query")
		}
		replay.write("\x0f")
		replay.waitScreenGone("OK PAGE FIRST LINE")
		replay.write("/quit\r")
		replay.finish()
		if provider.requests() != 2 || pageRequests.Load() != 2 {
			t.Fatalf("replay expansion issued requests: provider=%d page=%d", provider.requests(), pageRequests.Load())
		}
	})

	t.Run("cancelled batch", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		holdRelease := make(chan struct{})
		var holdOnce sync.Once
		releaseHold := func() { holdOnce.Do(func() { close(holdRelease) }) }
		var pageRequests atomic.Int32
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pageRequests.Add(1)
			switch r.URL.Path {
			case "/hold":
				select {
				case <-holdRelease:
				case <-r.Context().Done():
					return
				}
				io.WriteString(w, "held")
			case "/404":
				http.NotFound(w, r)
			}
		}))
		defer func() {
			releaseHold()
			page.Close()
		}()
		urls := []string{page.URL + "/hold", page.URL + "/404"}
		args, err := json.Marshal(map[string]any{"urls": urls})
		if err != nil {
			t.Fatal(err)
		}
		provider := newWebCardProvider(t, []webCardTurn{
			{toolName: "fetch_content", args: string(args)},
			{text: "NEXT_REPLY"},
		})
		webCardHome(t, agentDir, provider)
		store := webCardSession(t, home, work, agentDir)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(),
			"--tools", "fetch_content,get_search_content")
		q.prompt()
		q.write("CANCEL_FETCH\r")
		q.waitScreen("[█████░░░░░]")
		q.write("\x03")
		q.waitScreen("Cancelled")
		q.prompt()
		rows := webCardRows(q)
		joined := strings.Join(rows, "\n")
		if strings.Contains(joined, "URLs (content") || strings.Contains(joined, "/2 URLs") {
			t.Fatalf("cancelled card invented success counts: %#v", rows)
		}
		if strings.Contains(joined, "█████") {
			t.Fatalf("cancelled card kept a progress bar: %#v", rows)
		}
		q.write("NEXT\r")
		q.wait("NEXT_REPLY")
		q.prompt()
		if strings.Contains(q.screen.Text(), "█████") {
			t.Fatalf("stale progress row leaked into the next turn: %q", q.screen.Text())
		}
		q.write("/quit\r")
		q.finish()
		if got := pageRequests.Load(); got != 2 {
			t.Fatalf("cancel retried a page request: %d", got)
		}
	})

	t.Run("broken body fails once", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		var pageRequests atomic.Int32
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pageRequests.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// The declared length exceeds the short body: the client sees an
			// unexpected EOF, a deterministic transport failure.
			w.Header().Set("Content-Length", "128")
			io.WriteString(w, "<p>short")
		}))
		defer page.Close()
		args, err := json.Marshal(map[string]any{"url": page.URL + "/broken"})
		if err != nil {
			t.Fatal(err)
		}
		provider := newWebCardProvider(t, []webCardTurn{
			{toolName: "fetch_content", args: string(args)},
			{text: "BROKEN_DONE"},
		})
		webCardHome(t, agentDir, provider)
		store := webCardSession(t, home, work, agentDir)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(),
			"--tools", "fetch_content")
		q.prompt()
		q.write("BROKEN_FETCH\r")
		q.wait("BROKEN_DONE")
		q.prompt()
		rows := webCardRows(q)
		if !containsRow(rows, "Error: request failed") {
			t.Fatalf("broken body did not settle as an error: %#v", rows)
		}
		for _, row := range rows {
			if strings.Contains(row, "chars)") || strings.Contains(row, "Untitled") {
				t.Fatalf("failed fetch rendered a success card: %#v", rows)
			}
		}
		if got := pageRequests.Load(); got != 1 {
			t.Fatalf("broken body was retried: %d requests", got)
		}
		q.write("/quit\r")
		q.finish()
	})
}

// waitScreenGone waits until the visible VT screen no longer contains want.
func (q *reviewPTY) waitScreenGone(want string) {
	q.t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if !strings.Contains(q.screen.Text(), want) {
			return
		}
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				q.t.Fatalf("process exited waiting for screen without %q: %q", want, q.screen.Text())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timer.C:
			q.t.Fatalf("timeout waiting for screen without %q: %q", want, q.screen.Text())
		}
	}
}

// webCardProgressCounts extracts the completed-attempt counts from every
// progress row drawn in the raw stream. Duplicate redraws are expected.
func webCardProgressCounts(raw string) []int {
	re := regexp.MustCompile(`\[[█░]{10}\] fetching URLs ([0-9]+)/2`)
	var out []int
	for _, match := range re.FindAllStringSubmatch(raw, -1) {
		value, err := strconv.Atoi(match[1])
		if err == nil {
			out = append(out, value)
		}
	}
	return out
}
