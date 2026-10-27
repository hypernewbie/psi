package provider

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func sseStream(chunks []string) io.Reader {
	// Simulate fragmented TCP reads: join then split into tiny pieces.
	full := strings.Join(chunks, "")
	parts := []string{}
	for i := 0; i < len(full); i += 7 {
		end := i + 7
		if end > len(full) {
			end = len(full)
		}
		parts = append(parts, full[i:end])
	}
	pr, pw := io.Pipe()
	go func() {
		for _, p := range parts {
			pw.Write([]byte(p))
		}
		pw.Close()
	}()
	return pr
}

func TestParseSSEFragmented(t *testing.T) {
	chunks := []string{
		"event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"model\":\"claude-x\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo 🌍\"}}\n\n",
		"event: content_block_stop\ndata: {\"index\":0}\n\n",
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":5}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	}
	var deltas []string
	resp, err := parseSSE(sseStream(chunks), 5*time.Second, context.Background(), func(s string) { deltas = append(deltas, s) }, Request{Model: ModelMeta{ID: "claude-x"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello 🌍" {
		t.Errorf("text=%q deltas=%v", resp.Text, deltas)
	}
	if resp.StopReason != "stop" || resp.Usage.Output != 5 {
		t.Errorf("resp=%+v", resp)
	}
}

func TestParseSSEToolCalls(t *testing.T) {
	chunks := []string{
		"event: message_start\ndata: {\"message\":{\"id\":\"m1\"}}\n\n",
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"doing\"}}\n\n",
		"event: content_block_stop\ndata: {\"index\":0}\n\n",
		"event: content_block_start\ndata: {\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"c1\",\"name\":\"read\"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\": \"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"a\\\"}\" }}\n\n",
		"event: content_block_stop\ndata: {\"index\":1}\n\n",
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	}
	resp, err := parseSSE(sseStream(chunks), 5*time.Second, context.Background(), nil, Request{Model: ModelMeta{ID: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Arguments["path"] != "a" {
		t.Errorf("calls=%+v", resp.ToolCalls)
	}
	if resp.StopReason != "toolUse" {
		t.Errorf("stop=%q", resp.StopReason)
	}
}

func TestParseSSEIncompleteToolRejected(t *testing.T) {
	chunks := []string{
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"c1\",\"name\":\"read\"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\": \"}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	}
	if _, err := parseSSE(sseStream(chunks), 5*time.Second, context.Background(), nil, Request{}); err == nil {
		t.Errorf("incomplete tool must be ineligible")
	}
}

func TestParseSSEUsageOnlyAndMultiline(t *testing.T) {
	// Multiline data record split across reads.
	payload := "{\"message\":{\"id\":\"m9\"}}"
	chunks := []string{"event: message_start\ndata: " + payload + "\n\n", "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":4}}\n\n", "event: message_stop\ndata: {}\n\n"}
	resp, err := parseSSE(sseStream(chunks), 5*time.Second, context.Background(), nil, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Errorf("usage=%+v", resp.Usage)
	}
}

func TestClassifyErrors(t *testing.T) {
	if k := (&ProviderError{Kind: ErrRateLimit}).Kind; k != ErrRateLimit {
		t.Errorf("kind")
	}
}
