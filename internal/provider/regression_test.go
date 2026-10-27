package provider

import (
	"context"
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

func TestStreamingPreservesSignaturesOrderAndUsage(t *testing.T) {
	data := `event: message_start
data: {"message":{"id":"response-1","usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"index":0,"delta":{"type":"thinking_delta","thinking":"thought"}}

event: content_block_delta
data: {"index":0,"delta":{"type":"signature_delta","signature":"signed-payload"}}

event: content_block_stop
data: {"index":0}

event: content_block_start
data: {"index":1,"content_block":{"type":"text","text":"answer"}}

event: content_block_stop
data: {"index":1}

event: message_delta
data: {"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {}

`
	resp, err := parseSSE(strings.NewReader(data), time.Second, context.Background(), nil, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 2 || resp.Content[0].ThinkingSignature != "signed-payload" || resp.Content[1].Text != "answer" {
		t.Fatalf("content: %+v", resp.Content)
	}
	if resp.Usage.Input != 10 || resp.Usage.CacheRead != 20 || resp.Usage.CacheWrite != 30 || resp.Usage.TotalTokens != 65 {
		t.Fatalf("usage lost at message_delta: %+v", resp.Usage)
	}
	if _, err := parseSSE(strings.NewReader(strings.Split(data, "event: message_stop")[0]), time.Second, context.Background(), nil, Request{}); err == nil {
		t.Fatal("truncated stream accepted as a complete response")
	}
}

func TestPartialStreamIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		io.WriteString(w, "event: message_start\ndata: {\"message\":{\"id\":\"r\"}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"partial\"}}\n\n")
	}))
	defer srv.Close()
	client := NewClient(srv.URL+"/v1", "test-key", 3, time.Second)
	defer client.CloseIdle()
	var text strings.Builder
	_, err := client.Send(context.Background(), Request{Model: ModelMeta{ID: "m"}, Thinking: "off"}, func(s string) { text.WriteString(s) })
	if err == nil || calls.Load() != 1 || text.String() != "partial" {
		t.Fatalf("partial replay: calls=%d text=%q err=%v", calls.Load(), text.String(), err)
	}
}

func TestStreamIdleTimeoutClosesBlockedBody(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	start := time.Now()
	_, err := parseSSE(pr, 20*time.Millisecond, context.Background(), nil, Request{})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("idle timeout: %v", err)
	}
	if _, err := pw.Write([]byte("x")); err == nil {
		t.Fatal("blocked body was not closed")
	}
}

func TestCredentialTypeAndPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{"ANTHROPIC_API_KEY": "env-key", "FILE_KEY": "stored-key"}
	write(`{"anthropic":{"type":"api_key","key":"$FILE_KEY"},"other":{"type":"oauth"}}`)
	key, _, err := SelectAPIKey(env, path)
	if err != nil || key != "stored-key" {
		t.Fatalf("stored credential precedence: %q %v", key, err)
	}
	write(`{"anthropic":{"type":"oauth","access":"secret"},"other":{"type":"api_key","key":"not-anthropic"}}`)
	if _, _, err := SelectAPIKey(env, path); err == nil || !strings.Contains(err.Error(), "OAuth") {
		t.Fatalf("silently fell back across billing paths: %v", err)
	}
	write(`{"anthropic":{"type":"api_key","key":"!echo dangerous"}}`)
	if _, _, err := SelectAPIKey(env, path); err == nil {
		t.Fatal("credential command accepted as a key")
	}
	write(`{"my-anthropic-proxy":{"type":"api_key","key":"wrong-account"}}`)
	key, _, err = SelectAPIKey(env, path)
	if err != nil || key != "env-key" {
		t.Fatalf("selected an unrelated credential: %q %v", key, err)
	}
}

func TestToolResultsGroupedWithErrorFlags(t *testing.T) {
	out, err := BuildAnthropicRequest(Request{Model: ModelMeta{ID: "m"}, Messages: []Message{
		{Role: "toolResult", ToolCallID: "a", IsError: true, Content: []Block{{Type: "text", Text: "failed"}}},
		{Role: "toolResult", ToolCallID: "b", Content: []Block{{Type: "text", Text: "ok"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]map[string]any)
	if len(msgs) != 1 {
		t.Fatalf("tool batch not grouped: %+v", msgs)
	}
	blocks := msgs[0]["content"].([]any)
	if len(blocks) != 2 || blocks[0].(map[string]any)["is_error"] != true {
		t.Fatal("lost tool-result error flag")
	}
}
