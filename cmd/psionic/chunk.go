package main

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// chunkText splits outbound text at the last newline, then the last space,
// inside the target limit. A window without a usable separator falls back to
// the limit. A single grapheme may exceed that target to avoid splitting it,
// but no returned chunk exceeds Discord's 2,000-byte API maximum.
func chunkText(text string, limit int) []string {
	if text == "" {
		return nil
	}
	limit = clamp(limit, 1, discordMaxChars)
	if len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	remaining := text
	for len(remaining) > limit {
		window := remaining[:limit]
		breakAt := -1
		if i := strings.LastIndexByte(window, '\n'); i > 0 {
			breakAt = i
		} else if i := strings.LastIndexByte(window, ' '); i > 0 {
			breakAt = i
		}
		end := graphemeEnd(remaining, limit, breakAt)
		if chunk := strings.TrimRightFunc(remaining[:end], unicode.IsSpace); chunk != "" {
			chunks = append(chunks, chunk)
		}
		remaining = strings.TrimLeftFunc(remaining[end:], unicode.IsSpace)
	}
	if chunk := strings.TrimRightFunc(remaining, unicode.IsSpace); chunk != "" {
		chunks = append(chunks, chunk)
	}
	return chunks
}

// graphemeEnd returns the byte end of the last complete grapheme cluster that
// fits the soft break. It keeps an oversized grapheme whole up to Discord's
// 2,000-byte API maximum; a larger cluster is split at a UTF-8 rune boundary.
// One rune can exceed a tiny target limit, but no chunk can exceed the API cap.
func graphemeEnd(text string, hard, soft int) int {
	if soft <= 0 || soft > hard {
		soft = hard
	}
	if soft > len(text) {
		soft = len(text)
	}
	end, state := 0, -1
	for end < len(text) {
		cluster, _, _, next := uniseg.FirstGraphemeClusterInString(text[end:], state)
		if end+len(cluster) > soft {
			break
		}
		end += len(cluster)
		state = next
	}
	if end == 0 {
		cluster, _, _, _ := uniseg.FirstGraphemeClusterInString(text, -1)
		if len(cluster) <= discordMaxChars {
			return len(cluster)
		}

		// A pathological grapheme must not exceed Discord's message-size cap.
		// Keep each UTF-8 rune intact even if the configured target is smaller.
		end = min(hard, discordMaxChars, len(text))
		for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		if end == 0 {
			_, end = utf8.DecodeRuneInString(text)
		}
	}
	return end
}
