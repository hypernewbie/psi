package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryObserverReportsTheExistingBackoffBeforeRetry(t *testing.T) {
	var observed []RetryInfo
	calls := 0
	response, err := sendWithRetryObserved(context.Background(), 2, func(info RetryInfo) { observed = append(observed, info) }, func() (*Response, error) {
		calls++
		if calls == 1 {
			return nil, &ProviderError{Kind: ErrRateLimit, RetryAfter: time.Millisecond}
		}
		return &Response{Text: "ok"}, nil
	})
	if err != nil || response == nil || response.Text != "ok" || calls != 2 {
		t.Fatalf("response=%+v calls=%d err=%v", response, calls, err)
	}
	if len(observed) != 1 || observed[0].Attempt != 1 || observed[0].Maximum != 2 || observed[0].Delay != time.Millisecond || observed[0].Class != string(ErrRateLimit) {
		t.Fatalf("retry observer=%+v", observed)
	}
}

func TestRetryObserverDoesNotReportFinalOrNonRetryableErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "final attempt", err: &ProviderError{Kind: ErrRateLimit, RetryAfter: time.Millisecond}},
		{name: "auth", err: &ProviderError{Kind: ErrAuth}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, reports := 0, 0
			_, err := sendWithRetryObserved(context.Background(), 0, func(RetryInfo) { reports++ }, func() (*Response, error) {
				calls++
				return nil, test.err
			})
			if err == nil || calls != 1 || reports != 0 {
				t.Fatalf("err=%v calls=%d observer reports=%d", err, calls, reports)
			}
		})
	}
}

func TestAnthropicHTTPRetryReportsRealDelayAndAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"message\":{\"id\":\"r\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	client := NewClient(server.URL, "synthetic", 1, time.Second)
	defer client.CloseIdle()
	observed := make(chan RetryInfo, 2)
	client.SetRetryObserver(func(info RetryInfo) { observed <- info })
	response, err := client.Send(context.Background(), Request{Model: ModelMeta{ID: "m", Provider: "anthropic", API: "anthropic-messages", MaxTokens: 8}, MaxTokens: 8, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "synthetic"}}}}}, nil)
	if err != nil || response == nil || response.Text != "ok" || calls.Load() != 2 {
		t.Fatalf("response=%+v calls=%d err=%v", response, calls.Load(), err)
	}
	info := <-observed
	if info.Attempt != 1 || info.Maximum != 1 || info.Delay != 500*time.Millisecond || info.Class != string(ErrRateLimit) {
		t.Fatalf("Anthropic retry info=%+v", info)
	}
}

func TestCodexHTTPRetryReportsRealDelayAndAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, codexTextStream("ok"))
	}))
	defer server.Close()
	client := NewCodexClient(server.URL, writeCodexAuth(t, false), 1, time.Second)
	defer client.CloseIdle()
	observed := make(chan RetryInfo, 2)
	client.SetRetryObserver(func(info RetryInfo) { observed <- info })
	response, err := client.Send(context.Background(), Request{Model: testCodexModel(), Thinking: "xhigh"}, nil)
	if err != nil || response == nil || response.Text != "ok" || calls.Load() != 2 {
		t.Fatalf("response=%+v calls=%d err=%v", response, calls.Load(), err)
	}
	info := <-observed
	if info.Attempt != 1 || info.Maximum != 1 || info.Delay != 500*time.Millisecond || info.Class != string(ErrRateLimit) {
		t.Fatalf("Codex retry info=%+v", info)
	}
}
