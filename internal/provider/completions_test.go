package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/modelmeta"
)

func completionsTestModel(providerID, modelID string) ModelMeta {
	return ModelMeta{ID: modelID, Provider: providerID, API: "openai-completions", BaseURL: "https://example.invalid/v1", MaxTokens: 4000,
		Compat: modelmeta.Compat{}}
}

func TestOpenCodeCompletionsLoopbackPathHeadersStreamAndUsage(t *testing.T) {
	var requestBody map[string]any
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/inference/go/openai/v1/chat/completions" {
			t.Errorf("request path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request method/headers = %s %#v", r.Method, r.Header)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-key" || r.Header.Get("X-OpenCode-Session") != "synthetic-session" {
			t.Errorf("request auth/session headers = %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"id\":\"resp-synthetic\",\"model\":\"served-model\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"why\"},\"finish_reason\":null}]}\n\n"+
				"data: {\"id\":\"resp-synthetic\",\"model\":\"served-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer \"},\"finish_reason\":null}]}\n\n"+
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-a\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\"}}]},\"finish_reason\":null}]}\n\n"+
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call-b\",\"type\":\"function\",\"function\":{\"name\":\"write\",\"arguments\":\"{\\\"path\\\":\\\"b\\\",\\\"content\\\":\\\"x\\\"}\"}},{\"index\":0,\"function\":{\"arguments\":\"\\\"a.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\n"+
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"prompt_tokens_details\":{\"cached_tokens\":2,\"cache_write_tokens\":1},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"+
				"data: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewCompletionsClient(server.URL+"/inference/go/openai/v1", "synthetic-key", 0, time.Second)
	client.SessionID = "synthetic-session"
	model := completionsTestModel("opencode-go", "open-code-synthetic")
	model.Compat = modelmeta.Compat{"maxTokensField": json.RawMessage(`"max_tokens"`), "supportsStore": json.RawMessage(`false`), "supportsStrictMode": json.RawMessage(`true`)}
	var streamed strings.Builder
	response, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", System: "rules", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}}, func(delta string) { streamed.WriteString(delta) })
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || response.ResponseID != "resp-synthetic" || response.Model != "served-model" || response.StopReason != "toolUse" {
		t.Fatalf("response identity/settlement = %+v, requests %d", response, requests.Load())
	}
	if response.Text != "answer " || response.Thinking != "why" || streamed.String() != "answer " || response.Content[0].ThinkingSignature != "reasoning_content" {
		t.Fatalf("response content = %+v, deltas %q", response.Content, streamed.String())
	}
	if len(response.ToolCalls) != 2 || response.ToolCalls[0].ID != "call-a" || response.ToolCalls[0].Arguments["path"] != "a.txt" || response.ToolCalls[1].Arguments["content"] != "x" {
		t.Fatalf("interleaved tool calls = %+v", response.ToolCalls)
	}
	if response.Usage.Input != 7 || response.Usage.Output != 4 || response.Usage.CacheRead != 2 || response.Usage.CacheWrite != 1 || response.Usage.TotalTokens != 14 || response.Usage.Reasoning == nil || *response.Usage.Reasoning != 1 {
		t.Fatalf("reported usage = %+v", response.Usage)
	}
	if requestBody["model"] != model.ID || requestBody["max_tokens"] != float64(model.MaxTokens) || requestBody["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("wire request fields = %#v", requestBody)
	}
}

func TestOpenCodeCompletionsExplicitSessionHeaderWinsCaseInsensitively(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-OpenCode-Session") != "explicit-session" {
			t.Errorf("explicit session header was replaced: %#v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	client := NewCompletionsClient(server.URL, "key", 0, time.Second)
	client.SessionID = "session-default"
	client.Headers = map[string]string{"X-OPENCode-SESSION": "explicit-session"}
	model := completionsTestModel("opencode-go", "model")
	model.Compat["supportsFinishReason"] = json.RawMessage(`true`)
	if _, err := client.Send(context.Background(), Request{Model: model}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionsStreamRejectsMalformedMissingFinishAndPartialTools(t *testing.T) {
	model := completionsTestModel("deepseek", "model")
	model.Compat["supportsFinishReason"] = json.RawMessage(`true`)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"malformed", "data: {not-json}\n\n", "malformed"},
		{"truncated JSON", "data: {\"choices\":[", "malformed"},
		{"missing finish", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n", "without finish_reason"},
		{"unfinished tool", "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-x\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n", "invalid completed OpenAI Completions tool arguments"},
		{"bad tool arguments", "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-x\",\"function\":{\"name\":\"read\",\"arguments\":\"[]\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n", "invalid completed OpenAI Completions tool arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := parseCompletionsSSE(strings.NewReader(tc.body), time.Second, context.Background(), nil, Request{Model: model, Thinking: "off"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parse error = %v, want %q", err, tc.want)
			}
			if response != nil && len(response.ToolCalls) != 0 {
				t.Fatalf("unfinished tool reached partial response: %+v", response.ToolCalls)
			}
		})
	}
}

