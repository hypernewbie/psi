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

func TestActualAppShowsZAIUsageLimitLikePiAfterPartialOutput(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	const body = `{"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-11-08 07:57:18"}`
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w,
			"event: message_start\ndata: {\"message\":{\"id\":\"partial\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n"+
				"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PARTIAL_BEFORE_LIMIT\"}}\n\n"+
				"event: content_block_stop\ndata: {\"index\":0}\n\n"+
				"event: content_block_start\ndata: {\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"read-partial\",\"name\":\"read\",\"input\":{}}}\n\n"+
				"event: content_block_delta\ndata: {\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"fixture.txt\\\"}\"}}\n\n"+
				"event: content_block_stop\ndata: {\"index\":1}\n\n"+
				"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n"+
				"event: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(work, "fixture.txt"), "fixture contents")
	reviewSettings(t, agentDir, server.URL)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-session")
	q.prompt()
	q.write("show limit error\r")
	q.wait("PARTIAL_BEFORE_LIMIT")
	q.wait(`Error: 429: {"code":"1308"`)
	q.wait("07:57:18")
	if strings.Contains(q.raw.String(), "Warning: The run was interrupted") || calls.Load() != 2 {
		t.Fatalf("quota error did not match Pi after partial output: calls=%d output=%s", calls.Load(), q.raw.String())
	}
	q.write("/quit\r")
	q.finish()
}

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
	q.wait("Retrying (1/3)")
	q.wait("RETRY_SETTLED")
	q.prompt()
	if calls.Load() != 2 || !strings.Contains(q.raw.String(), "Retrying (1/3)") {
		t.Fatalf("retry status was not routed through the app event loop: calls=%d", calls.Load())
	}
	q.write("/quit\r")
	q.finish()
}

// reviewRetrySettings is reviewSettings with Pi's automatic retry of a failed
// response slowed to baseDelayMs so the indicator is observable.
func reviewRetrySettings(t *testing.T, agentDir, url string, baseDelayMs int) {
	t.Helper()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), fmt.Sprintf(`{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"retry":{"baseDelayMs":%d}}`, baseDelayMs))
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+url+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-review-key"}}`)
}

// Pi retries a response whose stream drops after partial output: the partial
// text stays, the cause and the retry indicator follow it, and the retried
// response ends the run without a failure notice.
func TestActualAppAutoRetriesDroppedStreamLikePi(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) > 1 {
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"retry\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"RECOVERED_AFTER_DROP\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
			return
		}
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"drop\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PARTIAL_BEFORE_DROP\"}}\n\n")
		w.(http.Flusher).Flush()
		// Close the connection before message_stop, like a reset stream.
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	}))
	defer server.Close()
	reviewRetrySettings(t, agentDir, server.URL, 300)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session")
	q.prompt()
	q.write("drop and retry\r")
	q.wait("RECOVERED_AFTER_DROP")
	q.prompt()
	raw := q.raw.String()
	if calls.Load() != 2 {
		t.Fatalf("dropped stream calls=%d, want the failed request and one retry: %s", calls.Load(), raw)
	}
	partial, cause, indicator := strings.Index(raw, "PARTIAL_BEFORE_DROP"), strings.Index(raw, "Error: provider stream interrupted: unexpected EOF"), strings.Index(raw, "Retrying (1/3) in 1s... (escape to cancel)")
	if partial < 0 || cause < partial || indicator < cause {
		t.Fatalf("partial text, cause, and retry indicator are missing or out of order (%d, %d, %d): %s", partial, cause, indicator, raw)
	}
	if strings.Contains(raw, "Retry failed after") || strings.Contains(raw, "The run was interrupted") {
		t.Fatalf("a recovered retry reported a failure: %s", raw)
	}
	// The next prompt still works.
	q.write("after retry\r")
	q.wait("RECOVERED_AFTER_DROP")
	q.prompt()
	if calls.Load() != 3 {
		t.Fatalf("prompt after the retry calls=%d, want one more request", calls.Load())
	}
	q.write("/quit\r")
	q.finish()
}

// Pi's Escape binding aborts a retry that is waiting: the run reports
// "Retry cancelled" and makes no further request. The prompt is still
// unanswered, so a bump sends it again.
func TestActualAppEscapeCancelsAutoRetryLikePi(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) > 1 {
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"bump\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ANSWER_AFTER_CANCEL\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
			return
		}
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"drop\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PARTIAL_BEFORE_DROP\"}}\n\n")
		w.(http.Flusher).Flush()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	}))
	defer server.Close()
	// A minute of backoff cannot elapse before the Escape arrives.
	reviewRetrySettings(t, agentDir, server.URL, 60000)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session")
	q.prompt()
	q.write("drop and wait\r")
	q.wait("Retrying (1/3) in 60s... (escape to cancel)")
	q.write("\x1b") // Pi's Escape binding aborts the retry.
	q.wait("Retry failed after 1 attempts: Retry cancelled")
	q.prompt()
	raw := q.raw.String()
	if calls.Load() != 1 {
		t.Fatalf("cancelled retry calls=%d, want no request after the wait: %s", calls.Load(), raw)
	}
	if strings.Contains(raw, "Request cancelled.") || strings.Contains(raw, "The run was interrupted") {
		t.Fatalf("a cancelled retry did not use Pi's report: %s", raw)
	}
	// The omitted attempt leaves the prompt unanswered.
	q.write("/bump\r")
	q.wait("ANSWER_AFTER_CANCEL")
	q.prompt()
	if calls.Load() != 2 {
		t.Fatalf("bump after a cancelled retry calls=%d, want one new request: %s", calls.Load(), q.raw.String())
	}
	q.write("/quit\r")
	q.finish()
}

// Pi reports a retry budget that ends in failure once, with the cause of the
// last response, after the indicator has shown every retry.
func TestActualAppReportsExhaustedAutoRetryLikePi(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"drop\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PARTIAL_BEFORE_DROP\"}}\n\n")
		w.(http.Flusher).Flush()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	}))
	defer server.Close()
	reviewRetrySettings(t, agentDir, server.URL, 300)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session")
	q.prompt()
	q.write("always drops\r")
	q.wait("Retry failed after 3 attempts: provider stream interrupted: unexpected EOF")
	q.prompt()
	raw := q.raw.String()
	if calls.Load() != 4 {
		t.Fatalf("exhausted retry calls=%d, want the failed request and three retries: %s", calls.Load(), raw)
	}
	// The last failure reports its cause once; the three retried ones stated
	// theirs when their retry was announced.
	for _, want := range []string{"Retrying (1/3) in 1s... (escape to cancel)", "Retrying (2/3) in 1s... (escape to cancel)", "Retrying (3/3) in 2s... (escape to cancel)"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("retry indicator %q is missing: %s", want, raw)
		}
	}
	if got := strings.Count(raw, "Error: provider stream interrupted"); got != 3 {
		t.Fatalf("cause notices = %d, want one per announced retry: %s", got, raw)
	}
	q.write("/quit\r")
	q.finish()
}
