package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func googleChunk(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(encoded) + "\n\n"
}

func TestGoogleClientRequestPathHeadersStreamAndUsage(t *testing.T) {
	var seen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(true)
		if r.URL.Path != "/v1beta/models/gemini-3-flash-preview:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("Google request path/query = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("x-goog-api-key") != "synthetic-google-key" || r.Header.Get("Authorization") != "" || r.Header.Get("Accept") != "*/*" {
			t.Errorf("Google authentication/stream headers = %#v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Google request: %v", err)
		}
		system := body["systemInstruction"].(map[string]any)
		if system["role"] != "user" || system["parts"].([]any)[0].(map[string]any)["text"] != "system" {
			t.Errorf("Google system instruction = %#v", system)
		}
		if body["contents"].([]any)[0].(map[string]any)["role"] != "user" {
			t.Errorf("Google contents = %#v", body["contents"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, googleChunk(t, map[string]any{
			"responseId": "google-response-1",
			"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
				map[string]any{"text": "answer", "thoughtSignature": googleValidSignature},
				map[string]any{"text": "reasoning", "thought": true, "thoughtSignature": googleValidSignature},
				map[string]any{"functionCall": map[string]any{"id": "call-1", "name": "read", "args": map[string]any{"path": "a.txt"}}, "thoughtSignature": googleValidSignature},
			}}, "finishReason": "STOP"}},
			"usageMetadata": map[string]any{"promptTokenCount": 10, "cachedContentTokenCount": 3, "candidatesTokenCount": 2, "thoughtsTokenCount": 4, "totalTokenCount": 16},
		}))
	}))
	defer server.Close()

	model := googleTestModel("gemini-3-flash-preview", "google")
	model.BaseURL = server.URL + "/v1beta"
	model.ThinkingLevelMap = map[string]*string{"off": nil, "minimal": completionString("minimal"), "low": completionString("low")}
	client := NewGoogleClient(model.BaseURL, "synthetic-google-key", 0, time.Second)
	var streamed strings.Builder
	resp, err := client.Send(context.Background(), Request{Model: model, System: "system", Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}}, func(delta string) { streamed.WriteString(delta) })
	client.CloseIdle()
	if err != nil {
		t.Fatal(err)
	}
	if !seen.Load() || resp.ResponseID != "google-response-1" || resp.StopReason != "toolUse" || resp.RawStopReason != "STOP" || resp.Text != "answer" || resp.Thinking != "reasoning" || streamed.String() != "answer" {
		t.Fatalf("Google response = %+v (seen %v)", resp, seen.Load())
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call-1" || resp.ToolCalls[0].Arguments["path"] != "a.txt" ||
		resp.ToolCalls[0].ThoughtSignature != googleValidSignature {
		t.Fatalf("Google function call = %#v", resp.ToolCalls)
	}
	if resp.Content[0].TextSignature != googleValidSignature || resp.Content[1].ThinkingSignature != googleValidSignature {
		t.Fatalf("Google text/thinking signatures = %#v", resp.Content)
	}
	if resp.Usage.Input != 7 || resp.Usage.Output != 6 || resp.Usage.CacheRead != 3 || resp.Usage.TotalTokens != 16 || resp.Usage.Reasoning == nil || *resp.Usage.Reasoning != 4 {
		t.Fatalf("Google provider usage = %+v", resp.Usage)
	}
}

