package terminal

import (
	"strings"
	"testing"
)

func TestPlan4ResponsiveTailAcrossFixtureSizes(t *testing.T) {
	draft := []rune(strings.Repeat("wide 界 e\u0301 🎉\n", 8))
	completion := []string{"› /resume  select workspace session", "  /review  inspect a change", "  /model  choose a model"}
	fragments := []string{"  committed fragment row", "  another active fragment row"}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {40, 12}, {20, 6}, {2, 2}, {1, 1}} {
		width, height := size[0], size[1]
		view := ComposeConsoleViewComponents(width, height, draft, len(draft), PresentationSnapshot{
			WorkingDirectory: strings.Repeat("/long/project/path", 3),
			Provider:         "anthropic", Model: strings.Repeat("example-model-", 3), Thinking: "high", NoColor: true,
		}, "Working… (Esc/Ctrl+C cancel)", fragments, completion)
		if len(view.Rows) == 0 || len(view.Rows) > height {
			t.Fatalf("%dx%d rendered %d rows", width, height, len(view.Rows))
		}
		if view.CursorRow < 0 || view.CursorRow >= len(view.Rows) || view.CursorCol < 0 || view.CursorCol >= width {
			t.Fatalf("%dx%d cursor out of bounds: %+v rows=%q", width, height, view, view.Rows)
		}
		for _, row := range view.Rows {
			if CellWidth(row) > width || strings.Contains(row, "\x1b[") {
				t.Fatalf("%dx%d unsafe row %q width=%d", width, height, row, CellWidth(row))
			}
		}
	}
}
