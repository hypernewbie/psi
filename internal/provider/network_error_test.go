package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"psi/internal/testutil/leakcheck"
)

func TestClassifyHTTPError(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		body           string
		header         http.Header
		wantKind       ErrorKind
		wantStatus     int
		wantMessage    string
		wantRetryAfter time.Duration
	}{
		// 1. Context overflow patterns across providers (all 25 Pi overflow patterns + HTTP 413)
		{
			name:        "1. anthropic 400 prompt is too long",
			status:      400,
			body:        `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "2. zai 400 prompt exceeds max length",
			status:      400,
			body:        `{"code":"1261","message":"Prompt exceeds max length"}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "3. anthropic 400 request_too_large",
			status:      400,
			body:        `{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "4. bedrock 400 input is too long for requested model",
			status:      400,
			body:        `{"message":"Input is too long for requested model"}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "5. openai 400 context window exceeded",
			status:      400,
			body:        `{"error":{"message":"Your input exceeds the context window of this model","type":"invalid_request_error"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "6. openai proxy 400 maximum context length",
			status:      400,
			body:        `{"error":{"message":"Requested token count exceeds the model's maximum context length of 131072 tokens"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "7. google 400 token count exceeds maximum",
			status:      400,
			body:        `{"error":{"code":400,"message":"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)","status":"INVALID_ARGUMENT"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "8. xai 400 maximum prompt length",
			status:      400,
			body:        `{"error":{"message":"maximum prompt length is 131072 but request contains 150000 tokens"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "9. groq 400 reduce the length of the messages",
			status:      400,
			body:        `{"error":{"message":"Please reduce the length of the messages or completion"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "10. openrouter 400 maximum context length is tokens",
			status:      400,
			body:        `{"error":{"message":"maximum context length is 8192 tokens"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "11. openrouter poolside 400 exceeds maximum allowed input length",
			status:      400,
			body:        `{"error":{"message":"Input length exceeds the maximum allowed input length of 64,000 tokens."}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "12. together 400 input tokens longer than context length",
			status:      400,
			body:        `{"error":{"message":"The input (35000 tokens) is longer than the model's context length (32768 tokens)."}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "13. copilot 400 exceeds the limit of tokens",
			status:      400,
			body:        `{"error":{"message":"prompt token count exceeds the limit of 128000"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "14. llamacpp 400 exceeds the available context size",
			status:      400,
			body:        `{"error":{"message":"request exceeds the available context size"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "15. lmstudio 400 greater than the context length",
			status:      400,
			body:        `{"error":{"message":"prompt length is greater than the context length"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "16. minimax 400 context window exceeds limit",
			status:      400,
			body:        `{"error":{"message":"context window exceeds limit"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "17. kimi 400 exceeded model token limit",
			status:      400,
			body:        `{"error":{"message":"exceeded model token limit: 32000 (requested: 40000)"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "18. mistral 400 too large for model with maximum context length",
			status:      400,
			body:        `{"error":{"message":"Prompt contains 40000 tokens, too large for model with 32768 maximum context length"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "19. ds4 400 prompt has tokens but configured context size",
			status:      400,
			body:        `{"error":{"message":"Prompt has 50,000 tokens, but the configured context size is 32,768 tokens"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "20. zai 400 model_context_window_exceeded",
			status:      400,
			body:        `{"error":{"message":"model_context_window_exceeded"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "21. ollama 400 prompt too long exceeded max context",
			status:      400,
			body:        `{"error":"prompt too long; exceeded max context length by 100 tokens"}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "22. dashscope 400 range of input length should be",
			status:      400,
			body:        `{"error":{"message":"Range of input length should be [1, 131072]"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "23. generic 400 context_length_exceeded",
			status:      400,
			body:        `{"error":{"message":"prompt token count of 150000 exceeds the limit of 128000","code":"context_length_exceeded"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "24. generic 400 too many tokens",
			status:      400,
			body:        `{"error":{"message":"Too many tokens in input prompt"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "25. generic 400 token limit exceeded",
			status:      400,
			body:        `{"error":{"message":"Token limit exceeded"}}`,
			wantKind:    ErrContextLimit,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "http 413 request entity too large",
			status:      413,
			body:        `{"error":"Request Entity Too Large"}`,
			wantKind:    ErrContextLimit,
			wantStatus:  413,
			wantMessage: "Request Entity Too Large",
		},

		// 1b. Non-overflow exclusion patterns (verify they do NOT return ErrContextLimit)
		{
			name:        "non-overflow throttling exception with too many tokens",
			status:      400,
			body:        `{"error":{"message":"ThrottlingException: Too many tokens, please wait before trying again"}}`,
			wantKind:    ErrInvalid,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "non-overflow throttling error with too many tokens",
			status:      400,
			body:        `{"error":{"message":"Throttling error: Too many tokens, please wait before trying again"}}`,
			wantKind:    ErrInvalid,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "non-overflow service unavailable with too many tokens",
			status:      400,
			body:        `{"error":{"message":"Service unavailable: The service is temporarily unavailable, too many tokens"}}`,
			wantKind:    ErrInvalid,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "non-overflow rate limit with too many tokens",
			status:      400,
			body:        `{"error":{"message":"Rate limit: too many tokens"}}`,
			wantKind:    ErrInvalid,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "non-overflow rate limit reached with too many tokens",
			status:      400,
			body:        `{"error":{"message":"Rate limit reached: too many tokens"}}`,
			wantKind:    ErrInvalid,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "non-overflow too many requests with prompt too long",
			status:      400,
			body:        `{"error":{"message":"Too many requests: prompt too long"}}`,
			wantKind:    ErrInvalid,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "standard 400 invalid request (not context overflow)",
			status:      400,
			body:        `{"error":{"message":"missing required parameter 'model'"}}`,
			wantKind:    ErrInvalid,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},

		// 2. Auth errors: 401 & 403
		{
			name:        "401 unauthorized",
			status:      401,
			body:        `{"error":"Unauthorized: invalid api key"}`,
			wantKind:    ErrAuth,
			wantStatus:  401,
			wantMessage: "Unauthorized",
		},
		{
			name:        "403 forbidden",
			status:      403,
			body:        `{"error":"Forbidden: quota exceeded or permission denied"}`,
			wantKind:    ErrAuth,
			wantStatus:  403,
			wantMessage: "Forbidden",
		},

		// 3. Rate limiting: 429 with and without Retry-After
		{
			name:        "429 rate limit without retry-after",
			status:      429,
			body:        `{"error":"Rate limit reached"}`,
			wantKind:    ErrRateLimit,
			wantStatus:  429,
			wantMessage: "Too Many Requests",
		},
		{
			name:           "429 rate limit with integer retry-after",
			status:         429,
			body:           `{"error":"Rate limit reached"}`,
			header:         http.Header{"Retry-After": []string{"30"}},
			wantKind:       ErrRateLimit,
			wantStatus:     429,
			wantMessage:    "Too Many Requests",
			wantRetryAfter: 30 * time.Second,
		},
		{
			name:           "429 rate limit with decimal retry-after",
			status:         429,
			body:           `{"error":"Rate limit reached"}`,
			header:         http.Header{"Retry-After": []string{"1.5"}},
			wantKind:       ErrRateLimit,
			wantStatus:     429,
			wantMessage:    "Too Many Requests",
			wantRetryAfter: 1500 * time.Millisecond,
		},

		// 4. Server errors: 500 & 529 Overloaded
		{
			name:        "500 internal server error",
			status:      500,
			body:        `{"error":"Internal server error"}`,
			wantKind:    ErrTransport,
			wantStatus:  500,
			wantMessage: "Internal Server Error",
		},
		{
			name:        "529 overloaded",
			status:      529,
			body:        `{"type":"error","error":{"type":"overloaded_error","message":"Anthropic is temporarily overloaded"}}`,
			wantKind:    ErrTransport,
			wantStatus:  529,
			wantMessage: "Overloaded",
		},

		// 5. Non-JSON HTML error page (Cloudflare / nginx)
		{
			name:        "502 bad gateway html page",
			status:      502,
			body:        `<!DOCTYPE html><html><head><title>502 Bad Gateway</title></head><body><center><h1>502 Bad Gateway</h1></center><hr><center>cloudflare</center></body></html>`,
			wantKind:    ErrTransport,
			wantStatus:  502,
			wantMessage: "Bad Gateway",
		},
		{
			name:        "503 service unavailable html page",
			status:      503,
			body:        `<html><body><h1>503 Service Unavailable</h1><p>nginx/1.24.0</p></body></html>`,
			wantKind:    ErrTransport,
			wantStatus:  503,
			wantMessage: "Service Unavailable",
		},

		// 6. Empty and corrupted bodies
		{
			name:        "empty error body 500",
			status:      500,
			body:        "",
			wantKind:    ErrTransport,
			wantStatus:  500,
			wantMessage: "Internal Server Error",
		},
		{
			name:        "empty error body 400",
			status:      400,
			body:        "",
			wantKind:    ErrInvalid,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "corrupted json body 400",
			status:      400,
			body:        `{"error": {"incomplete_json: `,
			wantKind:    ErrInvalid,
			wantStatus:  400,
			wantMessage: "Bad Request",
		},
		{
			name:        "corrupted json body 500",
			status:      500,
			body:        `{"error": [malformed`,
			wantKind:    ErrTransport,
			wantStatus:  500,
			wantMessage: "Internal Server Error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			header := tc.header
			if header == nil {
				header = make(http.Header)
			}
			resp := &http.Response{
				StatusCode: tc.status,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			pe := classifyHTTPError(resp)
			if pe == nil {
				t.Fatalf("classifyHTTPError returned nil for status %d", tc.status)
			}
			if pe.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", pe.Kind, tc.wantKind)
			}
			if pe.Status != tc.wantStatus {
				t.Errorf("Status = %d, want %d", pe.Status, tc.wantStatus)
			}
			if pe.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", pe.Message, tc.wantMessage)
			}
			if tc.wantRetryAfter > 0 && pe.RetryAfter != tc.wantRetryAfter {
				t.Errorf("RetryAfter = %v, want %v", pe.RetryAfter, tc.wantRetryAfter)
			}
		})
	}
}

func TestProvidersHTTPErrorIntegration(t *testing.T) {
	defer leakcheck.Verify(t)

	errorCases := []struct {
		name       string
		statusCode int
		body       string
		header     map[string]string
		wantKind   ErrorKind
	}{
		{
			name:       "400 context overflow",
			statusCode: 400,
			body:       `{"error":"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)"}`,
			wantKind:   ErrContextLimit,
		},
		{
			name:       "401 unauthorized",
			statusCode: 401,
			body:       `{"error":"Invalid credentials"}`,
			wantKind:   ErrAuth,
		},
		{
			name:       "403 forbidden",
			statusCode: 403,
			body:       `{"error":"Forbidden"}`,
			wantKind:   ErrAuth,
		},
		{
			name:       "429 rate limit with retry-after",
			statusCode: 429,
			body:       `{"error":"Too many requests"}`,
			header:     map[string]string{"Retry-After": "5"},
			wantKind:   ErrRateLimit,
		},
		{
			name:       "502 html error page",
			statusCode: 502,
			body:       `<html><body>502 Bad Gateway from Cloudflare</body></html>`,
			wantKind:   ErrTransport,
		},
		{
			name:       "529 site overloaded",
			statusCode: 529,
			body:       `{"error":"Anthropic is overloaded"}`,
			wantKind:   ErrTransport,
		},
		{
			name:       "corrupted json 400",
			statusCode: 400,
			body:       `{"broken_json`,
			wantKind:   ErrInvalid,
		},
	}

	for _, ec := range errorCases {
		t.Run("Anthropic/"+ec.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range ec.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(ec.statusCode)
				io.WriteString(w, ec.body)
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-key", 0, time.Second)
			defer client.CloseIdle()

			req := Request{
				Model:    ModelMeta{ID: "claude-3-5-sonnet", API: "anthropic-messages"},
				Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
				Thinking: "off",
			}
			_, err := client.Send(context.Background(), req, nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var pe *ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ProviderError, got %T: %v", err, err)
			}
			if pe.Kind != ec.wantKind {
				t.Errorf("Anthropic %s got Kind %v, want %v", ec.name, pe.Kind, ec.wantKind)
			}
			if pe.Status != ec.statusCode {
				t.Errorf("Anthropic %s got Status %d, want %d", ec.name, pe.Status, ec.statusCode)
			}
		})

		t.Run("Completions/"+ec.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range ec.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(ec.statusCode)
				io.WriteString(w, ec.body)
			}))
			defer server.Close()

			client := NewCompletionsClient(server.URL, "test-key", 0, time.Second)
			defer client.CloseIdle()

			req := Request{
				Model:    ModelMeta{ID: "deepseek-chat", API: "openai-completions", Provider: "deepseek"},
				Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
				Thinking: "off",
			}
			_, err := client.Send(context.Background(), req, nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var pe *ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ProviderError, got %T: %v", err, err)
			}
			if pe.Kind != ec.wantKind {
				t.Errorf("Completions %s got Kind %v, want %v", ec.name, pe.Kind, ec.wantKind)
			}
		})

		t.Run("Google/"+ec.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range ec.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(ec.statusCode)
				io.WriteString(w, ec.body)
			}))
			defer server.Close()

			client := NewGoogleClient(server.URL, "test-key", 0, time.Second)
			defer client.CloseIdle()

			req := Request{
				Model:    ModelMeta{ID: "gemini-2.5-flash", API: "google-generative-ai", Provider: "google"},
				Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
				Thinking: "off",
			}
			_, err := client.Send(context.Background(), req, nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var pe *ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ProviderError, got %T: %v", err, err)
			}
			if pe.Kind != ec.wantKind {
				t.Errorf("Google %s got Kind %v, want %v", ec.name, pe.Kind, ec.wantKind)
			}
		})
	}
}
