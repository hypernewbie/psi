//go:build unix

package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestActualAppShowsLiveThinkingAndCollapsesItWithCtrlT is the end-to-end
// proof: the provider streams reasoning, the transcript shows it, and the
// toggle replaces it with Pi's collapsed label. The toggle also fires
// mid-run, between the reasoning and the answer.
func TestActualAppShowsLiveThinkingAndCollapsesItWithCtrlT(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	thinkingSent, releaseText := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `event: message_start
data: {"message":{"id":"think","model":"synthetic-model","usage":{"input_tokens":1}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"thinking","thinking":"WEIGH_OPTIONS"}}

event: content_block_delta
data: {"index":0,"delta":{"type":"thinking_delta","thinking":"_MORE"}}

event: content_block_stop
data: {"index":0}

`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(thinkingSent)
		<-releaseText
		fmt.Fprint(w, `event: content_block_start
data: {"index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"index":1,"delta":{"type":"text_delta","text":"ANSWER_TOKENS"}}

event: content_block_stop
data: {"index":1}

event: message_delta
data: {"delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {}

`)
	}))
	defer server.Close()
	defer close(releaseText)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":true,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-thinking-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("hello\r")
	<-thinkingSent
	q.wait("WEIGH_OPTIONS_MORE")

	// Toggle while the run is still live: only the reasoning has arrived.
	q.write("\x14")
	q.wait("Thinking...")
	releaseText <- struct{}{}
	q.wait("ANSWER_TOKENS")
	screen := q.screen.Text()
	if strings.Contains(screen, "WEIGH_OPTIONS_MORE") {
		t.Fatalf("the hidden reasoning stayed on the screen:\n%s", screen)
	}
	if got := strings.Count(screen, "Thinking..."); got != 1 {
		t.Fatalf("label count = %d, want 1:\n%s", got, screen)
	}
	if !strings.Contains(screen, "ANSWER_TOKENS") {
		t.Fatalf("the toggle removed the answer:\n%s", screen)
	}

	// The toggle works again and restores the reasoning.
	q.write("\x14")
	q.wait("WEIGH_OPTIONS_MORE")
	q.write("/quit\r")
	q.finish()
}
