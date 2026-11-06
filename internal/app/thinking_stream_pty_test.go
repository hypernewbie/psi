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
// proof: long reasoning reaches native scrollback before the answer arrives,
// and the toggle replaces it with Pi's collapsed label mid-run.
func TestActualAppShowsLiveThinkingAndCollapsesItWithCtrlT(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	releaseText := make(chan struct{})
	var reasoning strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&reasoning, "reason %02d\n", i)
	}
	reasoning.WriteString("WEIGHOPTIONS")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `event: message_start
data: {"message":{"id":"think","model":"synthetic-model","usage":{"input_tokens":1}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"thinking","thinking":%q}}

event: content_block_delta
data: {"index":0,"delta":{"type":"thinking_delta","thinking":"MORE"}}

event: content_block_stop
data: {"index":0}

`, reasoning.String())
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
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
	q.waitScreen("synthetic-model")
	q.write("hello\r")
	q.wait("WEIGHOPTIONSMORE")
	live := strings.Join(q.screen.Scrollback, "\n") + "\n" + q.screen.Text()
	for i := 1; i <= 60; i++ {
		needle := fmt.Sprintf("reason %02d", i)
		if got := strings.Count(live, needle); got != 1 {
			t.Fatalf("live reasoning %q appears %d times, want 1:\n%s", needle, got, live)
		}
	}
	if !strings.Contains(strings.Join(q.screen.Scrollback, "\n"), "reason 01") {
		t.Fatalf("oldest live reasoning never reached native scrollback:\n%s", live)
	}

	// Toggle while the run is still live: only the reasoning has arrived.
	q.write("\x14")
	q.wait("Thinking...")
	releaseText <- struct{}{}
	q.wait("ANSWER_TOKENS")
	screen := q.screen.Text()
	if strings.Contains(screen, "WEIGHOPTIONSMORE") {
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
	q.wait("WEIGHOPTIONSMORE")
	restored := strings.Join(q.screen.Scrollback, "\n") + "\n" + q.screen.Text()
	for i := 1; i <= 60; i++ {
		needle := fmt.Sprintf("reason %02d", i)
		if got := strings.Count(restored, needle); got != 1 {
			t.Fatalf("restored reasoning %q appears %d times, want 1:\n%s", needle, got, restored)
		}
	}
	q.write("/quit\r")
	q.finish()
}
