//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"psi/internal/session"
)

// Fixture text for the web card PTY regressions. The title is distinct so the
// result row cannot be confused with the page body. "BODY OK 😀" is 10 UTF-16
// units (the emoji counts as two), the unit Pi uses for its "N chars" label.
// The literal avoids Markdown-escaped characters, so the expected count comes
// from the fixture text and not from a converter detail.
const (
	webCardPageTitle = "Fixture Page"
	webCardPageText  = "BODY OK 😀"
)

// webCardProvider is a synthetic Anthropic SSE endpoint. Each request consumes
// one turn: a tool call or a final text reply.
type webCardProvider struct {
	t      *testing.T
	next   func(index int, body map[string]any) webCardTurn
	server *httptest.Server
	count  atomic.Int32
	mu     sync.Mutex
	bodies []map[string]any
}

type webCardCall struct {
	id   string
	name string
	args string
}

type webCardTurn struct {
	toolName string
	args     string
	text     string
	calls    []webCardCall
}

func newWebCardProvider(t *testing.T, turns []webCardTurn) *webCardProvider {
	t.Helper()
	return newWebCardProviderFunc(t, func(index int, _ map[string]any) webCardTurn {
		if index < len(turns) {
			return turns[index]
		}
		return webCardTurn{text: "UNEXPECTED_TURN"}
	})
}

func newWebCardProviderFunc(t *testing.T, next func(index int, body map[string]any) webCardTurn) *webCardProvider {
	t.Helper()
	p := &webCardProvider{t: t, next: next}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("provider body: %v", err)
			return
		}
		p.mu.Lock()
		p.bodies = append(p.bodies, body)
		p.mu.Unlock()
		index := int(p.count.Add(1)) - 1
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		emit("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("web-reply-%d", index), "model": "m", "usage": map[string]any{"input_tokens": 1}}})
		turn := p.next(index, body)
		if turn.toolName != "" || len(turn.calls) > 0 {
			calls := turn.calls
			if len(calls) == 0 {
				calls = []webCardCall{{id: fmt.Sprintf("call-%d", index+1), name: turn.toolName, args: turn.args}}
			}
			for i, call := range calls {
				emit("content_block_start", map[string]any{"index": i, "content_block": map[string]any{"type": "tool_use", "id": call.id, "name": call.name}})
				emit("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": call.args}})
				emit("content_block_stop", map[string]any{"index": i})
			}
			emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 2}})
		} else {
			emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": turn.text}})
			emit("content_block_stop", map[string]any{"index": 0})
			emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}})
		}
		emit("message_stop", map[string]any{})
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *webCardProvider) requests() int { return int(p.count.Load()) }

func (p *webCardProvider) body(index int) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index < 0 || index >= len(p.bodies) {
		return nil
	}
	return p.bodies[index]
}

// heldWebPage serves one HTML page and holds the response until release. The
// shutdown channel lets cleanup unblock a held handler without sleeping.
type heldWebPage struct {
	server    *httptest.Server
	arrived   chan struct{}
	release   chan struct{}
	releaseO  sync.Once
	requests  atomic.Int32
	body      string
	contentTy string
}

func newHeldWebPage(t *testing.T, body string) *heldWebPage {
	t.Helper()
	page := &heldWebPage{
		arrived:   make(chan struct{}, 4),
		release:   make(chan struct{}),
		body:      body,
		contentTy: "text/html; charset=utf-8",
	}
	page.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page.requests.Add(1)
		select {
		case page.arrived <- struct{}{}:
		default:
		}
		select {
		case <-page.release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", page.contentTy)
		io.WriteString(w, page.body)
	}))
	t.Cleanup(page.close)
	return page
}

func (p *heldWebPage) close() {
	// Closing a held handler races the server's own shutdown; release first so
	// Close never waits on a handler that is still blocked.
	p.letGo()
	p.server.Close()
}

func (p *heldWebPage) url(path string) string { return p.server.URL + path }

// waitArrived blocks until the page handler received one request. Every
// parent-side wait is bounded so a broken fixture fails instead of hanging.
func (p *heldWebPage) waitArrived(t *testing.T) {
	t.Helper()
	select {
	case <-p.arrived:
	case <-time.After(8 * time.Second):
		t.Fatal("page request did not arrive")
	}
}

func (p *heldWebPage) letGo() {
	p.releaseO.Do(func() { close(p.release) })
}