func TestOpenCodeGoogleUsesSelectedPathAndSessionHeaderPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, explicit string
	}{
		{name: "automatic"},
		{name: "explicit case-insensitive override", explicit: "explicit-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/inference/go/google/v1beta/models/gemini-4-argon-contributor:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
					t.Errorf("OpenCode Google route = %s?%s", r.URL.Path, r.URL.RawQuery)
				}
				if r.Header.Get("x-goog-api-key") != "synthetic-opencode-key" {
					t.Errorf("OpenCode Google key header = %#v", r.Header)
				}
				wantSession := "session-123"
				if tc.explicit != "" {
					wantSession = tc.explicit
				}
				if got := r.Header.Get("x-opencode-session"); got != wantSession {
					t.Errorf("OpenCode session header = %q, want %q", got, wantSession)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, googleChunk(t, map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP"}}}))
			}))
			defer server.Close()
			model := googleTestModel("gemini-4-argon-contributor", "opencode-go")
			model.BaseURL = server.URL + "/inference/go/google/v1beta"
			model.ThinkingLevelMap = map[string]*string{"off": nil, "low": completionString("low")}
			client := NewGoogleClient(model.BaseURL, "synthetic-opencode-key", 0, time.Second)
			client.SessionID = "session-123"
			if tc.explicit != "" {
				client.Headers = map[string]string{"X-OpenCode-Session": tc.explicit}
			}
			_, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
			client.CloseIdle()
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGoogleClientGeneratesUniqueIDsForMissingAndDuplicateCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, googleChunk(t, map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{
				map[string]any{"functionCall": map[string]any{"id": "same", "name": "read", "args": map[string]any{}}},
				map[string]any{"functionCall": map[string]any{"id": "same", "name": "write", "args": nil}},
				map[string]any{"functionCall": map[string]any{"name": "edit"}},
			}}, "finishReason": "STOP",
		}}}))
	}))
	defer server.Close()
	model := googleTestModel("gemini-3-flash-preview", "google")
	model.BaseURL = server.URL
	model.ThinkingLevelMap = map[string]*string{"off": nil, "minimal": completionString("minimal")}
	client := NewGoogleClient(server.URL, "key", 0, time.Second)
	resp, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
	client.CloseIdle()
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 3 || resp.ToolCalls[0].ID != "same" || resp.ToolCalls[1].ID == "same" || resp.ToolCalls[2].ID == "" ||
		resp.ToolCalls[1].ID == resp.ToolCalls[2].ID {
		t.Fatalf("generated Google tool IDs = %#v", resp.ToolCalls)
	}
	for _, call := range resp.ToolCalls {
		if call.Arguments == nil {
			t.Errorf("missing args did not normalize to an empty object: %+v", call)
		}
	}
}

func TestGoogleStreamErrorsDoNotPromotePartialToolsOrRetry(t *testing.T) {
	for _, tc := range []struct {
		name, stream, wantError string
	}{
		{name: "missing finish", stream: `data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}` + "\n\n", wantError: "without a finish reason"},
		{name: "malformed JSON", stream: "data: {bad json}\n\n", wantError: "malformed"},
		{name: "safety finish", stream: `data: {"candidates":[{"content":{"parts":[{"text":"visible"},{"functionCall":{"id":"call-1","name":"read","args":{}}}]},"finishReason":"SAFETY"}]}` + "\n\n", wantError: "Provider stopped with: SAFETY"},
		{name: "invalid function arguments", stream: `data: {"candidates":[{"content":{"parts":[{"functionCall":{"id":"call-1","name":"read","args":[]}}]},"finishReason":"STOP"}]}` + "\n\n", wantError: "function arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.stream)
			}))
			model := googleTestModel("gemini-3-flash-preview", "google")
			model.BaseURL = server.URL
			model.ThinkingLevelMap = map[string]*string{"off": nil, "minimal": completionString("minimal")}
			client := NewGoogleClient(server.URL, "key", 3, time.Second)
			resp, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
			server.Close()
			client.CloseIdle()
			var interrupted *StreamInterrupt
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || !errors.As(err, &interrupted) || attempts.Load() != 1 {
				t.Fatalf("Google interrupted response = %+v, err %v attempts %d", resp, err, attempts.Load())
			}
			if resp == nil || len(resp.ToolCalls) != 0 {
				t.Fatalf("partial Google tool calls escaped: %+v", resp)
			}
		})
	}
}

func TestGoogleRetriesBeforeStreamButNotAfterDispatch(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0.001")
			http.Error(w, "synthetic secret echo", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, googleChunk(t, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "ok"}}}, "finishReason": "STOP"}}}))
	}))
	model := googleTestModel("gemini-2.5-flash", "google")
	model.BaseURL = server.URL
	client := NewGoogleClient(server.URL, "synthetic-key", 1, time.Second)
	resp, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
	server.Close()
	client.CloseIdle()
	if err != nil || resp.Text != "ok" || attempts.Load() != 2 {
		t.Fatalf("Google pre-stream retry = %+v, %v attempts %d", resp, err, attempts.Load())
	}

	attempts.Store(0)
	partialServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, googleChunk(t, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "partial"}}}}}}))
	}))
	model.BaseURL = partialServer.URL
	client = NewGoogleClient(partialServer.URL, "synthetic-key", 2, time.Second)
	resp, err = client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
	partialServer.Close()
	client.CloseIdle()
	if err == nil || resp == nil || resp.Text != "partial" || attempts.Load() != 1 {
		t.Fatalf("Google partial stream retry = %+v, %v attempts %d", resp, err, attempts.Load())
	}
}

