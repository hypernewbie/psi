//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/hypernewbie/psi/internal/testutil"
)

const (
	wrapEnglishSource = "this is a draft message where as the word gets chopped in half"
	wrapChineseSource = "这是一个很长的中文句子，用来测试换行是否符合中文标点规则。我们需要确保标点符号不会出现在行首。"
)

type wrapApp struct {
	q            *reviewPTY
	requests     chan map[string]any
	decodeErrors chan error
	requestCount *atomic.Int32
}

// startWrapApp runs the real application against an isolated synthetic
// loopback provider. Every request is counted and captured without blocking
// the handler goroutine.
func startWrapApp(t *testing.T) *wrapApp {
	t.Helper()
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	requests := make(chan map[string]any, 8)
	decodeErrors := make(chan error, 8)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			select {
			case decodeErrors <- err:
			default:
			}
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		select {
		case requests <- body:
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"wrap\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"WRAP_OK\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
	}))
	t.Cleanup(server.Close)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"editorPaddingX":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-wrap-key"}}`)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session")
	return &wrapApp{q: q, requests: requests, decodeErrors: decodeErrors, requestCount: &count}
}

// waitScreen feeds PTY output until the screen predicate holds. It uses the
// screen state, not a quiet-gap timer, so a mid-keystroke frame cannot pass
// for a settled one.
func (a *wrapApp) waitScreen(t *testing.T, description string, match func(*testutil.VTModel) bool) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		if match(a.q.screen) {
			a.q.pending = ""
			return
		}
		select {
		case chunk, ok := <-a.q.chunks:
			if !ok {
				t.Fatalf("process exited before %s: %q", description, a.q.raw.String())
			}
			a.q.pending += string(chunk)
			a.q.raw.Write(chunk)
			a.q.screen.Feed(chunk)
		case <-deadline.C:
			t.Fatalf("timeout waiting for %s: screen=%q", description, a.q.screen.Text())
		}
	}
}

func (a *wrapApp) resize(t *testing.T, width int) {
	t.Helper()
	if err := pty.Setsize(a.q.master, &pty.Winsize{Rows: 40, Cols: uint16(width)}); err != nil {
		t.Fatal(err)
	}
	a.q.screen.Resize(width, 40)
}

// wrapEditorLayout returns the nearest bordered editor around the visible
// cursor: border row indices, the body rows between them, and the cursor row
// relative to the first body row.
func wrapEditorLayout(screen *testutil.VTModel) (top, bottom int, body []string, cursorBodyRow int, ok bool) {
	if screen == nil || !screen.CursorVisible || screen.CursorRow < 0 {
		return 0, 0, nil, 0, false
	}
	rows := strings.Split(screen.Text(), "\n")
	top, bottom = -1, -1
	for i := screen.CursorRow - 1; i >= 0; i-- {
		if strings.Contains(rows[i], "─") {
			top = i
			break
		}
	}
	for i := screen.CursorRow + 1; i < len(rows); i++ {
		if strings.Contains(rows[i], "─") {
			bottom = i
			break
		}
	}
	if top < 0 || bottom < 0 || bottom <= top+1 {
		return 0, 0, nil, 0, false
	}
	return top, bottom, rows[top+1 : bottom], screen.CursorRow - top - 1, true
}

func wrapEmptyEditor(width int) func(*testutil.VTModel) bool {
	border := strings.Repeat("─", width-1)
	return func(screen *testutil.VTModel) bool {
		top, bottom, body, cursorBodyRow, ok := wrapEditorLayout(screen)
		if !ok {
			return false
		}
		rows := strings.Split(screen.Text(), "\n")
		if rows[top] != border || rows[bottom] != border {
			return false
		}
		return reflect.DeepEqual(body, []string{""}) && cursorBodyRow == 0 && screen.CursorCol == 0
	}
}