// webCardHome prepares an isolated agent home with synthetic provider, model,
// auth, and web-access configuration. The page server must be loopback.
func webCardHome(t *testing.T, agentDir string, provider *webCardProvider) {
	t.Helper()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+provider.server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-web-key"}}`)
	writeAppFile(t, filepath.Join(agentDir, "web-search.json"), `{"ssrf":{"allowRanges":["127.0.0.1/32"]}}`)
}

// webCardRows returns scrollback plus the current screen as plain rows.
func webCardRows(q *reviewPTY) []string {
	return append(append([]string{}, q.screen.Scrollback...), strings.Split(q.screen.Text(), "\n")...)
}

func containsRow(rows []string, want string) bool {
	for _, row := range rows {
		if strings.Contains(row, want) {
			return true
		}
	}
	return false
}

// rowIndex returns the index of the one row containing want, or -1. Two
// occurrences fail the test because the transcript must not duplicate a card.
func rowIndex(t *testing.T, rows []string, want string) int {
	t.Helper()
	found := -1
	for i, row := range rows {
		if !strings.Contains(row, want) {
			continue
		}
		if found >= 0 {
			t.Fatalf("row %q appears twice: %#v", want, rows)
		}
		found = i
	}
	return found
}

func TestWebCardPTYRawFetchPreview(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	prefix := "<!doctype html>\n\n  \t<script>😀\n"
	at200 := prefix + strings.Repeat("x", 200-len(utf16.Encode([]rune(prefix)))-len(" CUT200")) + " CUT200"
	middle := "-MORE\n"
	at500 := at200 + middle + strings.Repeat("y", 300-len(middle)-len(" CUT500")) + " CUT500"
	page := newHeldWebPage(t, at500+"-HIDDEN</script>")
	page.letGo()
	provider := newWebCardProvider(t, []webCardTurn{
		{toolName: "fetch_content", args: fmt.Sprintf(`{"url":%q,"mode":"raw"}`, page.url("/raw"))},
		{text: "RAW_FETCH_DONE"},
	})
	webCardHome(t, agentDir, provider)
	store := webCardSession(t, home, work, agentDir)
	start := func() *reviewPTY {
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(), "--tools", "fetch_content")
		q.prompt()
		return q
	}
	check := func(q *reviewPTY, expanded bool) {
		t.Helper()
		rows := webCardRows(q)
		want, hidden := "CUT200...", "CUT500"
		if expanded {
			want, hidden = "CUT500...", "CUT200..."
		}
		if !containsRow(rows, want) || containsRow(rows, hidden) || containsRow(rows, "-HIDDEN") {
			t.Fatalf("expanded=%v: expected the preview to end at %q: %#v", expanded, want, rows)
		}
		start := rowIndex(t, rows, "<!doctype html>")
		if start < 0 || start+2 >= len(rows) || strings.TrimSpace(rows[start+1]) != "" {
			t.Fatalf("preview lost its blank line: %#v", rows)
		}
		// Pi expands the tab to three spaces after it counts it as one
		// UTF-16 unit. Compare against the first row's card padding.
		padding := strings.TrimSuffix(rows[start], "<!doctype html>")
		if want := padding + "     <script>😀"; rows[start+2] != want {
			t.Fatalf("indented preview row = %q, want %q", rows[start+2], want)
		}
	}

	q := start()
	q.write("FETCH_RAW\r")
	q.wait("RAW_FETCH_DONE")
	q.prompt()
	check(q, false)
	q.write("\x0f")
	q.wait("Skill and web cards: expanded")
	check(q, true)
	q.write("\x0f")
	q.wait("Skill and web cards: collapsed")
	check(q, false)
	q.write("/quit\r")
	q.finish()

	replay := start()
	check(replay, false)
	replay.write("\x0f")
	replay.wait("Skill and web cards: expanded")
	check(replay, true)
	replay.write("/quit\r")
	replay.finish()
	if page.requests.Load() != 1 || provider.requests() != 2 {
		t.Fatalf("unexpected requests: page=%d provider=%d", page.requests.Load(), provider.requests())
	}
}

