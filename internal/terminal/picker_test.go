package terminal

import (
	"strings"
	"testing"
)

func TestChoicePickerIsBoundedAndHonorsNoColor(t *testing.T) {
	picker := &ChoicePicker{Title: "Trust project folder?", Context: "/synthetic/workspace", Selected: 3, Options: []ChoiceOption{
		{Label: "Trust"},
		{Label: "Trust (this session only)"},
		{Label: "Do not trust"},
		{Label: "Do not trust (this session only)", Current: true},
	}}
	rows := picker.Rows(40, true)
	if len(rows) != 7 || !strings.Contains(rows[0], "Trust project folder?") || !strings.Contains(rows[1], "/synthetic/workspace") || !strings.Contains(rows[2], "Trust") || !strings.Contains(rows[4], "Do not trust") {
		t.Fatalf("picker rows: %q", rows)
	}
	if strings.Contains(strings.Join(rows, "\n"), "\x1b[") {
		t.Fatalf("NO_COLOR emitted SGR: %q", rows)
	}
	for _, row := range rows {
		if CellWidth(row) > 39 {
			t.Fatalf("row exceeded width: %q", row)
		}
	}
	picker.Move(-1)
	if option, ok := picker.SelectedOption(); !ok || option.Label != "Do not trust" {
		t.Fatalf("selection did not move: %+v, %v", option, ok)
	}
	picker.Move(-1)
	if option, _ := picker.SelectedOption(); option.Label != "Trust (this session only)" {
		t.Fatalf("selection did not move up: %+v", option)
	}
	picker.Move(-1)
	picker.Move(-1)
	if option, _ := picker.SelectedOption(); option.Label != "Trust" {
		t.Fatalf("selection moved below first row: %+v", option)
	}

	small := &ChoicePicker{Title: "Trust project folder?", Selected: 1, Options: []ChoiceOption{
		{Label: "Trust"},
		{Label: "Do not trust (this session only)", Current: true},
	}}
	tiny := ComposeConsoleViewPicker(20, 6, nil, 0, PresentationSnapshot{NoColor: true}, "", nil, small.Rows(20, true), PickerChoice)
	visible := strings.Join(tiny.Rows, "\n")
	if !strings.Contains(visible, "→ ✓ Do not trust") || !strings.Contains(visible, "Trust") {
		t.Fatalf("tiny choice picker hid its selected/default-deny choices: %q", tiny.Rows)
	}
}

// The choice picker hint stays on one row at narrow widths.
func TestChoicePickerHintFitsNarrowWidths(t *testing.T) {
	picker := &ChoicePicker{Title: "Project trust", Options: []ChoiceOption{{Label: "Deny"}}}
	for _, width := range []int{16, 20, 30, 40, 80} {
		rows := picker.Rows(width, true)
		hint := rows[len(rows)-1]
		if CellWidth(hint) > width-1 {
			t.Fatalf("choice hint does not fit width %d: %q", width, hint)
		}
	}
}
