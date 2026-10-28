package terminal

import (
	"strings"
	"testing"
)

func TestStyledRowsSanitizeBeforeSemanticStyle(t *testing.T) {
	row := StyledRow{Spans: []StyledSpan{
		{Text: "safe\x1b[2J\ntext", Token: TokenHeading},
		{Text: "界", Token: TokenMuted},
	}}
	plain := row.Plain()
	if plain != "safe text界" || row.Width() != 11 {
		t.Fatalf("plain=%q width=%d", plain, row.Width())
	}
	styled := row.Serialize(false)
	if !strings.Contains(styled, "\x1b[33m") || !strings.Contains(styled, "\x1b[2m") || !strings.HasSuffix(styled, "\x1b[0m") {
		t.Fatalf("semantic styles missing or not reset: %q", styled)
	}
	if strings.Contains(styled, "\x1b[2J") || strings.Contains(styled, "\n") {
		t.Fatalf("untrusted control survived row serialization: %q", styled)
	}
	plainMode := row.Serialize(true)
	if strings.Contains(plainMode, "\x1b[") || Sanitize(plainMode) != plainMode {
		t.Fatalf("NO_COLOR emitted SGR: %q", plainMode)
	}
}

func TestNoColorEnvironmentDisablesEveryToken(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	view := ComposeEditorView(20, 6, nil, 0, PresentationSnapshot{WorkingDirectory: "/work", Model: "m", Thinking: "high"}, "")
	for _, row := range view.Rows {
		if strings.Contains(row, "\x1b[") {
			t.Fatalf("NO_COLOR row emitted SGR: %q", row)
		}
	}
}

func TestComposeEditorViewShowsPiOverflowCounts(t *testing.T) {
	draft := []rune(strings.Repeat("line\n", 30))
	atEnd := ComposeEditorView(40, 12, draft, len(draft), PresentationSnapshot{NoColor: true}, "")
	if !strings.Contains(atEnd.Rows[0], "↑ 26 more") {
		t.Fatalf("top border lacks hidden-row count: %q", atEnd.Rows[0])
	}
	atStart := ComposeEditorView(40, 12, draft, 0, PresentationSnapshot{NoColor: true}, "")
	if !strings.Contains(atStart.Rows[6], "↓ 26 more") {
		t.Fatalf("bottom border lacks hidden-row count: %q", atStart.Rows[6])
	}
}

func TestComposeEditorViewGeometryAndDraftPreservation(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {40, 12}, {20, 6}, {2, 2}, {1, 1}} {
		width, height := size[0], size[1]
		draft := []rune(strings.Repeat("界e\u0301🌍", 40) + "\nlast line")
		view := ComposeEditorView(width, height, draft, len(draft), PresentationSnapshot{
			WorkingDirectory: "/home/operator/work", HomeDirectory: "/home/operator",
			Usage: "↑1.2k ↓320", ContextEstimate: "~8%/100k", Provider: "anthropic", Model: "example-model", Thinking: "high",
		}, "Working… (Esc/Ctrl+C cancel, input paused)")
		if len(view.Rows) == 0 || len(view.Rows) > height {
			t.Fatalf("%dx%d row count=%d", width, height, len(view.Rows))
		}
		if len(view.CellWidth) != len(view.Rows) {
			t.Fatalf("%dx%d cell-width mismatch", width, height)
		}
		for i, row := range view.Rows {
			if view.CellWidth[i] > width || CellWidth(Sanitize(row)) > width {
				t.Fatalf("%dx%d row %d too wide (%d): %q", width, height, i, CellWidth(Sanitize(row)), row)
			}
		}
		if view.CursorRow < 0 || view.CursorRow >= len(view.Rows) || view.CursorCol < 0 || view.CursorCol >= width {
			t.Fatalf("%dx%d cursor=%d,%d rows=%d", width, height, view.CursorRow, view.CursorCol, len(view.Rows))
		}
		if string(draft[len(draft)-9:]) != "last line" {
			t.Fatal("view mutated source draft")
		}
	}
}

func TestHomeAbbreviationRespectsDirectoryBoundary(t *testing.T) {
	cases := map[string]string{
		"/home/operator":          "~",
		"/home/operator/work":     "~/work",
		"/home/operator-old/work": "/home/operator-old/work",
		"/tmp/work":               "/tmp/work",
	}
	for input, want := range cases {
		if got := homeAbbreviated(input, "/home/operator"); got != want {
			t.Errorf("%q: got %q want %q", input, got, want)
		}
	}
}

func TestNoColorEnvStillLeavesStateText(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	view := ComposeEditorView(50, 10, nil, 0, PresentationSnapshot{WorkingDirectory: "/work", Model: "model", Thinking: "high"}, "Working…")
	plain := strings.Join(view.Rows, "\n")
	if !strings.Contains(plain, "Working…") || !strings.Contains(plain, "model") || strings.Contains(plain, "\x1b[") {
		t.Fatalf("colorless state lost or SGR leaked: %q", plain)
	}
}
