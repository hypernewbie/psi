package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// All Codex fixtures are invented. No test reads a developer's Pi directory,
// copies auth.json, or obtains a real access/refresh token.
func testCodexModel() ModelMeta {
	xhigh := "xhigh"
	return ModelMeta{ID: "test-codex-model", Provider: "openai-codex", API: "openai-codex-responses", Reasoning: true, InputImage: true,
		ContextWindow: 100000, MaxTokens: 4096, ThinkingLevelMap: map[string]*string{"xhigh": &xhigh}}
}

func codexEvent(typ string, fields map[string]any) string {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = typ
	data, _ := json.Marshal(fields)
	return "event: " + typ + "\r\ndata: " + string(data) + "\r\n\r\n"
}

func codexTextStream(text string) string {
	item := map[string]any{"type": "message", "id": "msg_test", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": text}}}
	return codexEvent("response.output_item.added", map[string]any{"output_index": 0, "item": item}) +
		codexEvent("response.output_text.delta", map[string]any{"output_index": 0, "delta": text}) +
		codexEvent("response.output_item.done", map[string]any{"output_index": 0, "item": item}) +
		codexEvent("response.completed", map[string]any{"response": map[string]any{"id": "resp_test", "status": "completed", "output": []any{item},
			"usage": map[string]any{"input_tokens": 20, "output_tokens": 3, "total_tokens": 23, "input_tokens_details": map[string]any{"cached_tokens": 4}}}})
}

func writeCodexAuth(t *testing.T, expired bool) string {
	t.Helper()
	expires := time.Now().Add(time.Hour).UnixMilli()
	if expired {
		expires = 1
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	data := fmt.Sprintf(`{"openai-codex":{"type":"oauth","access":"dummy-access","refresh":"dummy-refresh","expires":%d,"accountId":"test-account","unknown":{"keep":true}},"unrelated":{"type":"api_key","key":"dummy-other-key"}}`, expires)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexRequestReplaysOpaqueItemsAndPairsToolIDs(t *testing.T) {
	reasoning := `{"type":"reasoning","id":"rs_test","summary":[],"encrypted_content":"opaque-test-ciphertext","unknown":{"retain":1}}`
	req := Request{Model: testCodexModel(), Thinking: "xhigh", System: "synthetic instructions", Messages: []Message{
		{Role: "assistant", Content: []Block{{Type: "thinking", ThinkingSignature: reasoning}, {Type: "toolCall", ID: "call_test|fc_test", Name: "read", Namespace: "functions", Arguments: map[string]any{"path": "fixture.txt"}}}},
		{Role: "toolResult", ToolCallID: "call_test|fc_test", Content: []Block{{Type: "text", Text: "synthetic result"}}},
		{Role: "assistant", Content: []Block{{Type: "text", Text: "done", TextSignature: `{"v":1,"id":"msg_test","phase":"final_answer"}`}}},
	}}
	body, err := BuildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body["store"] != false || body["reasoning"].(map[string]any)["effort"] != "xhigh" {
		t.Fatal("Codex protocol/reasoning was not selected")
	}
	if _, exists := body["max_output_tokens"]; exists {
		t.Fatal("ChatGPT rejects max_output_tokens")
	}
	input := body["input"].([]any)
	if string(input[0].(json.RawMessage)) != reasoning {
		t.Fatal("opaque reasoning was rewritten")
	}
	call, result := input[1].(map[string]any), input[2].(map[string]any)
	if call["id"] != "fc_test" || call["call_id"] != "call_test" || result["call_id"] != call["call_id"] || call["namespace"] != "functions" {
		t.Fatal("tool item and call IDs were conflated")
	}
	text := input[3].(map[string]any)
	if text["id"] != "msg_test" || text["phase"] != "final_answer" {
		t.Fatal("text replay metadata was dropped")
	}
	req.Messages[0].API, req.Messages[0].Provider = "anthropic-messages", "anthropic"
	req.Messages[0].Content[0].ThinkingSignature = "not-a-Codex-signature"
	req.Messages[0].Content[0].Thinking = "foreign thought text"
	if _, err := BuildRequest(req); err != nil {
		t.Fatal("foreign reasoning was incorrectly replayed as a Codex item")
	}
}

func TestCodexStreamReasoningBackfillAndLargeInteger(t *testing.T) {
	reasoning := map[string]any{"type": "reasoning", "id": "rs_test", "summary": []any{map[string]any{"type": "summary_text", "text": "thought"}}}
	call := map[string]any{"type": "function_call", "id": "fc_test", "call_id": "call_test", "name": "read", "namespace": "functions", "arguments": `{"offset":9007199254740993}`}
	stream := codexEvent("response.output_item.added", map[string]any{"output_index": 0, "item": reasoning}) +
		codexEvent("response.output_item.added", map[string]any{"output_index": 1, "item": call}) +
		codexEvent("response.output_item.done", map[string]any{"output_index": 1, "item": call}) +
		codexEvent("response.output_item.done", map[string]any{"output_index": 0, "item": reasoning})
	reasoning["encrypted_content"] = "opaque-terminal-only-ciphertext"
	stream += codexEvent("response.done", map[string]any{"response": map[string]any{"status": "completed", "output": []any{reasoning, call}}})
	resp, err := parseCodexSSE(strings.NewReader(stream), 0, context.Background(), nil, Request{Model: testCodexModel()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "toolUse" || len(resp.ToolCalls) != 1 || resp.Content[0].Type != "thinking" {
		t.Fatal("ordered stream output was lost")
	}
	if !strings.Contains(resp.Content[0].ThinkingSignature, "opaque-terminal-only-ciphertext") {
		t.Fatal("terminal reasoning backfill was lost")
	}
	if resp.ToolCalls[0].Arguments["offset"].(json.Number).String() != "9007199254740993" {
		t.Fatal("tool argument integer was rounded")
	}
}

func TestCodexRejectsUnfinishedToolCalls(t *testing.T) {
	item := map[string]any{"type": "function_call", "id": "fc_test", "call_id": "call_test", "name": "read", "arguments": "{}"}
	stream := codexEvent("response.output_item.added", map[string]any{"output_index": 0, "item": item}) +
		codexEvent("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{item}}})
	if _, err := parseCodexSSE(strings.NewReader(stream), 0, context.Background(), nil, Request{Model: testCodexModel()}); err == nil {
		t.Fatal("unfinished tool request was handed to execution")
	}
}

func TestCodexHTTPHeadersUsageAndSubscriptionOnly(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy-must-not-use-paid-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" || r.Header.Get("Authorization") != "Bearer dummy-access" || r.Header.Get("chatgpt-account-id") != "test-account" || r.Header.Get("originator") != "pi" {
			t.Error("subscription endpoint/auth headers were not used")
		}
		if r.Header.Get("session-id") != "test-session" || r.Header.Get("x-api-key") != "" {
			t.Error("session headers or billing path incorrect")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, codexTextStream("synthetic reply"))
	}))
	defer server.Close()
	c := NewCodexClient(server.URL+"/codex", writeCodexAuth(t, false), 0, time.Second)
	defer c.CloseIdle()
	c.SessionID = "test-session"
	var text strings.Builder
	resp, err := c.Send(context.Background(), Request{Model: testCodexModel(), Thinking: "xhigh"}, func(s string) { text.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "synthetic reply" || resp.Usage.Input != 16 || resp.Usage.CacheRead != 4 || resp.Usage.TotalTokens != 23 || resp.Content[0].TextSignature == "" {
		t.Fatal("reply/signature/usage lost")
	}
	missing := NewCodexClient(server.URL, filepath.Join(t.TempDir(), "missing.json"), 0, 0)
	defer missing.CloseIdle()
	if _, err := missing.Send(context.Background(), Request{Model: testCodexModel()}, nil); err == nil {
		t.Fatal("missing subscription silently used OPENAI_API_KEY")
	}
}

func TestCodexPartialStreamIsNeverRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, codexEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_partial"}}))
		io.WriteString(w, codexEvent("response.output_text.delta", map[string]any{"output_index": 0, "delta": "partial"}))
	}))
	defer server.Close()
	c := NewCodexClient(server.URL, writeCodexAuth(t, false), 3, time.Second)
	defer c.CloseIdle()
	if _, err := c.Send(context.Background(), Request{Model: testCodexModel()}, nil); err == nil {
		t.Fatal("partial stream accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("partial model turn was replayed")
	}
}

func TestCodexCancellationAndIdleTimeout(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRequest), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			idle := 40 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if cancelRequest {
				idle = time.Second
				time.AfterFunc(40*time.Millisecond, cancel)
			}
			c := NewCodexClient(server.URL, writeCodexAuth(t, false), 2, idle)
			defer c.CloseIdle()
			start := time.Now()
			if _, err := c.Send(ctx, Request{Model: testCodexModel()}, nil); err == nil || time.Since(start) > time.Second {
				t.Fatal("stream cancellation/idle timeout failed")
			}
		})
	}
}
