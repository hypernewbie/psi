package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkText(t *testing.T) {
	if got := chunkText("", 100); got != nil {
		t.Fatalf("empty = %#v", got)
	}
	if got := chunkText("short", 100); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short = %#v", got)
	}
	long := strings.Repeat("a", 25)
	got := chunkText(long, 10)
	if len(got) != 3 || got[0] != "aaaaaaaaaa" || got[2] != "aaaaa" {
		t.Fatalf("hard split = %#v", got)
	}
	for _, chunk := range got {
		if len(chunk) > 10 {
			t.Fatalf("chunk too long: %q", chunk)
		}
	}
}

func TestChunkTextPrefersNewlineThenSpace(t *testing.T) {
	text := "first line\nsecond line is longer"
	got := chunkText(text, 21)
	if len(got) != 2 || got[0] != "first line" || got[1] != "second line is longer" {
		t.Fatalf("newline split = %#v", got)
	}
	text = "alpha beta gamma delta"
	got = chunkText(text, 12)
	if len(got) != 2 || got[0] != "alpha beta" || got[1] != "gamma delta" {
		t.Fatalf("space split = %#v", got)
	}
}

func TestChunkTextBoundsPathologicalGraphemeToDiscordMaximum(t *testing.T) {
	// U+0301 COMBINING ACUTE ACCENT extends one grapheme beyond Discord's byte cap.
	text := "a" + strings.Repeat("\u0301", 1500)
	chunks := chunkText(text, discordMaxChars)
	if len(chunks) < 2 {
		t.Fatalf("oversized grapheme was not split: %d chunk(s)", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) > discordMaxChars {
			t.Fatalf("chunk %d exceeds Discord's limit: %d", i, len(chunk))
		}
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is invalid UTF-8", i)
		}
	}
	if got := strings.Join(chunks, ""); got != text {
		t.Fatalf("split changed text: got %d bytes, want %d", len(got), len(text))
	}
}

func TestChunkTextKeepsGraphemeClusters(t *testing.T) {
	text := "ab🏳️‍🌈cd"
	got := chunkText(text, 3)
	if len(got) == 0 {
		t.Fatal("no chunks")
	}
	joined := strings.Join(got, "")
	if strings.ReplaceAll(joined, " ", "") != "ab🏳️‍🌈cd" {
		t.Fatalf("lost characters: %#v", got)
	}
	for _, chunk := range got {
		if strings.Contains(chunk, "\u200d") && !strings.Contains(chunk, "🏳️‍🌈") {
			t.Fatalf("split a grapheme cluster: %q", chunk)
		}
	}
}
