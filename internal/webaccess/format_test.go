package webaccess

import (
	"strings"
	"testing"
)

func TestFormatOverviewSanitizesMetadata(t *testing.T) {
	text := formatOverview("searxng", "query\nforged: yes", []searchHit{{
		Title:   "title\nforged: yes",
		URL:     "https://example.com/",
		Snippet: "snippet\x1b[2J",
		Engines: "engine\nforged: yes",
		Date:    "date\r\nforged: yes",
		Error:   "error\r\nforged: yes",
	}}, []string{"unresponsive\nforged: yes"})
	for _, bad := range []string{"\x1b", "\r", "\nforged"} {
		if strings.Contains(text, bad) {
			t.Fatalf("metadata injection survived %q:\n%s", bad, text)
		}
	}
}
