package provider

import (
	"strings"
	"testing"
)

func TestResponsesImageURL(t *testing.T) {
	b := Block{
		Type:     "image",
		MimeType: "image/png",
		Data:     "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
	}
	url := responsesImageURL(b)
	expectedPrefix := "data:image/png;base64,"
	if !strings.HasPrefix(url, expectedPrefix) {
		t.Fatalf("responsesImageURL(%+v) = %q, want prefix %q", b, url, expectedPrefix)
	}
	if url != expectedPrefix+b.Data {
		t.Fatalf("responsesImageURL got %q, want %q", url, expectedPrefix+b.Data)
	}
}

func TestBlocksContainImage(t *testing.T) {
	tests := []struct {
		name   string
		blocks []Block
		want   bool
	}{
		{
			name:   "empty blocks",
			blocks: nil,
			want:   false,
		},
		{
			name: "only text blocks",
			blocks: []Block{
				{Type: "text", Text: "hello"},
				{Type: "text", Text: "world"},
			},
			want: false,
		},
		{
			name: "text and image blocks",
			blocks: []Block{
				{Type: "text", Text: "see this:"},
				{Type: "image", MimeType: "image/jpeg", Data: "abc"},
			},
			want: true,
		},
		{
			name: "only image block",
			blocks: []Block{
				{Type: "image", MimeType: "image/gif", Data: "xyz"},
			},
			want: true,
		},
		{
			name: "toolCall and other non-image blocks",
			blocks: []Block{
				{Type: "toolCall", ID: "call_1"},
				{Type: "thinking", Thinking: "hmmm"},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := blocksContainImage(tc.blocks)
			if got != tc.want {
				t.Errorf("blocksContainImage(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestResponsesShortHash(t *testing.T) {
	h1 := responsesShortHash("item-1")
	h2 := responsesShortHash("item-1")
	h3 := responsesShortHash("item-2")

	if h1 == "" {
		t.Fatal("responsesShortHash returned empty string")
	}
	if h1 != h2 {
		t.Fatalf("responsesShortHash should be deterministic: got %q and %q for same input", h1, h2)
	}
	if h1 == h3 {
		t.Fatalf("responsesShortHash collision on distinct inputs: %q == %q", h1, h3)
	}

	// UTF-16 / Unicode support test
	unicodeHash := responsesShortHash("emoji-🎉-test-한글")
	if unicodeHash == "" {
		t.Fatal("responsesShortHash failed on unicode string")
	}
}
