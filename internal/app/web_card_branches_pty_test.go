//go:build unix

package app

import (
	"bytes"
	"encoding/json"
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

	"github.com/hypernewbie/psi/internal/session"
)

// batchWebPage serves the ordered batch URLs. The held response proves the
// card settles only after every worker finished, including one that started
// later than the held request.
type batchWebPage struct {
	server   *httptest.Server
	gate     chan struct{}
	gateOnce sync.Once
	ok1      chan struct{}
	ok1Once  sync.Once
	requests atomic.Int32
}

func newBatchWebPage(t *testing.T) *batchWebPage {
	t.Helper()
	page := &batchWebPage{gate: make(chan struct{}), ok1: make(chan struct{})}
	page.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page.requests.Add(1)
		switch r.URL.Path {
		case "/hold":
			select {
			case <-page.gate:
			case <-r.Context().Done():
				return
			}
			io.WriteString(w, "HELD PAGE")
		case "/404":
			http.NotFound(w, r)
		case "/ok1":
			io.WriteString(w, "OK PAGE ONE")
			page.ok1Once.Do(func() { close(page.ok1) })
		default:
			io.WriteString(w, "OK PAGE")
		}
	}))
	t.Cleanup(func() {
		page.release()
		page.server.Close()
	})
	return page
}

func (p *batchWebPage) release() {
	p.gateOnce.Do(func() { close(p.gate) })
}

func (p *batchWebPage) waitOK1(t *testing.T) {
	t.Helper()
	select {
	case <-p.ok1:
	case <-time.After(8 * time.Second):
		t.Fatal("a later batch URL never finished")
	}
}

