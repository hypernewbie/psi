package terminal

import (
	"strings"
	"testing"
)

func TestChoicePickerIsBoundedAndHonorsNoColor(t *testing.T) {
	picker := &ChoicePicker{Title: "Project trust", Context: "/synthetic/workspace", Selected: 1, Options: []ChoiceOption{
		{Label: "Trust for this run", Description: "load protected project resources"},
		{Label: "Deny", Description: "ignore protected project resources", Current: true},
	}}
	rows := picker.Rows(40, true)
	if len(rows) != 4 || !strings.Contains(rows[0], "Deny") || !strings.Contains(rows[1], "Trust for this run") {
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
	if option, ok := picker.SelectedOption(); !ok || option.Label != "Trust for this run" {
		t.Fatalf("selection did not move: %+v, %v", option, ok)
	}
	picker.Move(-1)
	if option, _ := picker.SelectedOption(); option.Label != "Trust for this run" {
		t.Fatalf("selection moved below first row: %+v", option)
	}

	picker.Selected = 1
	tiny := ComposeConsoleViewPicker(20, 6, nil, 0, PresentationSnapshot{NoColor: true}, "", nil, picker.Rows(20, true), PickerChoice)
	visible := strings.Join(tiny.Rows, "\n")
	if !strings.Contains(visible, "› Deny") || !strings.Contains(visible, "Trust for this") {
		t.Fatalf("tiny choice picker hid its selected/default-deny choices: %q", tiny.Rows)
	}
}
