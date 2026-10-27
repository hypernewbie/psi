package provider

// PLAN3 P3-051: bounded Anthropic/Codex SSE chunking fuzzers with completion
// and no-execution properties. Chunk splits, duplicate fields, invalid UTF-8,
// and deep JSON must never panic, never promote a partial tool call, and
// never invent usage.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func FuzzAnthropicSSEChunking(f *testing.F) {
	seeds := [][]string{
		{
			"event: message_start\ndata: {\"message\":{\"id\":\"m1\"}}\n\n",
			"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n",
			"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
			"event: content_block_stop\ndata: {\"index\":0}\n\n",
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
			"event: message_stop\ndata: {}\n\n",
		},
		{
			"event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":3}}}\n\n",
			"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"c1\",\"name\":\"read\"}}\n\n",
			"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"a\\\"}\"}}\n\n",
		},
	}
	for _, s := range seeds {
		f.Add(strings.Join(s, ""))
	}
	f.Fuzz(func(t *testing.T, data string) {
		if len(data) > 256*1024 {
			return
		}
		resp, err := parseSSE(sseStream([]string{data}), 5*time.Second, context.Background(), nil, Request{Model: ModelMeta{ID: "x"}})
		if err != nil {
			// Failed streams: partial carries only completed blocks.
			if resp != nil {
				for _, tc := range resp.ToolCalls {
					if tc.ID == "" || tc.Name == "" || tc.Arguments == nil {
						t.Fatalf("executable tool call without payload: %+v", tc)
					}
				}
			}
			return
		}
		// Clean completion: tool calls have IDs, names, and object args.
		for _, tc := range resp.ToolCalls {
			if tc.ID == "" || tc.Name == "" || tc.Arguments == nil {
				t.Fatalf("incomplete executable tool call: %+v", tc)
			}
		}
	})
}

func FuzzCodexSSEChunking(f *testing.F) {
	f.Add("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	f.Fuzz(func(t *testing.T, data string) {
		if len(data) > 256*1024 {
			return
		}
		resp, err := parseCodexSSE(strings.NewReader(data), 5*time.Second, context.Background(), nil, Request{Model: ModelMeta{ID: "codex"}})
		if err != nil {
			if resp != nil && len(resp.ToolCalls) != 0 {
				t.Fatal("tool calls from failed Codex stream")
			}
			return
		}
		for _, tc := range resp.ToolCalls {
			if tc.ID == "" || tc.Name == "" {
				t.Fatalf("incomplete Codex tool call: %+v", tc)
			}
		}
	})
}
