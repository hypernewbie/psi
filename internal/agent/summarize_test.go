package agent

import (
	"context"
	"math"
	"strings"
	"testing"

	"psi/internal/provider"
)

func TestSummaryOutputTokenLimitMatchesPi(t *testing.T) {
	tests := []struct {
		name   string
		model  provider.ModelMeta
		prompt string
		want   int
	}{
		{
			name:   "use model limit without context window",
			model:  provider.ModelMeta{MaxTokens: 16_384},
			prompt: "short prompt",
			want:   16_384,
		},
		{
			name:   "do not impose psi 4096 output cap",
			model:  provider.ModelMeta{MaxTokens: 16_384, ContextWindow: 200_000},
			prompt: "short prompt",
			want:   16_384,
		},
		{
			name:   "clamp output to remaining context after safety margin",
			model:  provider.ModelMeta{MaxTokens: 16_384, ContextWindow: 10_000},
			prompt: strings.Repeat("p", 1_000),
			want:   10_000 - int(math.Ceil(float64(len(summarizationSystemPrompt)+1_000)/3.5)) - 4_096,
		},
		{
			name:   "leave at least one output token when context is exhausted",
			model:  provider.ModelMeta{MaxTokens: 16_384, ContextWindow: 4_096},
			prompt: strings.Repeat("p", 10_000),
			want:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryOutputTokenLimit(tt.model, tt.prompt); got != tt.want {
				t.Fatalf("summaryOutputTokenLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}

func FuzzSummaryOutputTokenLimit(f *testing.F) {
	f.Add(int64(0), int64(16_384), "short prompt")
	f.Add(int64(200_000), int64(16_384), strings.Repeat("x", 1_000))
	f.Add(int64(4_096), int64(16_384), "😀漢字")

	f.Fuzz(func(t *testing.T, contextWindow, maxTokens int64, prompt string) {
		const maxInput = int64(1_000_000_000)
		contextWindow = max(-maxInput, min(contextWindow, maxInput))
		maxTokens = max(-maxInput, min(maxTokens, maxInput))
		model := provider.ModelMeta{ContextWindow: int(contextWindow), MaxTokens: int(maxTokens)}
		got := summaryOutputTokenLimit(model, prompt)
		if contextWindow <= 0 {
			if got != int(maxTokens) {
				t.Fatalf("no-window budget = %d, want model limit %d", got, maxTokens)
			}
			return
		}

		inputChars := piStringChars(summarizationSystemPrompt) + piStringChars(prompt)
		available := max(1, int(contextWindow)-int(math.Ceil(float64(inputChars)/3.5))-4_096)
		want := available
		if maxTokens > 0 && int(maxTokens) < want {
			want = int(maxTokens)
		}
		if got != want || got < 1 {
			t.Fatalf("budget = %d, want %d (window=%d modelMax=%d chars=%d)", got, want, contextWindow, maxTokens, inputChars)
		}
	})
}

func TestTruncateSummaryTextUsesPiLengthAndMarker(t *testing.T) {
	for _, tt := range []struct {
		name string
		text string
		want string
	}{
		{name: "below limit", text: strings.Repeat("x", 1_999), want: strings.Repeat("x", 1_999)},
		{name: "at limit", text: strings.Repeat("x", 2_000), want: strings.Repeat("x", 2_000)},
		{name: "over limit", text: strings.Repeat("x", 2_001), want: strings.Repeat("x", 2_000) + "\n\n[... 1 more characters truncated]"},
		{name: "unicode uses UTF-16 units", text: strings.Repeat("😀", 1_001), want: strings.Repeat("😀", 1_000) + "\n\n[... 2 more characters truncated]"},
		{name: "unicode below UTF-16 limit", text: strings.Repeat("😀", 999), want: strings.Repeat("😀", 999)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateSummaryText(tt.text); got != tt.want {
				t.Fatalf("truncateSummaryText() length=%d, want length=%d", len(got), len(tt.want))
			}
		})
	}
}

func TestRequestSummaryUsesPiOutputBudget(t *testing.T) {
	_, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "summary", StopReason: "stop"}}
	cfg := Config{Model: provider.ModelMeta{
		ID: "large-output-model", API: "anthropic-messages", MaxTokens: 16_384, ContextWindow: 200_000,
	}}

	if _, err := loop.requestSummary(context.Background(), fake, cfg, "", "old conversation", "", false); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("summary requests = %d, want 1", len(requests))
	}
	if requests[0].MaxTokens != 16_384 {
		t.Fatalf("summary max tokens = %d, want model limit 16384", requests[0].MaxTokens)
	}
}

func TestRequestSummaryReportsIncompleteResponsesPrecisely(t *testing.T) {
	call := provider.Block{Type: "toolCall", ID: "call", Name: "read", Arguments: map[string]any{"path": "fixture.txt"}}
	tests := []struct {
		name     string
		response *provider.Response
		wantText string
		wantErr  string
	}{
		{name: "nil response", wantErr: "no response"},
		{name: "empty response", response: &provider.Response{StopReason: "stop"}, wantErr: "no text"},
		{name: "whitespace response", response: &provider.Response{Text: " \n", StopReason: "stop"}, wantErr: "no text"},
		{name: "output limit", response: &provider.Response{Text: "partial", StopReason: "length"}, wantErr: "output token limit"},
		{name: "empty output limit", response: &provider.Response{StopReason: "length"}, wantErr: "output token limit"},
		{name: "provider error stop", response: &provider.Response{Text: "partial", StopReason: "error"}, wantErr: "provider reported an error"},
		{name: "aborted stop", response: &provider.Response{Text: "partial", StopReason: "aborted"}, wantErr: "stopped before completion (aborted)"},
		{name: "tool call", response: &provider.Response{Text: "partial", StopReason: "toolUse", ToolCalls: []provider.Block{call}}, wantErr: "attempted to call a tool"},
		{name: "tool call only in content", response: &provider.Response{Text: "partial", StopReason: "stop", Content: []provider.Block{call}}, wantErr: "attempted to call a tool"},
		{name: "complete response", response: &provider.Response{Text: "complete summary", StopReason: "stop"}, wantText: "complete summary"},
		{name: "unspecified stop reason", response: &provider.Response{Text: "complete summary"}, wantText: "complete summary"},
		{name: "text derived from content", response: &provider.Response{StopReason: "stop", Content: []provider.Block{{Type: "text", Text: "content summary"}}}, wantText: "content summary"},
		{name: "content is authoritative", response: &provider.Response{Text: "stale text", StopReason: "stop", Content: []provider.Block{{Type: "text", Text: "authoritative summary"}}}, wantText: "authoritative summary"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, fake, loop, _ := testSetup(t)
			fake.Responses = []*provider.Response{tt.response}
			got, err := loop.requestSummary(context.Background(), fake, Config{Model: modelMeta()}, "", "conversation", "", false)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("requestSummary() error = %v, want substring %q", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), "original context is unchanged") {
					t.Fatalf("error does not promise context preservation: %v", err)
				}
				return
			}
			if err != nil || got == nil || got.Text != tt.wantText {
				t.Fatalf("requestSummary() = (%+v, %v), want text %q", got, err, tt.wantText)
			}
		})
	}
}

func TestIncompleteSummaryLeavesOriginalContextUncompacted(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("original request")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, "work so far")
	before := len(store.Entries())
	fake.Responses = []*provider.Response{{Text: "partial summary", StopReason: "length"}}

	err := loop.Compact(context.Background(), Config{Model: modelMeta(), KeepRecentTokens: 0}, "")
	if err == nil || !strings.Contains(err.Error(), "output token limit") {
		t.Fatalf("Compact() error = %v, want output-limit failure", err)
	}
	entries := store.Entries()
	if len(entries) != before {
		t.Fatalf("incomplete summary changed session: entry count %d -> %d", before, len(entries))
	}
	for _, entry := range entries {
		if entry.Kind == "compaction" {
			t.Fatal("incomplete summary appended a compaction checkpoint")
		}
	}
}
