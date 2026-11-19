package terminal

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestActivityBorderUsesPiSpinnerAndMessageColors(t *testing.T) {
	// Pi's WorkingStatusIndicator uses accent for the spinner and muted for
	// its message, independently of the thinking-colored editor border.
	view := ComposeEditorView(60, 10, nil, 0, PresentationSnapshot{Thinking: "low"}, "⠋ Working…")
	want := "\x1b[34m── \x1b[0m\x1b[35m⠋\x1b[0m\x1b[2m Working…\x1b[0m\x1b[34m "
	if !strings.HasPrefix(view.Rows[1], want) {
		t.Fatalf("working border colors = %q, want prefix %q", view.Rows[0], want)
	}
	plain := ComposeEditorView(60, 10, nil, 0, PresentationSnapshot{Thinking: "low", NoColor: true}, "⠋ Working…")
	if strings.Contains(plain.Rows[1], "\x1b[") || Sanitize(view.Rows[1]) != plain.Rows[1] {
		t.Fatalf("NO_COLOR changed border text: styled=%q plain=%q", view.Rows[1], plain.Rows[1])
	}
}

func TestNarrowActivityBorderTruncatesToWidth(t *testing.T) {
	view := ComposeConsoleView(20, 6, nil, 0, PresentationSnapshot{NoColor: true}, "⠋ Compacting context... (escape to cancel)", "")
	for _, row := range view.Rows {
		if CellWidth(Sanitize(row)) > 20 {
			t.Fatalf("status row exceeded width: %q", row)
		}
	}
	if !strings.Contains(view.Rows[1], "Compacting") {
		t.Fatalf("narrow status lost the activity label: %q", view.Rows[1])
	}
}

func TestComposeEditorViewShowsPiOverflowCounts(t *testing.T) {
	draft := []rune(strings.Repeat("line\n", 30))
	atEnd := ComposeEditorView(40, 12, draft, len(draft), PresentationSnapshot{NoColor: true}, "")
	if !strings.Contains(atEnd.Rows[1], "↑ 26 more") {
		t.Fatalf("top border lacks hidden-row count: %q", atEnd.Rows[1])
	}
	atStart := ComposeEditorView(40, 12, draft, 0, PresentationSnapshot{NoColor: true}, "")
	if !strings.Contains(atStart.Rows[7], "↓ 26 more") {
		t.Fatalf("bottom border lacks hidden-row count: %q", atStart.Rows[7])
	}
}

