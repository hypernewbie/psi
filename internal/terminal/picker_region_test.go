package terminal

import (
	"strings"
	"testing"
)

// The picker replaces the editor region, like Pi's showSelector. It owns its
// top border, the search query row, the bounded list, and its closing border.
// The editor block must not also render underneath it.
func TestPickerRegionReplacesTheEditorBlock(t *testing.T) {
	query := []rune("gpt-5")
	rows := []string{"Models · 2 matches", "→ ✓ gpt-5 [openai-codex]", "  gpt-5.6-luna [openai-codex]"}
	view := ComposeConsoleViewPicker(80, 24, query, len(query), PresentationSnapshot{WorkingDirectory: "/work", Model: "gpt-5.6-luna", NoColor: true}, "", nil, rows, PickerSearch)

	joined := strings.Join(view.Rows, "\n")
	if borders := countBorderRows(view.Rows); borders != 2 {
		t.Fatalf("picker did not own exactly one border pair: %q", view.Rows)
	}
	if view.Rows[0] != strings.Repeat("─", 79) {
		t.Fatalf("picker top border is not Pi's dynamic border: %q", view.Rows[0])
	}
	if view.Rows[len(view.Rows)-3] != strings.Repeat("─", 79) {
		t.Fatalf("picker closing border is missing: %q", view.Rows)
	}
	if !strings.HasPrefix(view.Rows[1], "> gpt-5") {
		t.Fatalf("picker did not own Pi's search query row: %q", view.Rows[1])
	}
	if view.CursorRow != 1 || view.CursorCol != 7 {
		t.Fatalf("picker cursor is not on the query row: row=%d col=%d rows=%q", view.CursorRow, view.CursorCol, view.Rows)
	}
	// The editor draft is the query, so the editor block itself must not be
	// rendered a second time below the picker.
	if strings.Count(joined, "> gpt-5") != 1 {
		t.Fatalf("picker rendered the editor block underneath the selector: %q", view.Rows)
	}
}

func countBorderRows(rows []string) int {
	count := 0
	for _, row := range rows {
		if strings.Contains(row, "─") {
			count++
		}
	}
	return count
}

// The editor region keeps its own borders while a picker is not active.
func TestEditorRegionStillRendersWhenNoPickerIsOpen(t *testing.T) {
	view := ComposeConsoleView(80, 24, []rune("draft"), 5, PresentationSnapshot{WorkingDirectory: "/work", Model: "m", NoColor: true}, "", "")
	joined := strings.Join(view.Rows, "\n")
	if countBorderRows(view.Rows) != 2 || !strings.Contains(joined, "draft") {
		t.Fatalf("editor region lost its borders or draft: %q", view.Rows)
	}
}

// Pi's search input scrolls horizontally so the query cursor stays visible.
func TestPickerQueryScrollsToKeepTheCursorVisible(t *testing.T) {
	long := "PREFIX" + strings.Repeat("x", 200) + "END"
	buf := []rune(long)
	pos := len(buf)
	view := ComposeConsoleViewPicker(40, 24, buf, pos, PresentationSnapshot{NoColor: true}, "", nil, []string{"row"}, PickerSearch)
	query := view.Rows[1]
	if strings.Contains(query, "PREFIX") {
		t.Fatalf("long query did not scroll away from its start: %q", query)
	}
	if !strings.HasSuffix(query, "END") {
		t.Fatalf("long query hid the cursor end: %q", query)
	}
	if view.CursorCol != 39 {
		t.Fatalf("long query cursor column is wrong: %d", view.CursorCol)
	}
	if CellWidth(query) > 39 {
		t.Fatalf("long query overflowed the row: %q (%d)", query, CellWidth(query))
	}
}

// A picker never grows past the terminal height, and the query row plus at
// least one list row survive the smallest plan sizes.
func TestPickerRegionStaysInsideTheTerminalHeight(t *testing.T) {
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = "row"
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {40, 12}, {20, 6}, {20, 4}, {20, 3}} {
		width, height := size[0], size[1]
		view := ComposeConsoleViewPicker(width, height, nil, 0, PresentationSnapshot{NoColor: true}, "", nil, rows, PickerSearch)
		if len(view.Rows) > height {
			t.Fatalf("%dx%d picker grew past the terminal: %d rows", width, height, len(view.Rows))
		}
		joined := strings.Join(view.Rows, "\n")
		if !strings.Contains(joined, "> ") {
			t.Fatalf("%dx%d picker hid its query row: %q", width, height, view.Rows)
		}
		for _, row := range view.Rows {
			if CellWidth(row) > width {
				t.Fatalf("%dx%d picker row overflowed: %q", width, height, row)
			}
		}
	}
}

// The picker region never emits SGR when the presentation disables color.
func TestPickerRegionHonorsNoColor(t *testing.T) {
	view := ComposeConsoleViewPicker(80, 24, []rune("q"), 1, PresentationSnapshot{NoColor: true}, "", nil, []string{"→ ✓ model [provider]"}, PickerSearch)
	for _, row := range view.Rows {
		if strings.Contains(row, "\x1b[") {
			t.Fatalf("NO_COLOR picker emitted SGR: %q", row)
		}
	}
}

// The choice picker has no search query, so it renders only its own rows.
func TestChoicePickerRegionHasNoQueryRow(t *testing.T) {
	choice := &ChoicePicker{Title: "Project trust", Context: "/work", Selected: 0, Options: []ChoiceOption{
		{Label: "Trust for this run"},
		{Label: "Deny", Current: true},
	}}
	view := ComposeConsoleViewPicker(80, 24, []rune("ignored"), 7, PresentationSnapshot{NoColor: true}, "", nil, choice.Rows(80, true), PickerChoice)
	joined := strings.Join(view.Rows, "\n")
	if strings.Contains(joined, "> ") {
		t.Fatalf("choice picker rendered a search query row: %q", view.Rows)
	}
	if !strings.Contains(joined, "Project trust") || !strings.Contains(joined, "Deny") {
		t.Fatalf("choice picker lost its title or options: %q", view.Rows)
	}
}
