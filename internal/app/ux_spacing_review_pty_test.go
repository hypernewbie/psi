//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"psi/internal/session"
)

func TestLiveAndReplaySpacingAcrossToolOnlyResponses(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		index := int(requests.Add(1))
		emit("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("reply-%d", index), "model": "m", "usage": map[string]any{"input_tokens": 1}}})
		text := ""
		switch index {
		case 1:
			text = "BEFORE_CHAIN"
		case 3:
			text = " \n\n"
		case 4:
			text = "AFTER_CHAIN\n\n```text\nCODE_LINE\n```\n\nDONE_CHAIN\n\n"
		case 5:
			text = "NEXT_REPLY"
		}
		if text != "" {
			emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
			emit("content_block_stop", map[string]any{"index": 0})
		}
		stop := "end_turn"
		if index < 4 {
			emit("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("call-%d", index), "name": "read"}})
			emit("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": fmt.Sprintf(`{"path":"%d.txt"}`, index)}})
			emit("content_block_stop", map[string]any{"index": 1})
			stop = "tool_use"
		}
		emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 2}})
		emit("message_stop", map[string]any{})
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"outputPad":0,"markdown":{"codeBlockIndent":"> "}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	for i := 1; i <= 3; i++ {
		writeAppFile(t, filepath.Join(work, fmt.Sprintf("%d.txt", i)), "content")
	}
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("PREVIOUS_QUERY")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("/session\r")
	q.wait("theme ")
	q.prompt()
	q.write("ASK_UX\r")
	q.wait("DONE_CHAIN")
	q.prompt()
	capture := func(q *reviewPTY) []string {
		t.Helper()
		rows := append(append([]string{}, q.screen.Scrollback...), strings.Split(q.screen.Text(), "\n")...)
		first, last := -1, -1
		for i, row := range rows {
			if strings.Contains(row, "› ASK_UX") {
				first = i
			}
			if strings.Contains(row, "DONE_CHAIN") {
				last = i
			}
		}
		if first < 1 || last < first || last+1 >= len(rows) {
			t.Fatalf("missing transcript: %q", rows)
		}
		if strings.TrimSpace(rows[first-1]) != "" {
			t.Errorf("user submission has no leading gap: %q", rows)
		}
		if strings.TrimSpace(rows[first+1]) != "" {
			t.Errorf("user submission has no trailing gap: %q", rows)
		}
		if strings.TrimSpace(rows[last+1]) != "" {
			t.Errorf("assistant reply has no trailing gap: %q", rows)
		}
		return append([]string(nil), rows[first:last+2]...)
	}
	live := capture(q)
	checkNextTurn := func(q *reviewPTY) {
		t.Helper()
		rows := append(append([]string{}, q.screen.Scrollback...), strings.Split(q.screen.Text(), "\n")...)
		for i := len(rows) - 1; i >= 2; i-- {
			if strings.Contains(rows[i], "› NEXT_UX") {
				if i+2 >= len(rows) || strings.TrimSpace(rows[i-1]) != "" || !strings.Contains(rows[i-2], "DONE_CHAIN") || strings.TrimSpace(rows[i+1]) != "" || !strings.Contains(rows[i+2], "NEXT_REPLY") {
					t.Errorf("consecutive turn gained or lost padding: %q", rows)
				}
				return
			}
		}
		t.Fatal("next turn missing")
	}
	q.write("NEXT_UX\r")
	q.wait("NEXT_REPLY")
	q.prompt()
	checkNextTurn(q)
	for _, command := range []string{"/history all\r", "/history 2\r"} {
		q.write(command)
		q.wait("DONE_CHAIN")
		q.prompt()
		if history := capture(q); !reflect.DeepEqual(live, history) {
			t.Errorf("live/history transcript differs for %q\nlive: %#v\nhistory: %#v", command, live, history)
		}
	}
	q.write("/quit\r")
	q.finish()
	q = startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	replay := capture(q)
	checkNextTurn(q)
	if !reflect.DeepEqual(live, replay) {
		t.Errorf("live/replay transcript differs\nlive: %#v\nreplay: %#v", live, replay)
	}
	q.write("/quit\r")
	q.finish()
	if requests.Load() != 5 {
		t.Fatalf("history sent provider requests: %d", requests.Load())
	}
}