func TestCompletionsFinishReasonCompatibilityAndLength(t *testing.T) {
	model := completionsTestModel("opencode-go", "model")
	model.Compat["supportsFinishReason"] = json.RawMessage(`false`)
	response, err := parseCompletionsSSE(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"), time.Second, context.Background(), nil, Request{Model: model, Thinking: "off"})
	if err != nil || response.StopReason != "stop" || response.Text != "ok" {
		t.Fatalf("compatible no-finish response = %+v, %v", response, err)
	}
	model.Compat["supportsFinishReason"] = json.RawMessage(`true`)
	response, err = parseCompletionsSSE(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"), time.Second, context.Background(), nil, Request{Model: model, Thinking: "off"})
	if err != nil || response.StopReason != "length" {
		t.Fatalf("length finish reason = %+v, %v", response, err)
	}
}

func TestCompletionsRetryOnlyBeforeStreamAndNeverRetryPartial(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "synthetic", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	client := NewCompletionsClient(server.URL, "key", 1, time.Second)
	model := completionsTestModel("deepseek", "model")
	model.Compat["supportsFinishReason"] = json.RawMessage(`true`)
	response, err := client.Send(context.Background(), Request{Model: model, Thinking: "off"}, nil)
	server.Close()
	if err != nil || response.Text != "ok" || count.Load() != 2 {
		t.Fatalf("pre-stream retry = response %+v error %v attempts %d", response, err, count.Load())
	}

	started := make(chan struct{})
	partialServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	partialClient := NewCompletionsClient(partialServer.URL, "key", 3, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	gotDelta := make(chan struct{}, 1)
	go func() {
		_, err := partialClient.Send(ctx, Request{Model: model, Thinking: "off"}, func(string) {
			select {
			case gotDelta <- struct{}{}:
			default:
			}
		})
		done <- err
	}()
	<-started
	select {
	case <-gotDelta:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("completion stream did not deliver its initial delta")
	}
	cancel()
	select {
	case err := <-done:
		var interrupted *StreamInterrupt
		if !errors.As(err, &interrupted) || !strings.Contains(err.Error(), "not retried") {
			t.Fatalf("partial stream error = %T %v", err, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled completion stream did not return")
	}
	partialServer.Close()

	var truncatedAttempts atomic.Int32
	truncatedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		truncatedAttempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
	}))
	truncatedClient := NewCompletionsClient(truncatedServer.URL, "key", 3, time.Second)
	partial, err := truncatedClient.Send(context.Background(), Request{Model: model, Thinking: "off"}, nil)
	truncatedServer.Close()
	var interrupted *StreamInterrupt
	if !errors.As(err, &interrupted) || truncatedAttempts.Load() != 1 || partial == nil || partial.Text != "partial" {
		t.Fatalf("truncated stream retry/partial = response %+v error %v attempts %d", partial, err, truncatedAttempts.Load())
	}
}

func TestCompletionsRejectsUnsupportedProviderAPI(t *testing.T) {
	body, err := BuildCompletionsRequest(Request{Model: ModelMeta{ID: "m", API: "openai-completions", Provider: "openai-codex"}, Thinking: "off"})
	if err == nil || body != nil {
		t.Fatalf("unsupported provider/API pair produced request: %#v, %v", body, err)
	}
}
