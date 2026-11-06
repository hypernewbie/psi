package provider

import (
	"context"
	"strings"
	"testing"
	"time"
)

// collectSSE deltas drives the Anthropic SSE parser and returns the typed
// deltas in arrival order.
func collectSSE(t *testing.T, chunks []string) ([]Delta, *Response) {
	t.Helper()
	var deltas []Delta
	resp, err := parseSSE(sseStream(chunks), 5*time.Second, context.Background(), func(d Delta) {
		deltas = append(deltas, d)
	}, Request{Model: ModelMeta{ID: "claude-x"}})
	if err != nil {
		t.Fatal(err)
	}
	return deltas, resp
}

func TestAnthropicThinkingDeltasStaySeparateFromText(t *testing.T) {
	chunks := []string{
		"event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"model\":\"claude-x\"}}\n\n",
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"weigh \"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"options\"}}\n\n",
		"event: content_block_stop\ndata: {\"index\":0}\n\n",
		"event: content_block_start\ndata: {\"index\":1,\"content_block\":{\"type\":\"text\"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n",
		"event: content_block_stop\ndata: {\"index\":1}\n\n",
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	}
	deltas, resp := collectSSE(t, chunks)
	var thinking, text strings.Builder
	for _, d := range deltas {
		switch d.Kind {
		case DeltaThinking:
			thinking.WriteString(d.Text)
		case DeltaText:
			text.WriteString(d.Text)
		default:
			t.Fatalf("unknown delta kind %q", d.Kind)
		}
	}
	if thinking.String() != "weigh options" {
		t.Fatalf("thinking deltas = %q, want %q", thinking.String(), "weigh options")
	}
	if text.String() != "answer" {
		t.Fatalf("text deltas = %q, want %q", text.String(), "answer")
	}
	if resp.Thinking != "weigh options" {
		t.Fatalf("resp.Thinking = %q", resp.Thinking)
	}
	if resp.Text != "answer" {
		t.Fatalf("resp.Text = %q", resp.Text)
	}
}

func TestAnthropicEmptyThinkingBlockEmitsNoDelta(t *testing.T) {
	chunks := []string{
		"event: message_start\ndata: {\"message\":{\"id\":\"m1\"}}\n\n",
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"\"}}\n\n",
		"event: content_block_stop\ndata: {\"index\":0}\n\n",
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	}
	deltas, resp := collectSSE(t, chunks)
	for _, d := range deltas {
		if d.Kind == DeltaThinking {
			t.Fatalf("empty thinking produced delta %q", d.Text)
		}
	}
	if resp.Thinking != "" {
		t.Fatalf("resp.Thinking = %q, want empty", resp.Thinking)
	}
}

func TestAnthropicThinkingDeltaInTextBlockIsRejected(t *testing.T) {
	chunks := []string{
		"event: message_start\ndata: {\"message\":{\"id\":\"m1\"}}\n\n",
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n",
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"nope\"}}\n\n",
		"event: content_block_stop\ndata: {\"index\":0}\n\n",
		"event: message_stop\ndata: {}\n\n",
	}
	var deltas []Delta
	_, err := parseSSE(sseStream(chunks), 5*time.Second, context.Background(), func(d Delta) {
		deltas = append(deltas, d)
	}, Request{Model: ModelMeta{ID: "claude-x"}})
	if err == nil {
		t.Fatal("expected an error for a thinking delta in a text block")
	}
	if len(deltas) != 0 {
		t.Fatalf("deltas = %v, want none", deltas)
	}
}

func TestCompletionsThinkingDeltaIsTypedAndNotDuplicated(t *testing.T) {
	chunks := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"weigh\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"type\":\"reasoning.text\",\"text\":\" options\"}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	}
	var deltas []Delta
	_, err := parseCompletionsSSE(sseStream(chunks), 5*time.Second, context.Background(), func(d Delta) {
		deltas = append(deltas, d)
	}, Request{Model: ModelMeta{ID: "local", API: "openai-completions", Provider: "deepseek"}})
	if err != nil {
		t.Fatal(err)
	}
	var thinking, text strings.Builder
	for _, d := range deltas {
		switch d.Kind {
		case DeltaThinking:
			thinking.WriteString(d.Text)
		case DeltaText:
			text.WriteString(d.Text)
		}
	}
	if thinking.String() != "weigh options" {
		t.Fatalf("thinking deltas = %q, want %q", thinking.String(), "weigh options")
	}
	if text.String() != "answer" {
		t.Fatalf("text deltas = %q, want %q", text.String(), "answer")
	}
}