func TestWebCardPTYWebBranches(t *testing.T) {
	t.Run("privacy across the display boundary", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		const (
			secret     = "SYNTHETIC_SECRET_VALUE_42"
			queryToken = "QUERY_TOKEN_CANARY"
		)
		var receivedQuery atomic.Value
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedQuery.Store(r.URL.RawQuery)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<!doctype html><html><head><title>Private Page</title></head><body><p>`+strings.Repeat("body ", 120)+secret+`</p></body></html>`)
		}))
		defer page.Close()
		// The secret sits past a 180-byte path prefix and inside the URL's
		// 256-byte display clip, so clipping before redaction would expose it.
		target := page.URL + "/" + strings.Repeat("a", 180) + secret + "/tail?token=" + queryToken
		args, err := json.Marshal(map[string]any{"url": target})
		if err != nil {
			t.Fatal(err)
		}
		provider := newWebCardProvider(t, []webCardTurn{
			{toolName: "fetch_content", args: string(args)},
			{text: "PRIVACY_DONE"},
		})
		webCardHome(t, agentDir, provider)
		writeAppFile(t, filepath.Join(agentDir, "web-search.json"),
			`{"exaApiKey":"`+secret+`","ssrf":{"allowRanges":["127.0.0.1/32"]}}`)
		store := webCardSession(t, home, work, agentDir)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(), "--tools", "fetch_content")
		q.prompt()
		q.write("PRIVACY_FETCH\r")
		q.wait("PRIVACY_DONE")
		q.prompt()

		if got, _ := receivedQuery.Load().(string); got != "token="+queryToken {
			t.Fatalf("page server received query %q", got)
		}
		rows := webCardRows(q)
		card := strings.Join(rows, "\n")
		secretPrefix := secret[:12]
		for _, forbidden := range []string{secret, secretPrefix, queryToken} {
			if strings.Contains(card, forbidden) {
				t.Fatalf("live card exposed %q: %#v", forbidden, rows)
			}
		}
		if !containsRow(rows, "[redacted]") {
			t.Fatalf("live card did not show the redaction marker: %#v", rows)
		}
		q.write("/quit\r")
		q.finish()

		replay := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(), "--tools", "fetch_content")
		replay.prompt()
		replayRows := webCardRows(replay)
		replayed := strings.Join(replayRows, "\n")
		for _, forbidden := range []string{secret, secretPrefix, queryToken} {
			if strings.Contains(replayed, forbidden) {
				t.Fatalf("replay exposed %q: %#v", forbidden, replayRows)
			}
		}
		replay.write("/quit\r")
		replay.finish()

		metadata := webCardPsiWeb(t, store.File())
		if len(metadata) == 0 {
			t.Fatal("session has no psiWeb metadata")
		}
		if bytes.Contains(metadata, []byte(secret)) || bytes.Contains(metadata, []byte(secretPrefix)) || bytes.Contains(metadata, []byte(queryToken)) {
			t.Fatalf("psiWeb metadata exposed a display secret: %s", metadata)
		}
	})

	t.Run("batch fetch with stored content", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		page := newBatchWebPage(t)
		urls := []string{
			page.server.URL + "/hold",
			page.server.URL + "/404",
			page.server.URL + "/ok1",
			page.server.URL + "/ok2",
			page.server.URL + "/ok3",
			page.server.URL + "/ok4",
		}
		args, err := json.Marshal(map[string]any{"urls": urls, "mode": "raw"})
		if err != nil {
			t.Fatal(err)
		}
		provider := newWebCardProvider(t, []webCardTurn{
			{toolName: "fetch_content", args: string(args)},
			{text: "BATCH_DONE"},
		})
		webCardHome(t, agentDir, provider)
		store := webCardSession(t, home, work, agentDir)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(),
			"--tools", "fetch_content,get_search_content")
		q.prompt()
		q.write("BATCH_FETCH\r")
		page.waitOK1(t)
		page.release()
		q.wait("BATCH_DONE")
		q.prompt()

		rows := webCardRows(q)
		callRow := rowIndex(t, rows, "fetch 6 URLs")
		if callRow < 0 {
			t.Fatalf("missing batch call row: %#v", rows)
		}
		for i, want := range []string{"/hold", "/404", "/ok1", "/ok2", "/ok3"} {
			row := strings.TrimSpace(rows[callRow+1+i])
			if !strings.Contains(row, want) {
				t.Fatalf("batch entry %d = %q, want %q", i, row, want)
			}
		}
		if got := strings.TrimSpace(rows[callRow+6]); got != "... and 1 more" {
			t.Fatalf("overflow row = %q", got)
		}
		if got := strings.TrimSpace(rows[callRow+7]); got != "mode: raw" {
			t.Fatalf("mode row = %q", got)
		}
		if got := strings.TrimSpace(rows[callRow+8]); got != "5/6 URLs (content stored)" {
			t.Fatalf("batch result row = %q", got)
		}
		// No collapsed preview for a batch: the next row is the batch
		// separator, never page text.
		if got := strings.TrimSpace(rows[callRow+9]); got != "" {
			t.Fatalf("batch card gained a preview row: %q", got)
		}
		if got := page.requests.Load(); got != 6 {
			t.Fatalf("page requests = %d, want 6", got)
		}
		q.write("/quit\r")
		q.finish()
	})

	t.Run("batch fetch with cache miss stays fetched", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		page := newBatchWebPage(t)
		page.release()
		urls := []string{page.server.URL + "/ok1", page.server.URL + "/ok2"}
		args, err := json.Marshal(map[string]any{"urls": urls})
		if err != nil {
			t.Fatal(err)
		}
		provider := newWebCardProvider(t, []webCardTurn{
			{toolName: "fetch_content", args: string(args)},
			{text: "CACHE_DONE"},
		})
		webCardHome(t, agentDir, provider)
		// A one-byte cache cannot retain any page entry.
		writeAppFile(t, filepath.Join(agentDir, "web-search.json"), `{"cache":{"maxBytes":1},"ssrf":{"allowRanges":["127.0.0.1/32"]}}`)
		store := webCardSession(t, home, work, agentDir)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(),
			"--tools", "fetch_content,get_search_content")
		q.prompt()
		q.write("CACHE_FETCH\r")
		q.wait("CACHE_DONE")
		q.prompt()

		rows := webCardRows(q)
		callRow := rowIndex(t, rows, "fetch 2 URLs")
		if callRow < 0 {
			t.Fatalf("missing batch call row: %#v", rows)
		}
		if got := strings.TrimSpace(rows[callRow+3]); got != "2/2 URLs (content fetched)" {
			t.Fatalf("cache-miss result row = %q", got)
		}
		for _, row := range rows {
			if strings.Contains(row, "content stored") {
				t.Fatalf("cache-miss card claimed storage: %q", row)
			}
		}
		q.write("/quit\r")
		q.finish()
	})

	t.Run("search and retrieval", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		const (
			hitTitle = "Hit Page"
			hitText  = "ABCDEFGHIJ 😀"
		)
		searchRequests := atomic.Int32{}
		var pageRequests atomic.Int32
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pageRequests.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<!doctype html><html><head><title>`+hitTitle+`</title></head><body><p>`+hitText+`</p></body></html>`)
		}))
		defer page.Close()
		search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			searchRequests.Add(1)
			if r.URL.Path != "/search" {
				http.NotFound(w, r)
				return
			}
			switch r.URL.Query().Get("q") {
			case "fail":
				http.Error(w, "failed", http.StatusInternalServerError)
			case "empty":
				io.WriteString(w, `{"results":[]}`)
			case "hit":
				fmt.Fprintf(w, `{"results":[{"url":%q,"title":%q,"content":"Snippet"}]}`, page.URL+"/hit", hitTitle)
			default:
				http.Error(w, "unexpected query", http.StatusBadRequest)
			}
		}))
		defer search.Close()

		provider := newWebCardProviderFunc(t, func(index int, body map[string]any) webCardTurn {
			switch index {
			case 0:
				return webCardTurn{toolName: "web_search", args: `{"queries":["fail","empty","hit"],"includeContent":true}`}
			case 1:
				id := webCardResponseID(t, body)
				return webCardTurn{toolName: "get_search_content", args: fmt.Sprintf(`{"responseId":%q,"urlIndex":0,"offset":2,"limit":3}`, id)}
			case 2:
				return webCardTurn{toolName: "get_search_content", args: `{"responseId":"missing-response-id","offset":0}`}
			default:
				return webCardTurn{text: "SEARCH_DONE"}
			}
		})
		webCardHome(t, agentDir, provider)
		writeAppFile(t, filepath.Join(agentDir, "web-search.json"),
			fmt.Sprintf(`{"searxngBaseUrl":%q,"ssrf":{"allowRanges":["127.0.0.1/32"]}}`, search.URL))
		store := webCardSession(t, home, work, agentDir)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(),
			"--tools", "web_search,fetch_content,get_search_content")
		q.prompt()
		q.write("SEARCH_IT\r")
		q.wait("SEARCH_DONE")
		q.prompt()

		rows := webCardRows(q)
		searchCall := rowIndex(t, rows, "search 3 queries")
		if searchCall < 0 {
			t.Fatalf("missing search call row: %#v", rows)
		}
		for i, want := range []string{`"fail"`, `"empty"`, `"hit"`} {
			if got := strings.TrimSpace(rows[searchCall+1+i]); got != want {
				t.Fatalf("search entry %d = %q, want %q", i, got, want)
			}
		}
		if got := strings.TrimSpace(rows[searchCall+4]); got != "2/3 queries, 1 sources (content ready)" {
			t.Fatalf("search result row = %q", got)
		}
		if got := strings.TrimSpace(rows[searchCall+5]); got != hitText {
			t.Fatalf("search preview row = %q", got)
		}

		retrievalCall := rowIndex(t, rows, "get_content urlIndex=0")
		if retrievalCall < 0 {
			t.Fatalf("missing retrieval call row: %#v", rows)
		}
		// Retrieval counts and offsets are psi's Unicode scalar units, so
		// the card must agree with paging rather than with UTF-16.
		wantChars := len([]rune(hitText))
		if got, want := strings.TrimSpace(rows[retrievalCall+1]), fmt.Sprintf("%s (%d chars, showing 2-5)", hitTitle, wantChars); got != want {
			t.Fatalf("retrieval result row = %q, want %q", got, want)
		}
		// The displayed range must match the result the model received.
		resultText := webCardLastToolResult(provider.body(2))
		for _, want := range []string{"offset: 2\n", "next-offset: 5\n"} {
			if !strings.Contains(resultText, want) {
				t.Fatalf("retrieval result lacks %q: %q", want, resultText)
			}
		}

		missingCall := rowIndex(t, rows, "get_content missing-")
		if missingCall < 0 {
			t.Fatalf("missing unknown-id call row: %#v", rows)
		}
		if got := strings.TrimSpace(rows[missingCall+1]); got != "Error: request failed" {
			t.Fatalf("unknown-id result row = %q", got)
		}
		if strings.Contains(strings.Join(rows[missingCall:missingCall+3], "\n"), "chars") {
			t.Fatalf("unknown-id card invented counts: %#v", rows[missingCall:missingCall+3])
		}
		if got := searchRequests.Load(); got != 3 {
			t.Fatalf("search requests = %d, want 3", got)
		}
		if got := pageRequests.Load(); got != 1 {
			t.Fatalf("page requests = %d, want 1", got)
		}
		q.write("/quit\r")
		q.finish()

		replay := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File(),
			"--tools", "web_search,fetch_content,get_search_content")
		replay.prompt()
		replayRows := webCardRows(replay)
		for _, want := range []string{
			"search 3 queries",
			"2/3 queries, 1 sources (content ready)",
			"get_content urlIndex=0",
			fmt.Sprintf("%s (%d chars, showing 2-5)", hitTitle, wantChars),
			"get_content missing-",
			"Error: request failed",
		} {
			if !containsRow(replayRows, want) {
				t.Fatalf("replay lacks %q: %#v", want, replayRows)
			}
		}
		replay.write("/quit\r")
		replay.finish()
		if searchRequests.Load() != 3 || pageRequests.Load() != 1 {
			t.Fatalf("replay issued requests: search=%d page=%d", searchRequests.Load(), pageRequests.Load())
		}
	})
}