func TestGoogleCancellationStopsStreamWithoutRetry(t *testing.T) {
	started := make(chan struct{})
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, googleChunk(t, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "partial"}}}}}}))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	model := googleTestModel("gemini-2.5-flash", "google")
	model.BaseURL = server.URL
	client := NewGoogleClient(server.URL, "key", 3, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Send(ctx, Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("Google stream did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || attempts.Load() != 1 {
			t.Fatalf("Google cancellation error = %v attempts %d", err, attempts.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Google stream did not stop after cancellation")
	}
	client.CloseIdle()
}

func TestGoogleMissingKeyAndErrorBodiesStaySecretFree(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash", "google")
	request := Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}
	client := NewGoogleClient("https://example.invalid/v1beta", "", 0, time.Second)
	if _, err := client.Send(context.Background(), request, nil); err == nil || !strings.Contains(err.Error(), "No API key") {
		t.Fatalf("missing Google key error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "synthetic-google-secret", http.StatusForbidden)
	}))
	defer server.Close()
	client = NewGoogleClient(server.URL, "synthetic-google-secret", 0, time.Second)
	_, err := client.Send(context.Background(), request, nil)
	if err == nil || strings.Contains(err.Error(), "synthetic-google-secret") || strings.Contains(err.Error(), "echo") {
		t.Fatalf("Google HTTP error leaked response body/key: %v", err)
	}
}

func TestGoogleFunctionCallArgumentsPreserveLargeNumbers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, googleChunk(t, map[string]any{"candidates": []any{map[string]any{
			"content":      map[string]any{"parts": []any{map[string]any{"functionCall": map[string]any{"name": "read", "args": json.RawMessage(`{"n":9007199254740993}`)}}}},
			"finishReason": "STOP",
		}}}))
	}))
	defer server.Close()
	model := googleTestModel("gemini-2.5-flash", "google")
	model.BaseURL = server.URL
	client := NewGoogleClient(server.URL, "key", 0, time.Second)
	resp, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
	client.CloseIdle()
	if err != nil {
		t.Fatal(err)
	}
	if number, ok := resp.ToolCalls[0].Arguments["n"].(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("Google function call number precision = %#v", resp.ToolCalls[0].Arguments["n"])
	}
}

func TestGoogleSSESupportsMultipleDataLines(t *testing.T) {
	body := "data: {\"candidates\":[{\"finishReason\":\n" + "data: \"STOP\"}]}\n\n"
	resp, err := parseGoogleSSE(strings.NewReader(body), time.Second, context.Background(), nil,
		Request{Model: googleTestModel("gemini-2.5-flash", "google"), Thinking: "off"})
	if err != nil || resp.StopReason != "stop" {
		t.Fatalf("multi-line Google SSE = %+v, %v", resp, err)
	}
}

func TestGoogleErrorsOnUnsafeBaseURLAndModelPath(t *testing.T) {
	for _, tc := range []struct{ baseURL, model string }{
		{"https://user:secret@example.com/v1beta", "gemini"},
		{"https://example.com/v1beta?x=1", "gemini"},
		{"file:///etc/passwd", "gemini"},
		{"https://example.com/v1beta", "../secret"},
	} {
		if _, err := googleStreamEndpoint(tc.baseURL, tc.model); err == nil {
			t.Errorf("unsafe Google endpoint accepted: %q model %q", tc.baseURL, tc.model)
		}
	}
}

func TestGoogleHTTPRetryableStatusesInclude408And409(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusConflict} {
		resp := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret"))}
		err := classifyHTTPError(resp)
		var providerErr *ProviderError
		if !errors.As(err, &providerErr) || providerErr.Kind != ErrTransport || strings.Contains(err.Error(), "secret") {
			t.Fatalf("Google retryable status %d = %v", status, err)
		}
	}
}

