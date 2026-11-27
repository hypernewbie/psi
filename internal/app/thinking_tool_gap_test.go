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

	"github.com/hypernewbie/psi/internal/testutil"
)

// TestActualAppKeepsThinkingLineBreaksBeforeAToolCall proves that a
// multi-line reasoning block keeps its line breaks in the settled
// transcript and that the tool row starts on its own line. Pi renders
// reasoning as Markdown, and its Markdown keeps a paragraph's source line
// breaks as line breaks. A joined reasoning block ran straight into the
// tool call that followed it.
func TestActualAppKeepsThinkingLineBreaksBeforeAToolCall(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	if err := os.WriteFile(filepath.Join(work, "fixture.txt"), []byte("SYNTHETIC_READ_RESULT"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) > 1 {
			fmt.Fprint(w, `event: message_start
data: {"message":{"id":"think-tool-2","model":"synthetic-model","usage":{"input_tokens":1}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"index":0,"delta":{"type":"text_delta","text":"ANSWER_AFTER_TOOL"}}

event: content_block_stop
data: {"index":0}

event: message_delta
data: {"delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {}

`)
			return
		}
		fmt.Fprint(w, `event: message_start
data: {"message":{"id":"think-tool","model":"synthetic-model","usage":{"input_tokens":1}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"index":0,"delta":{"type":"thinking_delta","thinking":"first thinking line\nsecond thinking line\n"}}

event: content_block_stop
data: {"index":0}

event: content_block_start
data: {"index":1,"content_block":{"type":"tool_use","id":"tool-1","name":"read","input":{}}}

event: content_block_delta
data: {"index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"fixture.txt\"}"}}

event: content_block_stop
data: {"index":1}

event: message_delta
data: {"delta":{"stop_reason":"tool_use"}}

event: message_stop
data: {}

`)
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":["read"],"compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":true,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-think-tool-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--tools", "read")
	q.waitScreen("synthetic-model")
	q.write("hello\r")
	q.wait("ANSWER_AFTER_TOOL")
	waitScreenState(q, "settled editor after the tool call", func(screen *testutil.VTModel) bool {
		return !screen.Modes["?2026"] && semanticEditorReady(screen)
	})
	visible := strings.Join(q.screen.Scrollback, "\n") + "\n" + q.screen.Text()
	if strings.Contains(visible, "first thinking line second thinking line") {
		t.Fatalf("the reasoning lines were joined:\n%s", visible)
	}
	first := thinkingLineIndex(visible, "first thinking line")
	second := thinkingLineIndex(visible, "second thinking line")
	if first < 0 || second < 0 {
		t.Fatalf("the reasoning lines are missing:\n%s", visible)
	}
	if first == second {
		t.Fatalf("both reasoning lines share one row:\n%s", visible)
	}
	q.write("/quit\r")
	q.finish()
}

// thinkingLineIndex returns the index of the screen line that holds needle.
func thinkingLineIndex(visible, needle string) int {
	for i, line := range strings.Split(visible, "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}
