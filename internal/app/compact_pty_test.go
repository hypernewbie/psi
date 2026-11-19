//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/session"
)

func compactWaitBody(t *testing.T, bodies <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case body := <-bodies:
		return body
	case <-time.After(8 * time.Second):
		t.Fatal("provider never received the request")
		return nil
	}
}

func compactHoldPartial(w http.ResponseWriter, req *http.Request, text string, stop <-chan struct{}) {
	fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"partial\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n")
	fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text)
	w.(http.Flusher).Flush()
	select {
	case <-stop:
	case <-req.Context().Done():
	}
}

func compactTextTurn(text string) string {
	return "event: message_start\ndata: {\"message\":{\"id\":\"reply\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n"
}

func compactRequestText(t *testing.T, body map[string]any) string {
	t.Helper()
	var sb strings.Builder
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		msg, _ := raw.(map[string]any)
		if msg["role"] != "user" {
			continue
		}
		switch content := msg["content"].(type) {
		case string:
			sb.WriteString(content)
			sb.WriteByte('\n')
		case []any:
			for _, rawBlock := range content {
				block, _ := rawBlock.(map[string]any)
				if block["type"] == "text" {
					if text, _ := block["text"].(string); text != "" {
						sb.WriteString(text)
						sb.WriteByte('\n')
					}
				}
			}
		}
	}
	return sb.String()
}

func compactTranscript(t *testing.T, path string) (prompts, aborted, checkpoints int) {
	t.Helper()
	_, entries, _, err := session.ReadFileEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Message == nil {
			if entry.Compaction != nil {
				checkpoints++
			}
			continue
		}
		msg := entry.Message.Message
		if msg.User != nil && msg.User.Content == "second work" {
			prompts++
		}
		if msg.Assistant != nil {
			if msg.Assistant.StopReason == "aborted" {
				text := ""
				for _, block := range msg.Assistant.Content {
					if block.Text != nil {
						text += block.Text.Text
					}
				}
				if text == "ORIGINAL_PARTIAL" {
					aborted++
				}
			}
		}
	}
	return prompts, aborted, checkpoints
}

// TestActualAppCompactWaitsForCancelledRun proves that a busy /compact
// cancels the active run once, waits for its durable aborted response, then
// starts one summary with the requested focus.
func TestActualAppCompactWaitsForCancelledRun(t *testing.T) {
	bodies := make(chan map[string]any, 8)
	releaseFirst := make(chan struct{})
	releaseSummary := make(chan struct{})
	stopStub := make(chan struct{})
	var mu sync.Mutex
	reqCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		reqCount++
		n := reqCount
		mu.Unlock()
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		switch n {
		case 1:
			select {
			case <-releaseFirst:
			case <-stopStub:
			case <-req.Context().Done():
				return
			}
			fmt.Fprint(w, compactTextTurn("FIRST_DONE"))
		case 2:
			compactHoldPartial(w, req, "ORIGINAL_PARTIAL", stopStub)
		case 3:
			select {
			case <-releaseSummary:
			case <-stopStub:
			case <-req.Context().Done():
				return
			}
			fmt.Fprint(w, compactTextTurn("SUMMARY"))
		default:
			fmt.Fprint(w, compactTextTurn("EXTRA"))
		}
	}))
	defer server.Close()
	defer close(stopStub)

	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","compaction":{"enabled":false,"keepRecentTokens":50}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":22000,"maxTokens":4096,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "local-model",
		StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("prior ", 6000)}}},
	}}); err != nil {
		t.Fatal(err)
	}

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("first work\r")
	compactWaitBody(t, bodies)
	close(releaseFirst)
	q.wait("FIRST_DONE")
	q.prompt()

	q.write("second work\r")
	compactWaitBody(t, bodies)
	q.wait("ORIGINAL_PARTIAL")
	q.write("/compact FOCUS_MARKER\r")
	q.wait("Cancelling the run for compaction.")
	summary := compactWaitBody(t, bodies)
	if !strings.Contains(compactRequestText(t, summary), "FOCUS_MARKER") {
		t.Fatalf("summary request lacks the focus marker: %v", summary)
	}
	prompts, aborted, checkpoints := compactTranscript(t, store.File())
	if prompts != 1 || aborted != 1 || checkpoints != 0 {
		t.Fatalf("durable prompts/aborted/checkpoints at summary start = %d/%d/%d, want 1/1/0", prompts, aborted, checkpoints)
	}
	close(releaseSummary)
	q.wait("Compacted from")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	prompts, aborted, checkpoints = compactTranscript(t, store.File())
	if prompts != 1 || aborted != 1 || checkpoints != 1 {
		t.Fatalf("durable prompts/aborted/checkpoints = %d/%d/%d, want 1/1/1", prompts, aborted, checkpoints)
	}
	mu.Lock()
	got := reqCount
	mu.Unlock()
	if got != 3 {
		t.Fatalf("provider requests = %d, want first run, interrupted run, one summary", got)
	}
}
