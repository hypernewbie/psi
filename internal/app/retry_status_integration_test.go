//go:build unix

package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestActualAppShowsProviderBackoffBeforeRetry(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"retry\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"RETRY_SETTLED\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	reviewSettings(t, agentDir, server.URL)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session")
	q.prompt()
	q.write("retry status\r")
	q.wait("Retrying 1/3")
	q.wait("RETRY_SETTLED")
	q.prompt()
	if calls.Load() != 2 || !strings.Contains(q.raw.String(), "Retrying 1/3") {
		t.Fatalf("retry status was not routed through the app event loop: calls=%d", calls.Load())
	}
	q.write("/quit\r")
	q.finish()
}
