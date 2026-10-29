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
)

func TestLiveThinkingAndToolBatchSpacing(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	gate := make(chan struct{})
	var release sync.Once
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		if requests.Add(1) == 1 {
			<-gate
			emit("message_start", map[string]any{"message": map[string]any{"id": "spacing-1", "model": "m", "usage": map[string]any{"input_tokens": 1}}})
			emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "BEFORE_BATCH"}})
			emit("content_block_stop", map[string]any{"index": 0})
			for i, path := range []string{"one.txt", "two.txt"} {
				index := i + 1
				emit("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("call-%d", i), "name": "read"}})
				emit("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": fmt.Sprintf(`{"path":%q}`, path)}})
				emit("content_block_stop", map[string]any{"index": index})
			}
			emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 2}})
		} else {
			emit("message_start", map[string]any{"message": map[string]any{"id": "spacing-2", "model": "m", "usage": map[string]any{"input_tokens": 1}}})
			emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "AFTER_BATCH"}})
			emit("content_block_stop", map[string]any{"index": 0})
			emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}})
		}
		emit("message_stop", map[string]any{})
	}))
	defer func() { release.Do(func() { close(gate) }); server.Close() }()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	writeAppFile(t, filepath.Join(work, "one.txt"), "one")
	writeAppFile(t, filepath.Join(work, "two.txt"), "two")
	q := startReviewPTY(t, home, work, agentDir, "--deny-project")
	q.prompt()
	q.write("go\r")
	q.wait("Thinking…")
	release.Do(func() { close(gate) })
	q.wait("AFTER_BATCH")
	q.prompt()
	view := strings.Join(q.screen.Scrollback, "\n") + "\n" + q.screen.Text()
	rows := strings.Split(view, "\n")
	find := func(text string) int {
		for i, row := range rows {
			if strings.Contains(row, text) {
				return i
			}
		}
		return -1
	}
	before, one, two, after := find("BEFORE_BATCH"), find("one.txt"), find("two.txt"), find("AFTER_BATCH")
	if before < 0 || one != before+2 || two != one+1 || after != two+2 || strings.TrimSpace(rows[before+1]) != "" || strings.TrimSpace(rows[two+1]) != "" || strings.TrimSpace(rows[after+1]) != "" {
		t.Fatalf("unexpected live spacing: %q", view)
	}
	if strings.Contains(view, "Thinking…") {
		t.Fatalf("thinking row remained in transcript: %q", view)
	}
	q.write("/quit\r")
	q.finish()
}