func wrapEnglishRows(screen *testutil.VTModel, width int) (top, bottom int, body []string, cursorBodyRow int, ok bool) {
	top, bottom, body, cursorBodyRow, ok = wrapEditorLayout(screen)
	if !ok {
		return 0, 0, nil, 0, false
	}
	rows := strings.Split(screen.Text(), "\n")
	border := strings.Repeat("─", width-1)
	if rows[top] != border || rows[bottom] != border {
		return 0, 0, nil, 0, false
	}
	return top, bottom, body, cursorBodyRow, true
}

func wrapChineseBody(screen *testutil.VTModel, top int, want []string) bool {
	if screen.Width != 24 {
		return false
	}
	body := screen.Rows[top+1 : top+1+len(want)]
	for i, expected := range want {
		cells := body[i]
		j := 0
		for _, r := range expected {
			if 2*j+1 >= len(cells) || cells[2*j] != r || cells[2*j+1] != 0 {
				return false
			}
			j++
		}
		for _, cell := range cells[2*j:] {
			if cell != 0 {
				return false
			}
		}
	}
	return true
}

func wrapEnglishFinal(screen *testutil.VTModel) bool {
	top, _, body, cursorBodyRow, ok := wrapEnglishRows(screen, 40)
	if !ok {
		return false
	}
	want := []string{"this is a draft message where as the", "word gets chopped in half"}
	return reflect.DeepEqual(body, want) && cursorBodyRow == 1 && screen.CursorCol == 25 && top > 0
}

func wrapChineseFinal(screen *testutil.VTModel) bool {
	top, bottom, _, cursorBodyRow, ok := wrapEditorLayout(screen)
	if !ok {
		return false
	}
	rows := strings.Split(screen.Text(), "\n")
	border := strings.Repeat("─", 23)
	if rows[top] != border || rows[bottom] != border {
		return false
	}
	want := []string{
		"这是一个很长的中文句",
		"子，用来测试换行是否符",
		"合中文标点规则。我们需",
		"要确保标点符号不会出现",
		"在行首。",
	}
	if bottom-top-1 != len(want) || !wrapChineseBody(screen, top, want) {
		return false
	}
	return cursorBodyRow == 4 && screen.CursorCol == 8
}

func wrapIdleReply(width int) func(*testutil.VTModel) bool {
	border := strings.Repeat("─", width-1)
	return func(screen *testutil.VTModel) bool {
		if !strings.Contains(screen.Text(), "WRAP_OK") {
			return false
		}
		top, bottom, body, cursorBodyRow, ok := wrapEditorLayout(screen)
		if !ok {
			return false
		}
		rows := strings.Split(screen.Text(), "\n")
		if rows[top] != border || rows[bottom] != border || strings.Contains(rows[top], "Working") {
			return false
		}
		return reflect.DeepEqual(body, []string{""}) && cursorBodyRow == 0 && screen.CursorCol == 0
	}
}

func wrapLastUserText(t *testing.T, body map[string]any) string {
	t.Helper()
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("request has no messages: %v", body)
	}
	last, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		t.Fatalf("last message is not an object: %v", messages[len(messages)-1])
	}
	if last["role"] != "user" {
		t.Fatalf("last message role = %v, want user", last["role"])
	}
	blocks, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("last message content = %T, want text blocks", last["content"])
	}
	var text strings.Builder
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || block["type"] != "text" {
			continue
		}
		s, _ := block["text"].(string)
		text.WriteString(s)
	}
	return text.String()
}

func (a *wrapApp) capturedRequest(t *testing.T) map[string]any {
	t.Helper()
	select {
	case body := <-a.requests:
		return body
	case <-time.After(2 * time.Second):
		t.Fatal("provider request was not captured")
		return nil
	}
}

func (a *wrapApp) assertNoDecodeErrors(t *testing.T) {
	t.Helper()
	select {
	case err := <-a.decodeErrors:
		t.Fatalf("provider request body did not decode: %v", err)
	default:
	}
}