func TestGoogleStreamResponseAllowsNoCandidatesInUsageChunk(t *testing.T) {
	body := googleChunk(t, map[string]any{"usageMetadata": map[string]any{"promptTokenCount": 1, "totalTokenCount": 1}}) +
		googleChunk(t, map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP"}}})
	resp, err := parseGoogleSSE(strings.NewReader(body), time.Second, context.Background(), nil,
		Request{Model: googleTestModel("gemini-2.5-flash", "google"), Thinking: "off"})
	if err != nil || resp.Usage.Input != 1 || resp.StopReason != "stop" {
		t.Fatalf("usage-only Google chunk = %+v, %v", resp, err)
	}
}

func TestGoogleBuildRequestWithoutMessagesIsModelCapabilityOnly(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash", "google")
	if _, err := BuildRequest(Request{Model: model, Thinking: "off"}); err != nil {
		t.Fatalf("Google model capability check unexpectedly requires messages: %v", err)
	}
	client := NewGoogleClient("https://example.invalid/v1beta", "key", 0, time.Second)
	if _, err := client.Send(context.Background(), Request{Model: model, Thinking: "off"}, nil); err == nil || !strings.Contains(err.Error(), "conversation content") {
		t.Fatalf("Google runtime accepted empty contents: %v", err)
	}
}

func TestGoogleStreamErrorPathDoesNotLeakMalformedPayload(t *testing.T) {
	secret := "synthetic-private-provider-body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+secret+"\n\n")
	}))
	defer server.Close()
	model := googleTestModel("gemini-2.5-flash", "google")
	client := NewGoogleClient(server.URL, "key", 0, time.Second)
	_, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
	client.CloseIdle()
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("malformed Google stream leaked response text: %v", err)
	}
}

func TestGoogleStreamBuilderErrorsAreStable(t *testing.T) {
	if _, err := googleStreamEndpoint("https://example.com/v1beta", "gemini:test"); err != nil {
		t.Fatalf("valid model punctuation rejected: %v", err)
	}
	model := googleTestModel("gemini-2.5-flash", "google")
	model.Compat = completionCompat(t, map[string]any{"supportsStore": false})
	if _, err := BuildGoogleRequest(Request{Model: model, Thinking: "off"}); err == nil {
		t.Fatal("OpenAI-only compatibility field was accepted for Google")
	}
}

func TestGoogleContextCancellationErrorJoinsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := parseGoogleSSE(strings.NewReader(""), 0, ctx, nil, Request{Model: googleTestModel("gemini-2.5-flash", "google")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Google parser cancellation = %v", err)
	}
}

func TestGoogleSchemaRewriteIsNonMutating(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "const": "a"}}}
	before, _ := json.Marshal(schema)
	result := rewriteGoogleConstAsEnum(schema, false)
	after, _ := json.Marshal(schema)
	if string(before) != string(after) {
		t.Fatalf("Google schema conversion mutated source: %s != %s", before, after)
	}
	if result.(map[string]any)["properties"].(map[string]any)["path"].(map[string]any)["enum"].([]any)[0] != "a" {
		t.Fatalf("Google schema const rewrite = %#v", result)
	}
}

func TestGoogleUsageRejectsImpossibleCounts(t *testing.T) {
	body := `data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"cachedContentTokenCount":2,"totalTokenCount":2}}` + "\n\n"
	_, err := parseGoogleSSE(strings.NewReader(body), time.Second, context.Background(), nil,
		Request{Model: googleTestModel("gemini-2.5-flash", "google"), Thinking: "off"})
	if err == nil || !strings.Contains(err.Error(), "usage metadata") {
		t.Fatalf("impossible Google usage was accepted: %v", err)
	}
}

func TestGoogleToolResultImagesOnlyForImageModels(t *testing.T) {
	content, images, err := googleToolResultContent([]Block{{Type: "image", MimeType: "image/png", Data: "AA"}}, false)
	if err != nil || content != "(tool image omitted: model does not support images)" || len(images) != 0 {
		t.Fatalf("Google unsupported tool image = %q %#v %v", content, images, err)
	}
	content, images, err = googleToolResultContent([]Block{{Type: "image", MimeType: "image/png", Data: "AA"}}, true)
	if err != nil || content != "" || len(images) != 1 {
		t.Fatalf("Google supported tool image = %q %#v %v", content, images, err)
	}
}

