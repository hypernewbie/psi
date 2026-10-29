package terminal

import (
	"path/filepath"
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

func TestNarrowActivityBorderKeepsPiCancellationHint(t *testing.T) {
	view := ComposeConsoleView(20, 6, nil, 0, PresentationSnapshot{NoColor: true}, "⠋ Working… (Esc/Ctrl+C cancel, input paused)", "")
	if !strings.Contains(view.Rows[0], "Esc/Ctrl+C") {
		t.Fatalf("narrow status discarded cancellation binding: %q", view.Rows[0])
	}
	for _, row := range view.Rows {
		if CellWidth(Sanitize(row)) > 20 {
			t.Fatalf("status row exceeded width: %q", row)
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
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(t.TempDir()); volume != "" {
		root = volume + string(filepath.Separator)
	}
	home := filepath.Join(root, "synthetic", "operator")
	outsideHome := filepath.Join(root, "synthetic", "operator-old", "work")
	other := filepath.Join(root, "tmp", "work")
	cases := []struct{ input, want string }{
		{home, "~"},
		{filepath.Join(home, "work"), "~" + string(filepath.Separator) + "work"},
		{outsideHome, outsideHome},
		{other, other},
	}
	for _, tc := range cases {
		if got := homeAbbreviated(tc.input, home); got != tc.want {
			t.Errorf("home abbreviation case: got %q want %q", got, tc.want)
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

// Pi's editor border puts the working status at the left of the top border and
// centers the overflow count, like createScrollBorder and the custom editor.
func TestEditorBorderFollowsPiLayout(t *testing.T) {
	draft := []rune("line\nline\nline\nline\nline\nline\nline\nline")
	view := ComposeConsoleView(80, 24, draft, 0, PresentationSnapshot{NoColor: true}, "⠋ Working… (Esc/Ctrl+C cancel, input paused)", "")
	if !strings.HasPrefix(view.Rows[0], "── ⠋ Working…") {
		t.Fatalf("working status is not at the left of the top border: %q", view.Rows[0])
	}
	bottom := view.Rows[8]
	if !strings.Contains(bottom, "↓ 1 more") {
		t.Fatalf("bottom border lost the overflow count: %q", bottom)
	}
	leading := CellWidth(bottom) - CellWidth(strings.TrimLeft(bottom, "─"))
	trailing := CellWidth(bottom) - CellWidth(strings.TrimRight(bottom, "─"))
	if leading > trailing+1 || trailing > leading+1 {
		t.Fatalf("overflow count is not centered in the bottom border: %q", bottom)
	}
}

// Pi's indexed fallback maps semantic tokens to ANSI palette slots. The
// thinking families keep Pi's slate, blue, periwinkle, violet, magenta, and
// red slots.
func TestIndexedFallbackMatchesPiPaletteSlots(t *testing.T) {
	cases := map[ThemeToken]string{
		TokenText:            "",
		TokenMuted:           "2",
		TokenQuote:           "2",
		TokenBorder:          "34",
		TokenLink:            "34",
		TokenAccent:          "35",
		TokenCode:            "35",
		TokenSuccess:         "32",
		TokenCodeBlock:       "32",
		TokenWarning:         "33",
		TokenHeading:         "33",
		TokenError:           "31",
		TokenThinkingOff:     "",
		TokenThinkingMinimal: "34",
		TokenThinkingLow:     "34",
		TokenThinkingMedium:  "36",
		TokenThinkingHigh:    "35",
		TokenThinkingXhigh:   "95",
		TokenThinkingMax:     "31",
	}
	for token, want := range cases {
		if got := tokenSGR(token); got != want {
			t.Fatalf("token %s: got %q, want %q", token, got, want)
		}
	}
}
