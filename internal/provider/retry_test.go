package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsRetryableErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"auth", &ProviderError{Kind: ErrAuth, Message: "invalid api key"}, false},
		{"invalid request", &ProviderError{Kind: ErrInvalid, Message: "bad request"}, false},
		{"context limit", &ProviderError{Kind: ErrContextLimit, Message: "prompt is too long"}, false},
		{"transport", &ProviderError{Kind: ErrTransport, Status: 503, Message: "Service Unavailable"}, true},
		{"invalid transport status", &ProviderError{Kind: ErrTransport, Status: 400, Message: "Bad Request"}, false},
		{"unclassified transport", &ProviderError{Kind: ErrTransport, Message: "HTTP request failed"}, false},
		{"rate limit throttle", &ProviderError{Kind: ErrRateLimit, Status: 429, Message: "Too Many Requests"}, true},
		{"quota on rate limit kind", &ProviderError{Kind: ErrRateLimit, Status: 429, Message: "insufficient_quota: add credits"}, false},
		{"billing", &ProviderError{Kind: ErrTransport, Status: 429, Message: "billing hard limit reached"}, false},
		{"usage limit type", &ProviderError{Kind: ErrTransport, Status: 429, Message: "GoUsageLimitError: plan spent"}, false},
		{"zhipu 1308 numeric", fmt.Errorf(`gateway said {"code":1308,"message":"balance exhausted"}`), false},
		{"zhipu 1308 string", fmt.Errorf(`gateway said {"code":"1308","message":"balance exhausted"}`), false},
		{"zhipu code 13080 with throttle", fmt.Errorf(`HTTP 429: {"code":13080,"message":"rate limit"}`), true},
		{"stream interrupt with partial", &StreamInterrupt{Partial: &Response{Text: "partial"}, Err: errors.New("provider stream interrupted (not retried): idle timeout")}, true},
		{"completions interrupt", fmt.Errorf("OpenAI Completions stream interrupted (not retried): stream terminated"), true},
		{"google interrupt", fmt.Errorf("Google Generative AI stream interrupted (not retried): unexpected EOF"), true},
		{"idle timeout", fmt.Errorf("idle timeout reading provider stream"), true},
		{"connection refused", fmt.Errorf("dial tcp: connection refused"), true},
		{"connection reset", fmt.Errorf("read: connection reset by peer"), true},
		{"no such host", fmt.Errorf("dial tcp: lookup api.example: no such host"), true},
		{"overloaded", fmt.Errorf("provider overloaded, try again"), true},
		{"gateway upstream", fmt.Errorf("provider returned an error: upstream_request_failed"), true},
		{"explicit retry guidance", fmt.Errorf("The server had an error; please retry your request"), true},
		{"status code in text", fmt.Errorf("gateway returned 502 for this request"), true},
		{"status code in request id", fmt.Errorf("request id req-4290123 failed"), false},
		{"deterministic message", fmt.Errorf("model does not support thinking level"), false},
		{"cancelled", fmt.Errorf("context canceled"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableError(tt.err); got != tt.want {
				t.Fatalf("IsRetryableError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestZhipuUsageLimitPreservesPiErrorAndDoesNotRetry(t *testing.T) {
	const body = `{"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-11-08 07:57:18"}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, body)
	}))
	defer server.Close()

	client := NewCompletionsClient(server.URL, "test-key", 3, time.Second)
	model := ModelMeta{ID: "glm-4.5", API: "openai-completions", Provider: "zhipu", Custom: true, MaxTokens: 1024}
	_, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{
		Role: "user", Content: []Block{{Type: "text", Text: "hello"}},
	}}}, nil)
	if err == nil || err.Error() != "429: "+body {
		t.Fatalf("provider error = %v, want %q", err, "429: "+body)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider requests = %d, want one for non-retryable plan limit", got)
	}
	if IsRetryableError(err) {
		t.Fatalf("Zhipu plan limit must be non-retryable: %v", err)
	}
}

func TestQuotaResponseBodyPreventsRetriesWithoutLeakingDetails(t *testing.T) {
	const detail = `{"error":{"code":"insufficient_quota","message":"private billing detail"}}`
	quota := &ProviderError{Kind: ErrRateLimit, Status: http.StatusTooManyRequests, Message: "Too Many Requests", Detail: detail}
	if IsRetryableError(quota) {
		t.Fatal("quota response was classified as retryable")
	}
	if strings.Contains(quota.Error(), "private billing detail") {
		t.Fatalf("provider error leaked response details: %v", quota)
	}
	var calls atomic.Int32
	_, err := sendWithRetryObserved(context.Background(), 3, nil, func() (*Response, error) {
		calls.Add(1)
		return nil, quota
	})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("quota retry attempts=%d error=%v, want one failed attempt", calls.Load(), err)
	}
}

func TestIsRetryableErrorUnwrapsWrappedErrors(t *testing.T) {
	wrapped := fmt.Errorf("summary failed; original context is unchanged: %w",
		&ProviderError{Kind: ErrTransport, Status: 529, Message: "Overloaded"})
	if !IsRetryableError(wrapped) {
		t.Fatal("wrapped retryable transport error classified as fatal")
	}
}
