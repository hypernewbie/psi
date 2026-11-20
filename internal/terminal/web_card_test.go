package terminal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/event"
	pitheme "github.com/hypernewbie/psi/internal/theme"
)

// TestWebCardLayoutSafety formats one hostile card at a narrow and a wide
// width. Untrusted fields must never add rows or control sequences, and the
// card must keep its call-above-result order.
func TestWebCardLayoutSafety(t *testing.T) {
	call := event.WebCall{
		Title: "fetch https://example.com/" + strings.Repeat("路径/", 40) + "\x1b[31mEVIL\nROW2",
		Mode:  "raw\nmode-injected",
	}
	result := &event.WebResult{
		Title:     "标题\nSECOND" + strings.Repeat("😀", 100),
		Chars:     1234,
		HasChars:  true,
		Truncated: true,
		Preview: []string{
			"PREVIEW " + strings.Repeat("😀", 150) + "\x1b]0;injected\x07",
			"second\npreview row",
		},
	}
	web := event.WebDisplay{Call: call, Result: result}
	display := DisplaySettings{OutputPad: 0, AutocompleteMaxVisible: 3, CodeBlockIndent: "  "}
	doc, err := pitheme.BuiltinDocument("dark")
	if err != nil {
		t.Fatal(err)
	}
	palette, err := doc.Compile(pitheme.ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}

	for _, width := range []int{40, 120} {
		plain := FormatWebCardRows("fetch_content", "done", web, nil, width, true, display)
		if len(plain) < 3 {
			t.Fatalf("width %d: card rows = %d", width, len(plain))
		}
		for _, row := range plain {
			if got := CellWidth(Sanitize(row)); got > width-1 {
				t.Fatalf("width %d: row is %d cells: %q", width, got, row)
			}
			if strings.ContainsAny(row, "\r\n") || strings.Contains(row, "\x1b") || strings.Contains(row, "\x07") {
				t.Fatalf("width %d: row carried a control sequence: %q", width, row)
			}
		}
		joined := strings.Join(plain, "\n")
		if strings.Contains(joined, "injected") || strings.Contains(joined, "mode-injected") {
			// The mode text itself is literal data, but the injected OSC and
			// row separator must be gone; only the folded text may remain.
			if strings.Contains(joined, "\x1b]") {
				t.Fatalf("width %d: OSC sequence survived: %q", width, joined)
			}
		}
		callRow := -1
		titleRow := -1
		previewRow := -1
		for i, row := range plain {
			stripped := strings.TrimSpace(Sanitize(row))
			switch {
			case callRow < 0 && strings.HasPrefix(stripped, "fetch"):
				callRow = i
			case titleRow < 0 && strings.Contains(stripped, "chars)"):
				titleRow = i
			case previewRow < 0 && strings.Contains(stripped, "PREVIEW"):
				previewRow = i
			}
		}
		folded := strings.ReplaceAll(strings.Join(plain, ""), " ", "")
		if !strings.Contains(folded, "EVIL") || !strings.Contains(folded, "ROW2") {
			t.Fatalf("width %d: folded hostile title missing: %#v", width, plain)
		}
		if callRow < 0 || titleRow < 0 || previewRow < 0 {
			t.Fatalf("width %d: missing card rows (call=%d title=%d preview=%d): %#v", width, callRow, titleRow, previewRow, plain)
		}
		if !(callRow < titleRow && titleRow < previewRow) {
			t.Fatalf("width %d: card order call=%d title=%d preview=%d", width, callRow, titleRow, previewRow)
		}
		if !strings.Contains(strings.Join(plain, "\n"), "[truncated]") {
			t.Fatalf("width %d: truncation label missing", width)
		}

		colored := FormatWebCardRows("fetch_content", "done", web, nil, width, false, display, palette)
		if len(colored) != len(plain) {
			t.Fatalf("width %d: colored rows = %d, plain rows = %d", width, len(colored), len(plain))
		}
		for i := range colored {
			if CellWidth(Sanitize(colored[i])) != CellWidth(Sanitize(plain[i])) {
				t.Fatalf("width %d: color changed row %d width", width, i)
			}
			if Sanitize(colored[i]) != Sanitize(plain[i]) {
				t.Fatalf("width %d: color changed row %d text", width, i)
			}
		}
	}

	// The shared rows formatter and the document block must produce the same
	// layout for the same width.
	summary := event.ToolSummary{Kind: "fetch_content", Available: false, Web: &web}
	rows := FormatToolRowsWithDisplay(summary, "fetch_content", "done", 60, true, display)
	direct := FormatWebCardRows("fetch_content", "done", web, nil, 60, true, display)
	if strings.Join(rows, "\n") != strings.Join(direct, "\n") {
		t.Fatalf("rows formatter differs from the card formatter:\n%#v\n%#v", rows, direct)
	}
	blockRows := FormatDocumentBlock(DocumentBlock{Kind: DocumentTool, ToolName: "fetch_content", ToolState: "done", Summary: summary}, 60, PresentationSnapshot{NoColor: true, Display: display})
	if strings.Join(blockRows, "\n") != strings.Join(rows, "\n") {
		t.Fatalf("document block differs from the rows formatter:\n%#v\n%#v", blockRows, rows)
	}

	// Other tools keep the existing single-row layout.
	readSummary := event.ToolSummary{Kind: "read", Path: "a.txt", Available: true}
	if got := FormatToolRowsWithDisplay(readSummary, "read", "done", 60, true, display); len(got) != 1 || got[0] != FormatToolRowWithDisplay(readSummary, "read", "done", 60, true, display) {
		t.Fatalf("non-web tool changed layout: %#v", got)
	}

	// Narrow widths first drop the outer spacer and continuation rows; the
	// title survives while any row fits. With no usable column the card is
	// hidden instead of printed broken.
	clipped := clipWebCardRows(rows, 1)
	if len(clipped) != 1 || !strings.HasPrefix(strings.TrimSpace(clipped[0]), "fetch") {
		t.Fatalf("title did not survive clipping: %#v", clipped)
	}
	if got := FormatWebCardRows("fetch_content", "done", web, nil, 4, true, display); got != nil {
		t.Fatalf("card printed without a usable column: %#v", got)
	}
	if got := FormatWebCardRows("fetch_content", "done", web, nil, 8, true, display); len(got) == 0 {
		t.Fatal("narrow card was hidden with a usable column")
	}
}

