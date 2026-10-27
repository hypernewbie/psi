package provider

// Phase F regressions for PLAN3 F12: failed streams carry known provider
// usage through one explicit partial-result path instead of discarding it.
//
// Matrices: failure before headers, after message_start, during text,
// reasoning, tool arguments; cancellation, timeout, malformed SSE, clean EOF
// without completion. Known input/cache usage persists; output is never
// invented; no retry follows partial output; no unfinished tool executes.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func mustPartial(t *testing.T, chunks []string) (*Response, error) {
	t.Helper()
	resp, err := parseSSE(sseStream(chunks), 5*time.Second, context.Background(), nil, Request{Model: ModelMeta{ID: "claude-x"}})
	if err == nil {
		t.Fatal("expected stream failure")
	}
	if resp == nil {
		t.Fatal("partial response discarded")
	}
	return resp, err
}

func TestF12UsageAfterMessageStartSurvives(t *testing.T) {
	// message_start reports input usage 777; the stream then breaks.
	resp, _ := mustPartial(t, []string{
		"event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":777,\"cache_read_input_tokens\":100}}}\n\n",
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n",
		// No text delta, no completion: connection drops.
	})
	se := &StreamInterrupt{Partial: resp, Err: errors.New("cut")}
	if !se.KnownUsage() {
		t.Fatal("known usage not detected")
	}
	if resp.Usage.Input != 777 || resp.Usage.CacheRead != 100 {
		t.Fatalf("usage lost: %+v", resp.Usage)
	}
	if resp.Usage.Output != 0 {
		t.Fatalf("output invented: %+v", resp.Usage)
	}
}

func TestF12FailurePoints(t *testing.T) {
	start := "event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":5}}}\n\n"
	textStart := start + "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n"
	thinkStart := start + "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n"
	toolStart := start +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"c1\",\"name\":\"read\"}}\n\n" +
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\"\"}}\n\n"
	for _, tc := range []struct {
		name   string
		chunks []string
	}{
		{"during-text", []string{textStart + "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"half\"}}\n\n"}},
		{"during-reasoning", []string{thinkStart + "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"half\"}}\n\n"}},
		{"during-tool-args", []string{toolStart}},
		{"malformed-sse", []string{textStart + "event: content_block_delta\ndata: {not json\n\n"}},
		{"clean-eof", []string{textStart}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := mustPartial(t, tc.chunks)
			_ = err
			if resp.Usage.Input != 5 {
				t.Fatalf("input usage lost at %s: %+v", tc.name, resp.Usage)
			}
			for _, b := range resp.Content {
				if b.Type == "toolCall" {
					t.Fatalf("unfinished tool call promoted at %s", tc.name)
				}
			}
			if len(resp.ToolCalls) != 0 {
				t.Fatalf("tool calls executable at %s", tc.name)
			}
		})
	}
}

func TestF12NoRetryAfterPartialOutput(t *testing.T) {
	calls := 0
	_, err := sendWithRetry(context.Background(), 3, func() (*Response, error) {
		calls++
		return &Response{Usage: Usage{Input: 3}}, &StreamInterrupt{
			Partial: &Response{Usage: Usage{Input: 3}}, Err: errors.New("cut"),
		}
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var se *StreamInterrupt
	if !errors.As(err, &se) || !se.KnownUsage() {
		t.Fatalf("partial did not propagate: %v", err)
	}
	if calls != 1 {
		t.Fatalf("retried after partial output: %d calls", calls)
	}
}

func TestF12CancelledStreamKeepsKnownUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":42}}}\n\n"))
		cancel()
		pw.Close()
	}()
	resp, err := parseSSE(pr, 5*time.Second, ctx, nil, Request{Model: ModelMeta{ID: "claude-x"}})
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if resp == nil || resp.Usage.Input != 42 {
		t.Fatalf("usage lost on cancel: %+v", resp)
	}
	if !strings.Contains(err.Error(), "incomplete") && !strings.Contains(err.Error(), "context") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestF12CodexTerminalUsageSurvivesAssemblyFailure(t *testing.T) {
	// Backend supplies usage in the terminal event; output assembly then
	// fails on an unfinished item. The partial must carry the usage.
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","content":[]}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":7,"total_tokens":107}}}`,
		"",
	}, "\n") + "\n"
	resp, err := parseCodexSSE(strings.NewReader(stream), 5*time.Second, context.Background(), nil, Request{Model: ModelMeta{ID: "codex"}})
	if err == nil {
		t.Fatal("expected assembly failure")
	}
	if resp == nil {
		t.Fatal("partial discarded")
	}
	if resp.Usage.Input != 100 || resp.Usage.Output != 7 {
		t.Fatalf("usage lost: %+v", resp.Usage)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatal("tool calls from failed assembly")
	}
}