func TestComposeEditorViewGeometryAndDraftPreservation(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {40, 12}, {20, 6}, {2, 2}, {1, 1}} {
		width, height := size[0], size[1]
		draft := []rune(strings.Repeat("界e\u0301🌍", 40) + "\nlast line")
		view := ComposeEditorView(width, height, draft, len(draft), PresentationSnapshot{
			WorkingDirectory: "/home/operator/work", HomeDirectory: "/home/operator",
			Usage: "↑1.2k ↓320", ContextEstimate: "8%/100k", Provider: "anthropic", ProviderCount: 2, Model: "example-model", Thinking: "high",
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

func TestFooterSessionNameSanitizesTerminalControls(t *testing.T) {
	view := ComposeEditorView(80, 10, nil, 0, PresentationSnapshot{
		WorkingDirectory: "/work", SessionName: "safe\x1b[2J\nname", NoColor: true,
	}, "")
	plain := strings.Join(view.Rows, "\n")
	if strings.Contains(plain, "\x1b") || strings.Contains(plain, "\x1b[2J") {
		t.Fatalf("session name injected terminal control: %q", plain)
	}
	if !strings.Contains(plain, "safe name") {
		t.Fatalf("session name was not kept as harmless footer text: %q", plain)
	}
}

func TestFooterRendersGitBranchBeforeSessionName(t *testing.T) {
	rows, _ := formatFooter(PresentationSnapshot{
		WorkingDirectory: filepath.FromSlash("/tmp/work"), GitBranch: "main", SessionName: "notes",
	}, 80, 3, true)
	if !strings.Contains(rows[0], filepath.FromSlash("/tmp/work")+" (main) • notes") {
		t.Fatalf("branch not rendered before session name: %q", rows[0])
	}
}

func TestFooterSanitizesGitBranch(t *testing.T) {
	rows, _ := formatFooter(PresentationSnapshot{
		WorkingDirectory: "/work", GitBranch: "ma\nin", NoColor: true,
	}, 80, 3, true)
	plain := rows[0]
	if strings.ContainsAny(plain, "\n\r\x1b") {
		t.Fatalf("branch injected terminal control: %q", plain)
	}
	if !strings.Contains(plain, "ma in") {
		t.Fatalf("branch was not kept as harmless footer text: %q", plain)
	}
}

func TestFooterDrawsInlineContextBar(t *testing.T) {
	rows, widths := formatFooter(PresentationSnapshot{
		Usage: "↑27k ↓5.1k", ContextEstimate: "2.4%/1.0M",
		ContextPercent: 2.4, ContextUsageLabel: "2.4%", ContextWindowLabel: "1.0M",
		AutoCompact: true, Model: "m", NoColor: true,
	}, 80, 3, true)
	line := Sanitize(rows[1])
	for _, want := range []string{"↑27k ↓5.1k [", "] 2.4%/1.0M (auto)", "░"} {
		if !strings.Contains(line, want) {
			t.Fatalf("inline context bar missing %q: %q", want, line)
		}
	}
	if got := strings.Count(line, "|"); got != 0 {
		t.Fatalf("inline context bar shows %d band dividers, want none: %q", got, line)
	}
	if !strings.ContainsAny(line, "▏▎▍▌▋▊▉█") {
		t.Fatalf("inline context bar has no fill: %q", line)
	}
	if strings.Contains(rows[1], "\x1b[") {
		t.Fatalf("NO_COLOR footer emitted SGR: %q", rows[1])
	}
	if widths[1] > 79 {
		t.Fatalf("footer exceeded its wrap-safe width: %d", widths[1])
	}
}

// barTrackCells returns the track width between the bar brackets.
func barTrackCells(line string) (int, bool) {
	open := strings.IndexByte(line, '[')
	if open < 0 {
		return 0, false
	}
	relativeClose := strings.IndexByte(line[open:], ']')
	if relativeClose < 0 {
		return 0, false
	}
	return CellWidth(line[open+1 : open+relativeClose]), true
}

// TestFooterContextBarAdaptsToWidth pins the continuous 4-16 cell track.
// A bar-mode stats group spends at most half the terminal on usage, the bar,
// brackets, caption, and the auto marker; the textual fallback keeps the full
// wrap-safe row even when it extends past that midpoint.
func TestFooterContextBarAdaptsToWidth(t *testing.T) {
	snapshot := PresentationSnapshot{
		Usage: "↑57k", ContextEstimate: "5.7%/1.0M",
		ContextPercent: 5.7, ContextUsageLabel: "5.7%", ContextWindowLabel: "1.0M",
		AutoCompact: true, Provider: "anthropic", ProviderCount: 2, Model: "m", Thinking: "high",
		NoColor: true,
	}
	const (
		caption = "5.7%/1.0M (auto)"
		// Four usage cells, the complete caption, one separator after usage,
		// one separator after the bar, and the two brackets.
		barOverhead = 4 + 16 + 2 + 2
		fallback    = "↑57k 5.7%/1.0M (auto)"
		rightLabel  = "(anthropic) m • high"
	)
	checkRows := func(width int, rows []string, cells []int) {
		t.Helper()
		if len(rows) != 2 || len(cells) != 2 {
			t.Fatalf("width %d: footer rows=%q cells=%v", width, rows, cells)
		}
		limit := max(1, width-1)
		for i, row := range rows {
			if got := CellWidth(Sanitize(row)); got > limit || cells[i] > limit {
				t.Fatalf("width %d row %d: width=%d cells=%d, want at most %d: %q", width, i, got, cells[i], limit, row)
			}
		}
	}

	for width := 56; width <= 81; width++ {
		rows, cells := formatFooter(snapshot, width, 3, true)
		checkRows(width, rows, cells)
		line := Sanitize(rows[1])
		track, ok := barTrackCells(line)
		if !ok {
			t.Fatalf("width %d: bar missing: %q", width, line)
		}
		if want := width/2 - barOverhead; track != want {
			t.Fatalf("width %d: track=%d, want %d: %q", width, track, want, line)
		}
		captionAt := strings.Index(line, caption)
		if captionAt < 0 {
			t.Fatalf("width %d: complete caption missing: %q", width, line)
		}
		leftEnd := CellWidth(line[:captionAt+len(caption)])
		if leftEnd > width/2 {
			t.Fatalf("width %d: bar-mode stats end at %d, past the midpoint %d: %q", width, leftEnd, width/2, line)
		}
		if !strings.HasSuffix(line, rightLabel) || cells[1] != width-1 {
			t.Fatalf("width %d: right label not aligned to the wrap-safe edge: %q", width, line)
		}
		gap := cells[1] - leftEnd - CellWidth(rightLabel)
		if gap < 2 {
			t.Fatalf("width %d: right label separated by %d cells, want at least 2: %q", width, gap, line)
		}
		if strings.Contains(line, "~") || !strings.Contains(line, "5.7%/1.0M") {
			t.Fatalf("width %d: caption is not compact: %q", width, line)
		}
	}

	t.Run("maximum track", func(t *testing.T) {
		const width = 120
		rows, cells := formatFooter(snapshot, width, 3, true)
		checkRows(width, rows, cells)
		line := Sanitize(rows[1])
		track, ok := barTrackCells(line)
		if !ok || track != 16 {
			t.Fatalf("width %d: track=%d present=%v, want 16: %q", width, track, ok, line)
		}
		if !strings.HasSuffix(line, rightLabel) {
			t.Fatalf("width %d: complete provider/model label missing: %q", width, line)
		}
	})

	for _, width := range []int{55, 40} {
		rows, cells := formatFooter(snapshot, width, 3, true)
		checkRows(width, rows, cells)
		line := Sanitize(rows[1])
		if strings.ContainsAny(line, "[]") {
			t.Fatalf("width %d: bar survived without its half-row budget: %q", width, line)
		}
		if !strings.HasPrefix(line, fallback) {
			t.Fatalf("width %d: counters-first text is not the complete 21-cell fallback: %q", width, line)
		}
		if strings.Contains(line, "~") || !strings.Contains(line, "5.7%/1.0M (auto)") {
			t.Fatalf("width %d: fallback is not compact: %q", width, line)
		}
		if width == 40 && (!strings.HasSuffix(line, "m • high") || strings.Contains(line, "anthropic")) {
			t.Fatalf("width 40: model label or provider omission wrong: %q", line)
		}
	}

	t.Run("fit boundaries", func(t *testing.T) {
		// The minimum group is usage plus its separator, six bar cells (four
		// track cells and two brackets), one separator, and the caption. The
		// half-row budget doubles that group width.
		for _, tc := range []struct {
			name  string
			usage string
			auto  bool
			first int
		}{
			{"usage auto", "↑57k", true, 56},
			{"usage no auto", "↑57k", false, 42},
			{"empty auto", "", true, 46},
			{"empty no auto", "", false, 32},
		} {
			t.Run(tc.name, func(t *testing.T) {
				variant := snapshot
				variant.Usage = tc.usage
				variant.AutoCompact = tc.auto
				caption := "5.7%/1.0M"
				if tc.auto {
					caption += " (auto)"
				}
				fallback := strings.TrimSpace(tc.usage + " " + caption)
				for _, width := range []int{tc.first, tc.first + 1} {
					rows, _ := formatFooter(variant, width, 3, true)
					line := Sanitize(rows[1])
					track, ok := barTrackCells(line)
					if !ok || track != 4 {
						t.Fatalf("width %d: track=%d present=%v, want 4: %q", width, track, ok, line)
					}
					if !strings.Contains(line, caption) {
						t.Fatalf("width %d: incomplete caption: %q", width, line)
					}
				}
				rows, _ := formatFooter(variant, tc.first-1, 3, true)
				line := Sanitize(rows[1])
				if strings.ContainsAny(line, "[]") || !strings.HasPrefix(line, fallback) {
					t.Fatalf("width %d: bar survived below the fit boundary or text is incomplete: %q", tc.first-1, line)
				}
			})
		}
	})

	t.Run("tiny widths", func(t *testing.T) {
		for _, width := range []int{0, 1, 2} {
			rows, cells := formatFooter(snapshot, width, 3, true)
			if width == 0 {
				if len(rows) != 0 || len(cells) != 0 {
					t.Fatalf("width 0 returned rows=%q cells=%v", rows, cells)
				}
				continue
			}
			checkRows(width, rows, cells)
			if strings.ContainsAny(Sanitize(rows[1]), "[]") {
				t.Fatalf("width %d: impossible bar appeared: %q", width, rows[1])
			}
		}
		// Below the caption width, counters stay first and clipping is safe.
		rows, cells := formatFooter(snapshot, 12, 3, true)
		checkRows(12, rows, cells)
		line := Sanitize(rows[1])
		if !strings.HasPrefix(line, "↑57k") || !strings.Contains(line, "...") {
			t.Fatalf("narrow stats did not keep counters first with Pi ellipsis: %q", line)
		}
	})

	t.Run("heights", func(t *testing.T) {
		for _, height := range []int{2, 3, 40} {
			rows, _ := formatFooter(snapshot, 80, height, true)
			var stats string
			switch height {
			case 2:
				stats = Sanitize(rows[0])
			default:
				stats = Sanitize(rows[1])
			}
			track, ok := barTrackCells(stats)
			if !ok || track != 16 {
				t.Fatalf("height %d: track=%d present=%v, want 16: %q", height, track, ok, stats)
			}
		}
		if rows, cells := formatFooter(snapshot, 80, 1, true); len(rows) != 0 || len(cells) != 0 {
			t.Fatalf("height 1 returned rows=%q cells=%v", rows, cells)
		}
	})
}

func TestFooterBarRendersExactWidth(t *testing.T) {
	for width := footerBarMinWidth; width <= footerBarMaxWidth; width++ {
		for _, percent := range []float64{0, 0.1, 40, 70, 100} {
			bar := StyledRow{Spans: footerBarSpans(percent, width)}
			if got := bar.Width(); got != width {
				t.Fatalf("bar at %v%% width=%d: rendered width=%d", percent, width, got)
			}
			plain := bar.Plain()
			if percent == 0 && strings.ContainsAny(plain, "█▏▎▍▌▋▊▉") {
				t.Fatalf("zero-percent bar has fill: %q", plain)
			}
			if percent > 0 && !strings.ContainsAny(plain, "█▏▎▍▌▋▊▉") {
				t.Fatalf("%v%% bar lost its visible fill: %q", percent, plain)
			}
			if percent == 100 && strings.Contains(plain, "░") {
				t.Fatalf("full bar has an empty track: %q", plain)
			}
		}
	}
}

func TestFooterBarZonesKeepEveryBandVisible(t *testing.T) {
	for width := footerBarMinWidth; width <= footerBarMaxWidth; width++ {
		green, yellow, red := footerBarZones(width)
		if green < 1 || yellow < 1 || red < 1 {
			t.Fatalf("width=%d: band widths (%d,%d,%d) hide a band", width, green, yellow, red)
		}
		track := green + yellow + red
		if track != width-2 {
			t.Fatalf("width=%d: track=%d, want %d", width, track, width-2)
		}
		if got, want := green, int(math.Round(0.40*float64(track))); got < want-1 || got > want+1 {
			t.Fatalf("width=%d: green band=%d, want ~%d", width, got, want)
		}
		if got, want := green+yellow, int(math.Round(0.70*float64(track))); got < want-1 || got > want+1 {
			t.Fatalf("width=%d: yellow ends at %d, want ~%d", width, got, want)
		}
	}
	if green, yellow, red := footerBarZones(footerBarMinWidth - 1); green+yellow+red != 0 {
		t.Fatalf("narrow width produced bands (%d,%d,%d)", green, yellow, red)
	}
}

func TestFooterClampsInvalidContextPercent(t *testing.T) {
	for _, tc := range []struct {
		percent float64
		color   string
	}{
		{math.NaN(), "\x1b[32m"},
		{-25, "\x1b[32m"},
		{150, "\x1b[31m"},
		{math.Inf(1), "\x1b[31m"},
	} {
		rows, _ := formatFooter(PresentationSnapshot{
			ContextPercent: tc.percent, ContextUsageLabel: "x%", ContextWindowLabel: "1M", Model: "m",
		}, 80, 3, false)
		if !strings.Contains(rows[1], tc.color+" x%/1M") {
			t.Errorf("percent %v colored the label without %q: %q", tc.percent, tc.color, rows[1])
		}
		if tc.color == "\x1b[32m" && strings.Contains(rows[1], "\x1b[31m") {
			t.Errorf("percent %v put red in the bar: %q", tc.percent, rows[1])
		}
	}
}

func TestFooterContextBarColorBands(t *testing.T) {
	const (
		green  = "\x1b[32m"
		yellow = "\x1b[33m"
		red    = "\x1b[31m"
	)
	// Band colors follow cell quantization, so the probes stay well inside
	// each band instead of pinning the rounded 40% and 70% boundaries.
	for _, tc := range []struct {
		percent float64
		want    []string
		absent  []string
	}{
		{10, []string{green}, []string{yellow, red}},
		{30, []string{green}, []string{yellow, red}},
		{50, []string{green, yellow}, []string{red}},
		{65, []string{green, yellow}, []string{red}},
		{85, []string{green, yellow, red}, nil},
		{100, []string{green, yellow, red}, nil},
	} {
		rows, _ := formatFooter(PresentationSnapshot{
			ContextPercent: tc.percent, ContextUsageLabel: "x%", ContextWindowLabel: "1M", Model: "m",
		}, 80, 3, false)
		for _, want := range tc.want {
			if !strings.Contains(rows[1], want) {
				t.Errorf("context bar at %v%% lacks color %q: %q", tc.percent, want, rows[1])
			}
		}
		for _, absent := range tc.absent {
			if strings.Contains(rows[1], absent) {
				t.Errorf("context bar at %v%% shows unused band color %q: %q", tc.percent, absent, rows[1])
			}
		}
	}
}

func TestFooterDrawsTokenContextCaption(t *testing.T) {
	rows, widths := formatFooter(PresentationSnapshot{
		Usage: "↑79k ↓1.0k", ContextEstimate: "80k/200k",
		ContextPercent: 40, ContextUsageLabel: "80k", ContextWindowLabel: "200k",
		Model: "m", NoColor: true,
	}, 80, 3, true)
	line := Sanitize(rows[1])
	for _, want := range []string{"↑79k ↓1.0k [", "] 80k/200k", "░"} {
		if !strings.Contains(line, want) {
			t.Fatalf("token context bar missing %q: %q", want, line)
		}
	}
	if width := widths[1]; width > 79 {
		t.Fatalf("token footer exceeded its wrap-safe width: %d", width)
	}
}

func TestFooterFallsBackToTokenTextOnNarrowTerminal(t *testing.T) {
	rows, widths := formatFooter(PresentationSnapshot{
		Usage: "↑79k ↓1.0k", ContextEstimate: "80k/200k",
		ContextPercent: 40, ContextUsageLabel: "80k", ContextWindowLabel: "200k",
		Model: "m", NoColor: true,
	}, 24, 3, true)
	line := Sanitize(rows[1])
	if strings.Contains(line, "[") || !strings.Contains(line, "80k/200k") {
		t.Fatalf("narrow footer did not fall back to the token estimate: %q", line)
	}
	if width := widths[1]; width > 23 {
		t.Fatalf("narrow token footer exceeded its wrap-safe width: %d", width)
	}
}

func TestFooterTokenCaptionFitsOneColumnViewport(t *testing.T) {
	rows, widths := formatFooter(PresentationSnapshot{
		Usage: "↑79k ↓1.0k", ContextEstimate: "80k/200k",
		ContextPercent: 40, ContextUsageLabel: "80k", ContextWindowLabel: "200k",
		Model: "m", NoColor: true,
	}, 1, 3, true)
	if len(rows) != 2 || len(widths) != len(rows) {
		t.Fatalf("rows=%d widths=%d; want two footer rows with widths", len(rows), len(widths))
	}
	for i, row := range rows {
		width := CellWidth(Sanitize(row))
		// A one-column viewport cannot reserve the usual wrap-safe column.
		if width > 1 || widths[i] != width {
			t.Fatalf("one-column viewport row %d width=%d reported=%d", i, width, widths[i])
		}
	}
}

func TestFooterTokenCaptionPreservesCountOutsideBarRange(t *testing.T) {
	zero, _ := formatFooter(PresentationSnapshot{
		ContextPercent: 0, ContextUsageLabel: "0", ContextWindowLabel: "200k", Model: "m", NoColor: true,
	}, 80, 3, true)
	if line := Sanitize(zero[1]); !strings.Contains(line, "] 0/200k") {
		t.Fatalf("zero token label missing: %q", line)
	}

	above, _ := formatFooter(PresentationSnapshot{
		ContextPercent: 150, ContextUsageLabel: "250k", ContextWindowLabel: "200k", Model: "m",
	}, 80, 3, false)
	line := above[1]
	if !strings.Contains(line, "\x1b[31m 250k/200k") {
		t.Fatalf("usage above the window did not keep the token count with a red label: %q", line)
	}
}

func TestFooterColorsContextLabelByBand(t *testing.T) {
	for _, tc := range []struct {
		percent float64
		label   string
		color   string
	}{
		{12, "12.0%", "\x1b[32m"},
		{39.9, "39.9%", "\x1b[32m"},
		{40.0, "40.0%", "\x1b[33m"},
		{55, "55.0%", "\x1b[33m"},
		{69.9, "69.9%", "\x1b[33m"},
		{70.0, "70.0%", "\x1b[31m"},
		{99.9, "99.9%", "\x1b[31m"},
		{12, "80k", "\x1b[32m"},
		{55, "80k", "\x1b[33m"},
		{99.9, "80k", "\x1b[31m"},
	} {
		rows, _ := formatFooter(PresentationSnapshot{
			ContextPercent: tc.percent, ContextUsageLabel: tc.label, ContextWindowLabel: "1.0M", Model: "m",
		}, 80, 3, false)
		want := tc.color + " " + tc.label + "/1.0M"
		if !strings.Contains(rows[1], want) {
			t.Errorf("context label at %v%% lacks %q: %q", tc.percent, want, rows[1])
		}
	}
}

func TestFooterFallsBackToTextOnNarrowTerminal(t *testing.T) {
	rows, widths := formatFooter(PresentationSnapshot{
		Usage: "↑27k ↓5.1k", ContextEstimate: "2.4%/1.0M",
		ContextPercent: 2.4, ContextUsageLabel: "2.4%", ContextWindowLabel: "1.0M",
		Model: "m", NoColor: true,
	}, 24, 3, true)
	line := Sanitize(rows[1])
	if strings.Contains(line, "[") || !strings.Contains(line, "2.4%/1.0M") {
		t.Fatalf("narrow footer did not fall back to the textual estimate: %q", line)
	}
	if widths[1] > 23 {
		t.Fatalf("narrow footer exceeded its wrap-safe width: %d", widths[1])
	}
}

func TestFooterShowsPiAutoCompactMarker(t *testing.T) {
	rows, _ := formatFooter(PresentationSnapshot{
		ContextEstimate: "8%/100k", AutoCompact: true, Model: "m", NoColor: true,
	}, 80, 3, true)
	if !strings.Contains(Sanitize(rows[1]), "8%/100k (auto)") {
		t.Fatalf("enabled auto-compaction marker missing from footer: %q", rows[1])
	}
	rows, _ = formatFooter(PresentationSnapshot{
		ContextEstimate: "8%/100k", Model: "m", NoColor: true,
	}, 80, 3, true)
	if strings.Contains(Sanitize(rows[1]), "(auto)") {
		t.Fatalf("disabled auto-compaction marker appeared in footer: %q", rows[1])
	}
}

func TestFooterOmitsProviderWhenOnlyOneProviderIsAvailable(t *testing.T) {
	rows, _ := formatFooter(PresentationSnapshot{
		Provider: "anthropic", ProviderCount: 1, Model: "m", Thinking: "high", NoColor: true,
	}, 80, 3, true)
	line := Sanitize(rows[1])
	if strings.Contains(line, "anthropic") || !strings.Contains(line, "m • high") {
		t.Fatalf("single-provider footer should omit the provider label: %q", line)
	}
}

func TestFooterOmitsProviderWhenPiWouldNeedMoreRoom(t *testing.T) {
	rows, _ := formatFooter(PresentationSnapshot{
		Usage: "↑12k", ContextEstimate: "8%/100k", Provider: "anthropic", ProviderCount: 2, Model: "m", Thinking: "high", NoColor: true,
	}, 22, 3, true)
	if len(rows) != 2 {
		t.Fatalf("footer rows=%q", rows)
	}
	line := Sanitize(rows[1])
	if strings.Contains(line, "anthropic") || line != "↑12k 8%/100k  m • hig" {
		t.Fatalf("narrow footer should preserve stats, omit provider, then clip the model side like Pi: %q", line)
	}
	if CellWidth(line) > 21 {
		t.Fatalf("footer exceeded its wrap-safe width: %q", line)
	}
}

func TestFooterUsesPiEllipsisForLongPathAndStats(t *testing.T) {
	pathRows, _ := formatFooter(PresentationSnapshot{
		WorkingDirectory: filepath.FromSlash("/very/long/project"), SessionName: "release audit", NoColor: true,
	}, 12, 3, true)
	if got, want := Sanitize(pathRows[0]), filepath.FromSlash("/very/lo..."); got != want {
		t.Fatalf("long path footer row=%q; want Pi-style ellipsis", got)
	}
	widePathRows, _ := formatFooter(PresentationSnapshot{
		WorkingDirectory: filepath.FromSlash("/界界界界"), SessionName: "release audit", NoColor: true,
	}, 12, 3, true)
	if got, want := Sanitize(widePathRows[0]), filepath.FromSlash("/界界界..."); got != want || CellWidth(got) > 11 {
		t.Fatalf("wide path footer row=%q width=%d; want grapheme-safe Pi-style ellipsis %q", got, CellWidth(got), want)
	}

	statsRows, _ := formatFooter(PresentationSnapshot{
		Usage: "↑1234567890", ContextEstimate: "10%/100k", Model: "m", NoColor: true,
	}, 12, 3, true)
	if got := Sanitize(statsRows[1]); got != "↑1234567..." {
		t.Fatalf("long stats footer row=%q; want Pi-style ellipsis", got)
	}
}

func TestFooterRowWidthHoldsWithCacheStats(t *testing.T) {
	rows, _ := formatFooter(PresentationSnapshot{
		Usage: "↑126 ↓30 R874 CH87.4%", ContextEstimate: "8%/100k",
		Model: "a-very-long-model-name-that-does-not-fit", NoColor: true,
	}, 40, 3, true)
	for i, row := range rows {
		if w := CellWidth(Sanitize(row)); w > 40 {
			t.Fatalf("footer row %d width=%d; want at most 40", i, w)
		}
	}
	if got := Sanitize(rows[1]); !strings.Contains(got, "CH87.4") {
		t.Fatalf("cache segments clipped from stats row: %q", got)
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
	if !strings.HasPrefix(view.Rows[1], "── ⠋ Working…") {
		t.Fatalf("working status is not at the left of the top border: %q", view.Rows[1])
	}
	bottom := view.Rows[9]
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

func TestFooterQuotaSuffixUsesOnlyUnusedGap(t *testing.T) {
	quota := "[████▎░░░░░] Codex: 58% Left"
	without, withoutWidths := formatFooter(PresentationSnapshot{
		ContextPercent: 40, ContextUsageLabel: "40%", ContextWindowLabel: "100k",
		Model: "m", NoColor: true,
	}, 120, 3, true)
	with, withWidths := formatFooter(PresentationSnapshot{
		ContextPercent: 40, ContextUsageLabel: "40%", ContextWindowLabel: "100k",
		Model: "m", Quota: quota, QuotaToken: TokenSuccess, NoColor: true,
	}, 120, 3, true)
	if len(with) != 2 || len(without) != 2 {
		t.Fatalf("footer rows = %d, want 2", len(with))
	}
	if with[0] != without[0] {
		t.Fatalf("quota changed the path row: %q != %q", with[0], without[0])
	}
	if withWidths[1] != withoutWidths[1] || CellWidth(Sanitize(with[1])) != CellWidth(Sanitize(without[1])) {
		t.Fatalf("quota changed the footer width: %d != %d", withWidths[1], withoutWidths[1])
	}
	line := Sanitize(with[1])
	for _, want := range []string{" " + quota + " ", "] 40%/100k", "m"} {
		if !strings.Contains(line, want) {
			t.Fatalf("quota footer missing %q: %q", want, line)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(line, " "), " "+quota+" m") {
		t.Fatalf("quota was not right-aligned against the model side: %q", line)
	}
	colored := PresentationSnapshot{
		ContextPercent: 40, ContextUsageLabel: "40%", ContextWindowLabel: "100k",
		Model: "m", Quota: quota, QuotaToken: TokenSuccess,
	}
	coloredRows, _ := formatFooter(colored, 120, 3, false)
	if !strings.Contains(coloredRows[1], "\x1b[32m"+quota) {
		t.Fatalf("quota suffix did not use its token: %q", coloredRows[1])
	}
	if strings.Contains(Sanitize(without[1]), quota) {
		t.Fatalf("baseline footer already contained quota: %q", without[1])
	}

	presentFrom := 0
	for width := 40; width <= 140; width++ {
		without, withoutWidths := formatFooter(PresentationSnapshot{
			ContextPercent: 40, ContextUsageLabel: "40%", ContextWindowLabel: "100k",
			Model: "模型✓", NoColor: true,
		}, width, 3, true)
		with, withWidths := formatFooter(PresentationSnapshot{
			ContextPercent: 40, ContextUsageLabel: "40%", ContextWindowLabel: "100k",
			Model: "模型✓", Quota: quota, QuotaToken: TokenSuccess, NoColor: true,
		}, width, 3, true)
		if with[0] != without[0] {
			t.Fatalf("width %d: quota changed the path row: %q != %q", width, with[0], without[0])
		}
		if CellWidth(Sanitize(with[1])) != CellWidth(Sanitize(without[1])) || withWidths[1] != withoutWidths[1] {
			t.Fatalf("width %d: quota changed the footer width: %d != %d", width, CellWidth(Sanitize(with[1])), CellWidth(Sanitize(without[1])))
		}
		line := Sanitize(with[1])
		if strings.Contains(line, quota) {
			if presentFrom == 0 {
				presentFrom = width
			}
			if !strings.Contains(line, "] 40%/100k") || !strings.HasSuffix(strings.TrimRight(line, " "), " "+quota+" 模型✓") {
				t.Fatalf("width %d: quota displaced context or model text or was not right-aligned: %q", width, line)
			}
			continue
		}
		if strings.Join(with, "\n") != strings.Join(without, "\n") {
			t.Fatalf("width %d: footer changed without showing quota:\nwith=%q\nwithout=%q", width, with, without)
		}
	}
	if presentFrom == 0 {
		t.Fatal("quota never appeared between 40 and 140 columns")
	}
	if presentFrom > 40 {
		without, _ := formatFooter(PresentationSnapshot{
			ContextPercent: 40, ContextUsageLabel: "40%", ContextWindowLabel: "100k",
			Model: "模型✓", NoColor: true,
		}, presentFrom-1, 3, true)
		with, _ := formatFooter(PresentationSnapshot{
			ContextPercent: 40, ContextUsageLabel: "40%", ContextWindowLabel: "100k",
			Model: "模型✓", Quota: quota, QuotaToken: TokenSuccess, NoColor: true,
		}, presentFrom-1, 3, true)
		if strings.Join(with, "\n") != strings.Join(without, "\n") {
			t.Fatalf("width %d: quota fit below its first fitting width %d", presentFrom-1, presentFrom)
		}
	}
}

func TestFooterQuotaExpiresAtCompositionTime(t *testing.T) {
	quota := "[░░░░░░░░░░] Codex: 100% Left"
	base := PresentationSnapshot{
		ContextPercent: 40, ContextUsageLabel: "40%", ContextWindowLabel: "100k",
		Model: "m", NoColor: true,
	}
	with := base
	with.Quota = quota
	with.QuotaToken = TokenMuted
	future := time.Now().Add(time.Hour)
	with.QuotaDeadline = future
	rows, _ := formatFooter(with, 120, 3, true)
	if !strings.Contains(Sanitize(rows[1]), quota) {
		t.Fatalf("future deadline hid quota: %q", rows[1])
	}
	with.QuotaDeadline = time.Now().Add(-time.Second)
	expired, _ := formatFooter(with, 120, 3, true)
	baseline, _ := formatFooter(base, 120, 3, true)
	if strings.Join(expired, "\n") != strings.Join(baseline, "\n") {
		t.Fatalf("expired quota changed the footer:\nexpired=%q\nbaseline=%q", expired, baseline)
	}
}

func TestFooterBarEighthsAndCells(t *testing.T) {
	for _, tc := range []struct {
		percent float64
		eighths int
	}{
		{0, 0},
		{0.01, 1},
		{42, 34},
		{50, 40},
		{99.5, 80},
		{100, 80},
	} {
		if got := FooterBarEighths(tc.percent, 10); got != tc.eighths {
			t.Fatalf("FooterBarEighths(%v, 10) = %d, want %d", tc.percent, got, tc.eighths)
		}
		filled, empty := FooterBarCells(tc.eighths, 10)
		if CellWidth(filled)+CellWidth(empty) != 10 {
			t.Fatalf("FooterBarCells(%d, 10) widths = %d + %d", tc.eighths, CellWidth(filled), CellWidth(empty))
		}
	}
}