// TestWebCardLayoutBranches formats the batch, search, and retrieval cards.
// Each shape keeps the call above the result line at both narrow and wide
// widths, and the shared bounds keep every row inside the terminal.
func TestWebCardLayoutBranches(t *testing.T) {
	display := DisplaySettings{OutputPad: 0, AutocompleteMaxVisible: 3, CodeBlockIndent: "  "}
	cases := []struct {
		name   string
		web    event.WebDisplay
		want   []string
		noWant []string
	}{
		{
			name: "batch",
			web: event.WebDisplay{
				Call: event.WebCall{Title: "fetch 6 URLs", Entries: []string{
					"https://example.com/1", "https://example.com/2", "https://example.com/3",
					"https://example.com/4", "https://example.com/5",
				}, Overflow: 1, Mode: "raw"},
				Result: &event.WebResult{Kind: event.WebResultBatch, OK: 5, Total: 6, HasCounts: true, Stored: true},
			},
			want:   []string{"fetch 6 URLs", "... and 1 more", "mode: raw", "5/6 URLs (content stored)"},
			noWant: []string{"content fetched"},
		},
		{
			name: "search",
			web: event.WebDisplay{
				Call: event.WebCall{Title: "search 3 queries", Entries: []string{`"one"`, `"two"`, `"three"`}},
				Result: &event.WebResult{Kind: event.WebResultSearch, OK: 2, Total: 3, HasCounts: true,
					Sources: 1, HasSources: true, Stored: true, Preview: []string{"first content line"}},
			},
			want: []string{"search 3 queries", `"one"`, "2/3 queries, 1 sources (content ready)", "first content line"},
		},
		{
			name: "retrieval",
			web: event.WebDisplay{
				Call: event.WebCall{Title: "get_content urlIndex=0"},
				Result: &event.WebResult{Kind: event.WebResultContent, Title: "Hit Page", Chars: 12,
					HasChars: true, HasRange: true, RangeStart: 2, RangeEnd: 5},
			},
			want: []string{"get_content urlIndex=0", "Hit Page (12 chars, showing 2-5)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, width := range []int{40, 120} {
				rows := FormatWebCardRows("fetch_content", "done", tc.web, nil, width, true, display)
				if len(rows) < 2 {
					t.Fatalf("width %d: rows = %#v", width, rows)
				}
				for _, row := range rows {
					if got := CellWidth(Sanitize(row)); got > width-1 {
						t.Fatalf("width %d: row is %d cells: %q", width, got, row)
					}
					if strings.ContainsAny(row, "\r\n\x1b") {
						t.Fatalf("width %d: row carried a control sequence: %q", width, row)
					}
				}
				joined := strings.Join(rows, "\n")
				// Width wrapping can split a status line, so order checks use a
				// folded view that ignores layout-owned breaks.
				fold := func(text string) string {
					return strings.ReplaceAll(strings.ReplaceAll(text, "\n", ""), " ", "")
				}
				folded := fold(joined)
				position := -1
				for _, want := range tc.want {
					next := strings.Index(folded, fold(want))
					if next < 0 {
						t.Fatalf("width %d: missing %q in %#v", width, want, rows)
					}
					if next < position {
						t.Fatalf("width %d: %q is out of order: %#v", width, want, rows)
					}
					position = next
				}
				for _, unwanted := range tc.noWant {
					if strings.Contains(folded, fold(unwanted)) {
						t.Fatalf("width %d: unexpected %q: %#v", width, unwanted, rows)
					}
				}
			}
		})
	}
}