func TestGoogleToolIdNormalizationMatchesPiUtf16Bound(t *testing.T) {
	if got := normalizeGoogleToolID("x😀"); got != "x__" {
		t.Fatalf("Google ID normalization for non-BMP characters = %q", got)
	}
	if got := normalizeGoogleToolID(strings.Repeat("a", 63) + "😀"); got != strings.Repeat("a", 63)+"_" {
		t.Fatalf("Google ID normalization did not apply the 64-unit bound: %q", got)
	}
}

func TestGoogleToolIdPolicyUsesPinnedModelFamilies(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"gemini-2.5-flash", false}, {"gemini-3.1-pro-preview", true}, {"gemini-live-3-preview", true},
		{"claude-sonnet-4-5", true}, {"gpt-oss-120b", true}, {"gemma-4-26b-a4b-it", false},
	} {
		if got := googleRequiresToolCallID(tc.model); got != tc.want {
			t.Errorf("Google tool ID policy for %s = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestGoogleResponseStopMapping(t *testing.T) {
	for _, tc := range []struct{ reason, want string }{{"STOP", "stop"}, {"MAX_TOKENS", "length"}, {"SAFETY", "error"}, {"", "error"}} {
		if got := googleStopReason(tc.reason); got != tc.want {
			t.Errorf("Google finish %q = %q, want %q", tc.reason, got, tc.want)
		}
	}
}

func TestGoogleProviderAPIValidation(t *testing.T) {
	model := googleTestModel("m", "opencode-go")
	if _, err := BuildGoogleRequest(Request{Model: model, Thinking: "off"}); err != nil {
		t.Fatal(err)
	}
	model.Provider = "deepseek"
	if _, err := BuildGoogleRequest(Request{Model: model, Thinking: "off"}); err == nil {
		t.Fatal("DeepSeek was allowed to use Google API")
	}
}

func TestGoogleUsageCountsDoNotInventFromTotal(t *testing.T) {
	body := `data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":99}}` + "\n\n"
	resp, err := parseGoogleSSE(strings.NewReader(body), time.Second, context.Background(), nil,
		Request{Model: googleTestModel("gemini-2.5-flash", "google"), Thinking: "off"})
	if err != nil || resp.Usage.Input != 0 || resp.Usage.Output != 0 || resp.Usage.TotalTokens != 99 {
		t.Fatalf("Google total-only usage was guessed: %+v %v", resp, err)
	}
}

func TestGoogleNoRetryAfterMalformedStream(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"candidates\":[\n\n")
	}))
	defer server.Close()
	model := googleTestModel("gemini-2.5-flash", "google")
	client := NewGoogleClient(server.URL, "key", 5, time.Second)
	_, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}}, nil)
	client.CloseIdle()
	if err == nil || attempts.Load() != 1 {
		t.Fatalf("malformed Google stream retry = %v attempts %d", err, attempts.Load())
	}
}

func TestGoogleErrorMessageDoesNotIncludeBaseURLCredentials(t *testing.T) {
	if _, err := googleStreamEndpoint("https://user:password@example.com/v1beta", "gemini-2.5-flash"); err == nil || strings.Contains(err.Error(), "password") {
		t.Fatalf("Google endpoint error exposed credentials: %v", err)
	}
}

func TestGoogleToolCallFunctionArgumentsObjectOnly(t *testing.T) {
	for _, raw := range []string{`[]`, `"str"`, `null`} {
		body := fmt.Sprintf(`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":%s}}]},"finishReason":"STOP"}]}`, raw) + "\n\n"
		resp, err := parseGoogleSSE(strings.NewReader(body), time.Second, context.Background(), nil,
			Request{Model: googleTestModel("gemini-2.5-flash", "google"), Thinking: "off"})
		if raw == "null" {
			if err != nil || len(resp.ToolCalls) != 1 || len(resp.ToolCalls[0].Arguments) != 0 {
				t.Errorf("null Google args = %+v, %v", resp, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "function arguments") || len(resp.ToolCalls) != 0 {
			t.Errorf("non-object Google args %s accepted: %+v %v", raw, resp, err)
		}
	}
}
