package provider

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReviewCompletionsLinearReasoningDetails(t *testing.T) {
	measure := func(n int) uint64 {
		chunk := `data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"abcdefghijklmnop"}]}}]}` + "\n\n"
		data := strings.Repeat(chunk, n) + `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		r, err := parseCompletionsSSE(strings.NewReader(data), time.Second, context.Background(), nil, Request{Model: ModelMeta{Provider: "opencode-go", API: "openai-completions", ID: "synthetic"}})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Content) != 1 {
			t.Fatalf("unexpected content: %+v", r.Content)
		}
		var details []map[string]any
		if err := json.Unmarshal([]byte(r.Content[0].ThinkingSignature), &details); err != nil || len(details) != 1 || details[0]["text"] != strings.Repeat("abcdefghijklmnop", n) {
			t.Fatalf("reasoning accumulation changed content: %v", err)
		}
		return after.TotalAlloc - before.TotalAlloc
	}
	one, ten := measure(1000), measure(10000)
	t.Logf("1k=%d 10k=%d ratio=%.2f", one, ten, float64(ten)/float64(one))
	if ten > 15*one {
		t.Fatalf("quadratic reasoning-detail accumulation: %d > 15x %d", ten, one)
	}
}

func TestReviewGoogleLinearAccumulation(t *testing.T) {
	for _, kind := range []string{"text", "thinking"} {
		t.Run(kind, func(t *testing.T) {
			measure := func(n int) uint64 {
				part := `{"text":"abcdefghijklmnop"}`
				if kind == "thinking" {
					part = `{"text":"abcdefghijklmnop","thought":true}`
				}
				chunk := "data: {\"candidates\":[{\"content\":{\"parts\":[" + part + "]}}]}\n\n"
				data := strings.Repeat(chunk, n) + "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				r, err := parseGoogleSSE(strings.NewReader(data), time.Second, context.Background(), nil, Request{})
				runtime.ReadMemStats(&after)
				if err != nil {
					t.Fatal(err)
				}
				if r.Text+r.Thinking != strings.Repeat("abcdefghijklmnop", n) {
					t.Fatal("accumulation changed content")
				}
				return after.TotalAlloc - before.TotalAlloc
			}
			one, ten := measure(1000), measure(10000)
			t.Logf("1k=%d 10k=%d ratio=%.2f", one, ten, float64(ten)/float64(one))
			if ten > 15*one {
				t.Fatalf("quadratic Google accumulation: %d > 15x %d", ten, one)
			}
		})
	}
}