// TestWebCardExpansionAndDuration covers the display-only additions that do
// not need a PTY: the collapsed/expanded preview budgets, the search hint, the
// measured duration suffix, and the ten-cell progress bar.
func TestWebCardExpansionAndDuration(t *testing.T) {
	display := DisplaySettings{OutputPad: 0, AutocompleteMaxVisible: 3, CodeBlockIndent: "  "}
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("PREVIEW LINE %d %s", i, strings.Repeat("x", 30)))
	}
	page := event.WebDisplay{
		Call: event.WebCall{Title: "fetch https://example.com/page"},
		Result: &event.WebResult{
			Kind: event.WebResultPage, Title: "Page", Chars: 900, HasChars: true,
			Preview: lines, DurationMs: 1500, HasDuration: true,
		},
	}
	collapsed := strings.Join(FormatWebCardRows("fetch_content", "done", page, nil, 60, true, display), "\n")
	display.ExpandWeb = true
	expanded := strings.Join(FormatWebCardRows("fetch_content", "done", page, nil, 60, true, display), "\n")
	display.ExpandWeb = false
	if !strings.Contains(collapsed, "1.5s total") {
		t.Fatalf("collapsed page lost the duration suffix: %q", collapsed)
	}
	if strings.Contains(collapsed, "PREVIEW LINE 9") {
		t.Fatalf("collapsed page exceeded its preview budget: %q", collapsed)
	}
	if !strings.Contains(expanded, "PREVIEW LINE 9") {
		t.Fatalf("expanded page did not grow: %q", expanded)
	}

	short := page
	short.Result = &event.WebResult{Kind: event.WebResultPage, Title: "Page", HasChars: true, HasDuration: true, DurationMs: 350}
	if got := strings.Join(FormatWebCardRows("fetch_content", "done", short, nil, 60, true, display), "\n"); !strings.Contains(got, "350ms total") {
		t.Fatalf("sub-second duration = %q", got)
	}

	search := event.WebDisplay{
		Call: event.WebCall{Title: `search "one"`},
		Result: &event.WebResult{
			Kind: event.WebResultSearch, OK: 1, Total: 1, HasCounts: true, Sources: 1, HasSources: true,
			Preview: lines,
		},
	}
	searchCollapsed := strings.Join(FormatWebCardRows("fetch_content", "done", search, nil, 60, true, display), "\n")
	if !strings.Contains(searchCollapsed, "ctrl+o to expand") {
		t.Fatalf("collapsed search lost the expansion hint: %q", searchCollapsed)
	}
	display.ExpandWeb = true
	searchExpanded := strings.Join(FormatWebCardRows("fetch_content", "done", search, nil, 60, true, display), "\n")
	display.ExpandWeb = false
	if strings.Contains(searchExpanded, "ctrl+o to expand") || !strings.Contains(searchExpanded, "PREVIEW LINE 9") {
		t.Fatalf("expanded search still hints or lacks rows: %q", searchExpanded)
	}
	// A search whose preview fits has no hint.
	shortSearch := search
	shortSearch.Result = &event.WebResult{Kind: event.WebResultSearch, OK: 1, Total: 1, HasCounts: true, Sources: 1, HasSources: true, Preview: []string{"one line"}}
	if got := strings.Join(FormatWebCardRows("fetch_content", "done", shortSearch, nil, 60, true, display), "\n"); strings.Contains(got, "ctrl+o") {
		t.Fatalf("short search kept a dead hint: %q", got)
	}

	// Batch and retrieval show their extra rows only when expanded.
	batch := event.WebDisplay{
		Call:   event.WebCall{Title: "fetch 2 URLs"},
		Result: &event.WebResult{Kind: event.WebResultBatch, OK: 2, Total: 2, HasCounts: true, Expanded: []string{"https://example.com/a: ALPHA", "https://example.com/b: BETA"}},
	}
	if got := strings.Join(FormatWebCardRows("fetch_content", "done", batch, nil, 60, true, display), "\n"); strings.Contains(got, "ALPHA") {
		t.Fatalf("collapsed batch showed expanded rows: %q", got)
	}
	display.ExpandWeb = true
	if got := strings.Join(FormatWebCardRows("fetch_content", "done", batch, nil, 60, true, display), "\n"); !strings.Contains(got, "ALPHA") {
		t.Fatalf("expanded batch lost page rows: %q", got)
	}
	display.ExpandWeb = false

	progress := event.WebProgress{Phase: "fetching URLs", Completed: 1, Total: 2}
	if got := webProgressLine(progress); got != "[█████░░░░░] fetching URLs 1/2" {
		t.Fatalf("progress line = %q", got)
	}
	if got := webProgressLine(event.WebProgress{Phase: "searching", Completed: 9, Total: 2}); !strings.HasPrefix(got, "[██████████]") {
		t.Fatalf("progress ratio was not clamped: %q", got)
	}
	if got := webProgressLine(event.WebProgress{Phase: "fetching", Total: 0}); !strings.HasPrefix(got, "[░░░░░░░░░░]") {
		t.Fatalf("zero total filled the bar: %q", got)
	}
	pending := strings.Join(FormatWebCardRows("fetch_content", "running", event.WebDisplay{Call: event.WebCall{Title: "fetch 2 URLs"}}, &progress, 60, true, display), "\n")
	if !strings.Contains(pending, "[█████░░░░░] fetching URLs 1/2") {
		t.Fatalf("pending card lacks the progress row: %q", pending)
	}
	settled := strings.Join(FormatWebCardRows("fetch_content", "done", event.WebDisplay{
		Call:   event.WebCall{Title: "fetch 2 URLs"},
		Result: &event.WebResult{Kind: event.WebResultBatch, OK: 2, Total: 2, HasCounts: true},
	}, &progress, 60, true, display), "\n")
	if strings.Contains(settled, "fetching URLs") {
		t.Fatalf("settled card kept progress: %q", settled)
	}
}

