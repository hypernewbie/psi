package app

import (
	"strings"
	"testing"
)

func conptyPickerSelectionText(text string) string {
	for _, row := range strings.Split(text, "\n") {
		if at := strings.Index(row, "→"); at >= 0 {
			label := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(row[at+len("→"):]), "✓"))
			if fields := strings.Fields(label); len(fields) > 0 {
				return fields[0] // Ignore provider metadata and resize clipping.
			}
		}
	}
	return ""
}

func conptyModelPickerReady(text, selected string) bool {
	return strings.Contains(text, "Models ·") && conptyPickerSelectionText(text) == selected
}

func TestConPTYModelPickerWaitIgnoresCommandCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, screen string
		ready        bool
	}{
		{"command completion", "> /\n→ /settings  Open settings menu\n  /model  Select model", false},
		{"partial picker redraw", "Models · 19 matches\n→ /settings  Open settings menu", false},
		{"picker rendered", "Models · 19 matches\n→ ✓ local-model [anthropic]\n  local-model-2 [anthropic]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := conptyModelPickerReady(tc.screen, "local-model"); got != tc.ready {
				t.Fatalf("picker ready = %v for %q, want %v", got, tc.screen, tc.ready)
			}
		})
	}
}
