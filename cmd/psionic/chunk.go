package main

import (
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

// chunkText splits outbound text at the last newline, then the last space,
// inside the limit. A window without a usable separator falls back to the hard
// limit, always on a grapheme boundary.
func chunkText(text string, limit int) []string {
	if text == "" {
		return nil
	}
	if limit <= 0 || len(text) <= limit {
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
// fits the soft break, never crossing the hard limit and always advancing.
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
		end = len(cluster)
	}
	return end
}