func TestWebCardPTYFetchMVP(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	page := newHeldWebPage(t, `<!doctype html><html><head><title>`+webCardPageTitle+`</title></head><body><p>`+webCardPageText+`</p></body></html>`)
	target := page.url("/p")
	provider := newWebCardProvider(t, []webCardTurn{
		{toolName: "fetch_content", args: fmt.Sprintf(`{"url":%q}`, target)},
		{text: "FETCH_DONE"},
	})
	webCardHome(t, agentDir, provider)

	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("PREVIOUS_QUERY")); err != nil {
		t.Fatal(err)
	}

	start := func() *reviewPTY {
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(), "--tools", "fetch_content")
		q.prompt()
		return q
	}
	q := start()
	q.write("ASK_FETCH\r")
	page.waitArrived(t)
	q.waitScreen("fetch " + target)

	pending := page.requests.Load()
	if pending == 0 {
		t.Fatal("page request was not counted")
	}
	// While the page is held, the transcript shows one pending call and no
	// result. A raw ANSI substring would match the in-flight request instead.
	live := strings.Join(webCardRows(q), "\n")
	if strings.Contains(live, webCardPageTitle) || strings.Contains(live, webCardPageText) || strings.Contains(live, " chars)") {
		t.Fatalf("pending card already shows result data: %q", live)
	}
	if strings.Contains(live, "summary unavailable") {
		t.Fatalf("pending web row kept the fallback summary: %q", live)
	}
	t.Logf("pending card rows: %q", webCardRows(q))

	page.letGo()
	q.wait("FETCH_DONE")
	q.prompt()
	rows := webCardRows(q)
	callRow := rowIndex(t, rows, "fetch "+target)
	if callRow < 0 {
		t.Fatalf("missing settled call row: %#v", rows)
	}
	if got, want := strings.TrimSpace(rows[callRow]), "fetch "+target; got != want {
		t.Fatalf("settled call row = %q, want %q", got, want)
	}
	if callRow+2 >= len(rows) {
		t.Fatalf("settled card is too short: %#v", rows[callRow:])
	}
	wantChars := len(utf16.Encode([]rune(webCardPageText)))
	titleRow := strings.TrimSpace(rows[callRow+1])
	wantTitle := fmt.Sprintf("%s (%d chars)", webCardPageTitle, wantChars)
	if !strings.HasPrefix(titleRow, wantTitle) || !strings.HasSuffix(titleRow, " total") {
		t.Fatalf("result title row = %q, want %q plus a duration suffix", titleRow, wantTitle)
	}
	// The duration is measured, so the replay must reuse the persisted value
	// instead of formatting a new one.
	settledCall, settledTitle, settledPreview := strings.TrimSpace(rows[callRow]), titleRow, strings.TrimSpace(rows[callRow+2])
	if preview := strings.TrimSpace(rows[callRow+2]); preview != webCardPageText {
		t.Fatalf("preview row = %q, want %q", preview, webCardPageText)
	}
	t.Logf("settled card rows: %q", append([]string{}, rows[callRow:callRow+3]...))
	for _, row := range rows {
		if strings.Contains(row, "summary unavailable") {
			t.Fatalf("settled transcript kept the fallback summary: %#v", rows)
		}
	}
	if want := int32(1); page.requests.Load() != want {
		t.Fatalf("page requests = %d, want %d", page.requests.Load(), want)
	}
	if provider.requests() != 2 {
		t.Fatalf("provider requests = %d, want 2", provider.requests())
	}
	// The next model request receives the normal bounded tool result, never
	// the display metadata.
	second, _ := json.Marshal(provider.body(1))
	if !strings.Contains(string(second), webCardPageText) {
		t.Fatalf("tool result missing from the follow-up request: %s", second)
	}
	for _, forbidden := range []string{"psiWeb", "10 chars", "Fixture Page ("} {
		if strings.Contains(string(second), forbidden) {
			t.Fatalf("display metadata leaked into the model request: %q in %s", forbidden, second)
		}
	}

	q.write("/quit\r")
	q.finish()

	replay := start()
	replayRows := webCardRows(replay)
	replayCall := rowIndex(t, replayRows, "fetch "+target)
	if replayCall < 0 || replayCall+2 >= len(replayRows) {
		t.Fatalf("replayed card missing: %#v", replayRows)
	}
	for offset, want := range []string{settledCall, settledTitle, settledPreview} {
		if got := strings.TrimSpace(replayRows[replayCall+offset]); got != want {
			t.Fatalf("replay row %d = %q, want %q", offset, got, want)
		}
	}
	t.Logf("replayed card rows: %q", append([]string{}, replayRows[replayCall:replayCall+3]...))
	replay.write("/quit\r")
	replay.finish()
	if want := int32(1); page.requests.Load() != want {
		t.Fatalf("replay fetched the page again: %d requests", page.requests.Load())
	}
	if provider.requests() != 2 {
		t.Fatalf("replay sent provider requests: %d", provider.requests())
	}
}