func TestActualAppEditorWordWrap(t *testing.T) {
	t.Run("english", func(t *testing.T) {
		a := startWrapApp(t)
		a.waitScreen(t, "initial empty editor", wrapEmptyEditor(80))
		a.resize(t, 40)
		a.waitScreen(t, "resized empty editor", wrapEmptyEditor(40))

		a.q.write(wrapEnglishSource)
		a.waitScreen(t, "wrapped English draft", wrapEnglishFinal)
		if got := a.requestCount.Load(); got != 0 {
			t.Fatalf("typing and wrapping sent %d provider requests, want 0", got)
		}

		a.q.write("\r")
		a.waitScreen(t, "WRAP_OK and idle empty editor", wrapIdleReply(40))
		body := a.capturedRequest(t)
		if got := wrapLastUserText(t, body); got != wrapEnglishSource {
			t.Fatalf("submitted source = %q, want %q", got, wrapEnglishSource)
		}
		a.q.write("\x04")
		a.q.finish()
		if got := a.requestCount.Load(); got != 1 {
			t.Fatalf("provider requests = %d, want exactly 1", got)
		}
		a.assertNoDecodeErrors(t)
	})

	t.Run("chinese", func(t *testing.T) {
		a := startWrapApp(t)
		a.waitScreen(t, "initial empty editor", wrapEmptyEditor(80))
		a.resize(t, 24)
		a.waitScreen(t, "resized empty editor", wrapEmptyEditor(24))

		a.q.write(wrapChineseSource)
		a.waitScreen(t, "wrapped Chinese draft", wrapChineseFinal)
		if got := a.requestCount.Load(); got != 0 {
			t.Fatalf("typing and wrapping sent %d provider requests, want 0", got)
		}

		a.q.write("\r")
		a.waitScreen(t, "WRAP_OK and idle empty editor", wrapIdleReply(24))
		body := a.capturedRequest(t)
		if got := wrapLastUserText(t, body); got != wrapChineseSource {
			t.Fatalf("submitted source = %q, want %q", got, wrapChineseSource)
		}
		a.q.write("\x04")
		a.q.finish()
		if got := a.requestCount.Load(); got != 1 {
			t.Fatalf("provider requests = %d, want exactly 1", got)
		}
		a.assertNoDecodeErrors(t)
	})
}

func wrapEnglishWidth24(screen *testutil.VTModel) bool {
	top, _, body, cursorBodyRow, ok := wrapEnglishRows(screen, 24)
	if !ok || top < 0 {
		return false
	}
	want := []string{"this is a draft", "message where as the", "word gets chopped in", "half"}
	return reflect.DeepEqual(body, want) && cursorBodyRow == 3 && screen.CursorCol == 4
}

func TestActualAppEditorWordWrapResize(t *testing.T) {
	a := startWrapApp(t)
	a.waitScreen(t, "initial empty editor", wrapEmptyEditor(80))
	a.resize(t, 40)
	a.waitScreen(t, "empty editor at width 40", wrapEmptyEditor(40))

	a.q.write(wrapEnglishSource)
	a.waitScreen(t, "width 40 draft", wrapEnglishFinal)

	a.resize(t, 24)
	a.waitScreen(t, "width 24 draft", wrapEnglishWidth24)

	a.resize(t, 40)
	a.waitScreen(t, "width 40 draft after resize", wrapEnglishFinal)

	if got := a.requestCount.Load(); got != 0 {
		t.Fatalf("typing and resizing sent %d provider requests, want 0", got)
	}

	a.q.write("\r")
	a.waitScreen(t, "WRAP_OK and idle empty editor", wrapIdleReply(40))
	body := a.capturedRequest(t)
	if got := wrapLastUserText(t, body); got != wrapEnglishSource {
		t.Fatalf("submitted source = %q, want %q", got, wrapEnglishSource)
	}
	a.q.write("\x04")
	a.q.finish()
	if got := a.requestCount.Load(); got != 1 {
		t.Fatalf("provider requests = %d, want exactly 1", got)
	}
	a.assertNoDecodeErrors(t)
}
