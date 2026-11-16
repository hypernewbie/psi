package webaccess

import (
	"strings"
	"testing"
)

func TestRedactorMatchesSecretsAcrossOutputBoundary(t *testing.T) {
	const prefix = "prefix: "
	const secret = "boundary-secret-value"
	cfg := testConfig()
	cfg.ExaAPIKey = secret
	redactor := newRedactor(cfg)
	for _, tc := range []struct {
		name  string
		limit int
		want  string
	}{
		{"marker fits", len(prefix) + len("[redacted]"), prefix + "[redacted]"},
		{"marker does not fit", len(prefix) + len("[redacted]") - 1, prefix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactor.redact(prefix+secret+" suffix", tc.limit); got != tc.want {
				t.Fatalf("boundary redaction = %q, want %q", got, tc.want)
			}
		})
	}
}

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