// webCardSession seeds a persistent session with one user entry.
func webCardSession(t *testing.T, home, work, agentDir string) *session.Store {
	t.Helper()
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("PREVIOUS_QUERY")); err != nil {
		t.Fatal(err)
	}
	return store
}

// webCardLastToolResult returns the text of the last tool_result block in a
// captured provider request body.
func webCardLastToolResult(body map[string]any) string {
	if body == nil {
		return ""
	}
	messages, _ := body["messages"].([]any)
	text := ""
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "user" {
			continue
		}
		blocks, _ := message["content"].([]any)
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			if block["type"] != "tool_result" {
				continue
			}
			switch content := block["content"].(type) {
			case string:
				text = content
			case []any:
				var b strings.Builder
				for _, rawPart := range content {
					part, _ := rawPart.(map[string]any)
					if value, ok := part["text"].(string); ok {
						b.WriteString(value)
					}
				}
				text = b.String()
			}
		}
	}
	return text
}

// webCardResponseID reads the synthetic responseId from the last tool result.
func webCardResponseID(t *testing.T, body map[string]any) string {
	t.Helper()
	const marker = "responseId: "
	text := webCardLastToolResult(body)
	index := strings.LastIndex(text, marker)
	if index < 0 {
		t.Fatalf("no responseId in tool result: %q", text)
	}
	rest := text[index+len(marker):]
	if end := strings.IndexByte(rest, '\n'); end >= 0 {
		rest = rest[:end]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		t.Fatalf("empty responseId in tool result: %q", text)
	}
	return rest
}

// webCardPsiWeb returns every psiWeb envelope in the session file, without
// reading or asserting on the provider's original call arguments.
func webCardPsiWeb(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var record map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if raw, ok := record["psiWeb"]; ok {
			out = append(out, raw...)
		}
	}
	return out
}
