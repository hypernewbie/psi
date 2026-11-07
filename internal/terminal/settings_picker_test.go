package terminal

import (
	"strings"
	"testing"
)

func testSettingsPicker(items int) *SettingsPicker {
	picker := &SettingsPicker{}
	labels := []string{"Auto-compact", "Editor padding", "Output padding", "Autocomplete", "Code indent", "Quiet startup", "Theme", "Color mode", "Extra"}
	for i := 0; i < items; i++ {
		picker.Items = append(picker.Items, SettingsItem{
			ID:          labels[i%len(labels)],
			Label:       labels[i%len(labels)],
			Description: "description " + labels[i%len(labels)],
			Values: []SettingsValue{
				{Label: "one", Value: 1},
				{Label: "two", Value: 2},
			},
		})
	}
	return picker
}

func TestSettingsPickerRowsShowsSelection(t *testing.T) {
	picker := testSettingsPicker(8)
	rows := picker.Rows(80, 12, true)
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	if !strings.Contains(rows[0], "→ Auto-compact") || !strings.Contains(rows[0], "one") {
		t.Fatalf("first row = %q", rows[0])
	}
	if picker.visibleCount != 8 {
		t.Fatalf("visibleCount = %d", picker.visibleCount)
	}
}

func TestSettingsPickerMoveWraps(t *testing.T) {
	picker := testSettingsPicker(3)
	picker.Move(-1)
	if picker.Index != 2 {
		t.Fatalf("index = %d, want 2", picker.Index)
	}
	picker.Move(1)
	if picker.Index != 0 {
		t.Fatalf("index = %d, want 0", picker.Index)
	}
}

func TestSettingsPickerCycleWraps(t *testing.T) {
	picker := testSettingsPicker(2)
	item, value, ok := picker.Cycle()
	if !ok || item.ID != "Auto-compact" || value.Label != "two" {
		t.Fatalf("cycle = %v %v %v", item.ID, value, ok)
	}
	if picker.Items[0].Selected != 1 || picker.Items[1].Selected != 0 {
		t.Fatal("cycle touched another row")
	}
	_, value, _ = picker.Cycle()
	if value.Label != "one" {
		t.Fatalf("second cycle = %v", value.Label)
	}
}

func TestSettingsPickerPageUsesVisibleCount(t *testing.T) {
	picker := testSettingsPicker(8)
	picker.Rows(80, 5, true)
	if picker.visibleCount < 1 {
		t.Fatalf("visibleCount = %d", picker.visibleCount)
	}
	picker.Page(1)
	if picker.Index == 0 {
		t.Fatal("page did not move")
	}
	picker.Page(100)
	if picker.Index != 7 {
		t.Fatalf("index = %d, want 7", picker.Index)
	}
}

func TestSettingsPickerShortBudgetKeepsRows(t *testing.T) {
	picker := testSettingsPicker(8)
	for budget := 1; budget <= 4; budget++ {
		rows := picker.Rows(80, budget, true)
		if len(rows) == 0 || len(rows) > budget {
			t.Fatalf("budget %d produced %d rows", budget, len(rows))
		}
		found := false
		for _, row := range rows {
			if strings.Contains(row, "→ Auto-compact") {
				found = true
			}
		}
		if !found {
			t.Fatalf("budget %d dropped the selected row: %q", budget, rows)
		}
	}
}

func TestSettingsPickerDropsDescriptionBeforeList(t *testing.T) {
	picker := testSettingsPicker(4)
	short := picker.Rows(80, 3, true)
	for _, row := range short {
		if strings.Contains(row, "description") {
			t.Fatalf("description at budget 3: %q", short)
		}
	}
	long := picker.Rows(80, 8, true)
	found := false
	for _, row := range long {
		if strings.Contains(row, "description Auto-compact") {
			found = true
		}
	}
	if !found {
		t.Fatalf("description missing at budget 8: %q", long)
	}
}

func TestSettingsPickerNarrowWidth(t *testing.T) {
	picker := testSettingsPicker(8)
	rows := picker.Rows(24, 10, true)
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, row := range rows {
		if CellWidth(row) > 23 {
			t.Fatalf("row too wide: %q (%d)", row, CellWidth(row))
		}
	}
}

func TestSettingsPickerShowsPositionWhenClipped(t *testing.T) {
	picker := testSettingsPicker(8)
	picker.Index = 7
	rows := picker.Rows(80, 5, true)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "(8/8)") {
		t.Fatalf("missing position: %q", rows)
	}
}

func TestSettingsPickerCapsVisibleRows(t *testing.T) {
	picker := testSettingsPicker(12)
	rows := picker.Rows(80, 20, true)
	if picker.visibleCount > 10 {
		t.Fatalf("visibleCount = %d", picker.visibleCount)
	}
	if !strings.Contains(strings.Join(rows, "\n"), "(1/12)") {
		t.Fatalf("missing position: %q", rows)
	}
}

func TestSettingsPickerNilSafe(t *testing.T) {
	var picker *SettingsPicker
	picker.Move(1)
	picker.Page(1)
	if rows := picker.Rows(80, 10, true); rows != nil {
		t.Fatalf("rows = %v", rows)
	}
	if _, _, ok := picker.Cycle(); ok {
		t.Fatal("nil picker cycled")
	}
}

func TestSettingsPickerNoColorOutput(t *testing.T) {
	picker := testSettingsPicker(3)
	rows := picker.Rows(80, 10, true)
	for _, row := range rows {
		if strings.Contains(row, "\x1b") {
			t.Fatalf("color output when disabled: %q", row)
		}
	}
}