// TestWebCardStateNeverShowsSuccessData protects the explicit non-success
// states: a cancelled or not-started card must not render joined result data.
func TestWebCardStateNeverShowsSuccessData(t *testing.T) {
	display := DisplaySettings{OutputPad: 0, AutocompleteMaxVisible: 3, CodeBlockIndent: "  "}
	web := event.WebDisplay{
		Call: event.WebCall{Title: "fetch https://example.com/page"},
		Result: &event.WebResult{Kind: event.WebResultPage, Title: "Secret Result", Chars: 5, HasChars: true,
			Preview: []string{"SECRET_PREVIEW"}},
	}
	for _, state := range []string{"cancelled", "not started", "interrupted", "failed"} {
		joined := strings.Join(FormatWebCardRows("fetch_content", state, web, nil, 60, true, display), "\n")
		if strings.Contains(joined, "Secret Result") || strings.Contains(joined, "SECRET_PREVIEW") {
			t.Fatalf("state %q rendered success data: %q", state, joined)
		}
		want := map[string]string{
			"cancelled":   "Cancelled",
			"not started": "Not started",
			"interrupted": "Outcome unknown",
			"failed":      "Error: request failed",
		}[state]
		if !strings.Contains(joined, want) {
			t.Fatalf("state %q lacks %q: %q", state, want, joined)
		}
	}
}

// TestWebCardTitleFallsBackToKind covers restored summaries that carry the
// tool kind but no separate name: the card must still say "fetch", not "tool".
func TestWebCardTitleFallsBackToKind(t *testing.T) {
	display := DisplaySettings{OutputPad: 0, AutocompleteMaxVisible: 3, CodeBlockIndent: "  "}
	summary := event.ToolSummary{Kind: "fetch_content"}
	rows := FormatToolRowsWithDisplay(summary, "", "interrupted", 60, true, display)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "fetch") || strings.Contains(joined, "tool") {
		t.Fatalf("kind-only card title = %q", joined)
	}
	if !strings.Contains(joined, "Outcome unknown") {
		t.Fatalf("kind-only card lost its state: %q", joined)
	}
}
