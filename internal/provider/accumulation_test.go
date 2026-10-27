package provider

// Phase F regressions for PLAN3 F08: text, thinking, signature, argument,
// and many-block accumulation grow linearly, not quadratically.
//
// Gate (S05): at most 15-fold allocation growth per tenfold workload increase,
// measured separately for text, thinking, signatures, and many-block assembly
// with fixed 16-byte deltas. Exact output is asserted at every size.

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

func deltaStream(n int, kind string, payload func(i int) string) []string {
	chunks := []string{
		"event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"model\":\"claude-x\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"" + kind + "\"}}\n\n",
	}
	for i := 0; i < n; i++ {
		chunks = append(chunks, "event: content_block_delta\ndata: {\"index\":0,\"delta\":"+payload(i)+"}\n\n")
	}
	chunks = append(chunks,
		"event: content_block_stop\ndata: {\"index\":0}\n\n",
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	)
	return chunks
}

func allocBytes(t *testing.T, chunks []string) (resp *Response, bytes uint64) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var err error
	resp, err = parseSSE(sseStream(chunks), 30*time.Second, context.Background(), nil, Request{Model: ModelMeta{ID: "claude-x"}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	return resp, after.TotalAlloc - before.TotalAlloc
}

func TestF08LinearTextAccumulation(t *testing.T) {
	make := func(n int) []string {
		return deltaStream(n, "text", func(i int) string {
			return fmt.Sprintf(`{"type":"text_delta","text":%q}`, strings.Repeat("a", 16))
		})
	}
	r1, b1 := allocBytes(t, make(1000))
	r10, b10 := allocBytes(t, make(10000))
	if r1.Text != strings.Repeat("a", 16*1000) || r10.Text != strings.Repeat("a", 16*10000) {
		t.Fatal("exact output changed")
	}
	t.Logf("1k=%d bytes 10k=%d bytes ratio=%.2f", b1, b10, float64(b10)/float64(maxU64(b1, 1)))
	if b10 > 15*b1 {
		t.Fatalf("quadratic text accumulation: 10k=%d > 15x 1k=%d", b10, b1)
	}
}

func TestF08LinearThinkingAndSignature(t *testing.T) {
	makeThink := func(n int) []string {
		return deltaStream(n, "thinking", func(i int) string {
			if i%2 == 0 {
				return fmt.Sprintf(`{"type":"thinking_delta","thinking":%q}`, strings.Repeat("t", 16))
			}
			return fmt.Sprintf(`{"type":"signature_delta","signature":%q}`, strings.Repeat("s", 16))
		})
	}
	r1, b1 := allocBytes(t, makeThink(1000))
	r2, b2 := allocBytes(t, makeThink(10000))
	if len(r1.Thinking) != 16*500 || len(r2.Thinking) != 16*5000 {
		t.Fatalf("thinking lengths %d %d", len(r1.Thinking), len(r2.Thinking))
	}
	t.Logf("think 1k=%d 10k=%d ratio=%.2f", b1, b2, float64(b2)/float64(maxU64(b1, 1)))
	if b2 > 15*b1 {
		t.Fatalf("quadratic thinking/signature accumulation: %d > 15x %d", b2, b1)
	}
}

func TestF08ManySmallBlocks(t *testing.T) {
	// 200 text blocks within per-block bounds; final assembly must not be
	// quadratic in block count.
	make := func(blocks int) []string {
		chunks := []string{
			"event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":1}}}\n\n",
		}
		for i := 0; i < blocks; i++ {
			chunks = append(chunks,
				fmt.Sprintf("event: content_block_start\ndata: {\"index\":%d,\"content_block\":{\"type\":\"text\"}}\n\n", i),
				fmt.Sprintf("event: content_block_delta\ndata: {\"index\":%d,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", i, strings.Repeat("b", 64)),
				fmt.Sprintf("event: content_block_stop\ndata: {\"index\":%d}\n\n", i),
			)
		}
		chunks = append(chunks,
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
			"event: message_stop\ndata: {}\n\n",
		)
		return chunks
	}
	r1, b1 := allocBytes(t, make(20))
	r2, b2 := allocBytes(t, make(200))
	if len(r1.Content) != 20 || len(r2.Content) != 200 {
		t.Fatalf("blocks %d %d", len(r1.Content), len(r2.Content))
	}
	if r2.Text != strings.Repeat("b", 64*200) {
		t.Fatal("assembled text changed")
	}
	t.Logf("blocks 20=%d 200=%d ratio=%.2f", b1, b2, float64(b2)/float64(maxU64(b1, 1)))
	if b2 > 15*b1 {
		t.Fatalf("quadratic many-block assembly: %d > 15x %d", b2, b1)
	}
}

func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
