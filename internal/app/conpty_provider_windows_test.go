//go:build windows

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func startConPTYTestProvider(t *testing.T, agentDir string) (*httptest.Server, <-chan struct{}, chan struct{}, <-chan struct{}) {
	t.Helper()
	streamStarted := make(chan struct{})
	releaseStream := make(chan struct{})
	toolStarted := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "conpty-test-key" {
			t.Errorf("unexpected local provider request: path=%q", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("read local provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flush, _ := w.(http.Flusher)
		emit := func(name string, data any) {
			encoded, err := json.Marshal(data)
			if err != nil {
				t.Errorf("encode synthetic SSE event: %v", err)
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
		}
		messageStart := func(id string) {
			emit("message_start", map[string]any{"message": map[string]any{
				"id": id, "model": "local-model", "usage": map[string]any{"input_tokens": 5},
			}})
		}
		messageEnd := func(reason string) {
			emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": reason}, "usage": map[string]any{"output_tokens": 2}})
			emit("message_stop", map[string]any{})
		}
		textBlockStart := func() {
			emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		}
		switch requests.Add(1) {
		case 1:
			messageStart("stream-1")
			textBlockStart()
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "STREAM_SENTINEL\n\nACTIVE_STREAM"}})
			flush.Flush()
			close(streamStarted)
			select {
			case <-releaseStream:
			case <-r.Context().Done():
				return
			}
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": " STREAM_FINISHED"}})
			emit("content_block_stop", map[string]any{"index": 0})
			messageEnd("end_turn")
		case 2:
			messageStart("tool-1")
			emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": "call-1", "name": "bash"}})
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"command":"sleep 5"}`}})
			emit("content_block_stop", map[string]any{"index": 0})
			messageEnd("tool_use")
			flush.Flush()
			close(toolStarted)
		default:
			messageStart("tool-2")
			textBlockStart()
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "TOOL_FINISHED"}})
			emit("content_block_stop", map[string]any{"index": 0})
			messageEnd("end_turn")
		}
	}))
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192},{"id":"local-model-2","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"conpty-test-key"}}`)
	return server, streamStarted, releaseStream, toolStarted
}
