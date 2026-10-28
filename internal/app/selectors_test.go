package app

import (
	"strings"
	"testing"
	"time"

	"psi/internal/session"
	"psi/internal/terminal"
)

func TestSelectorSearchUsesStableIdentityAndKeyboardSelection(t *testing.T) {
	items := []session.SessionInfo{
		{ID: "session-alpha", FirstMessage: "first synthetic prompt", Modified: time.Unix(1, 0)},
		{ID: "session-beta", FirstMessage: "second synthetic prompt", Modified: time.Unix(2, 0)},
		{ID: "session-gamma", FirstMessage: "third synthetic prompt", Modified: time.Unix(3, 0)},
	}
	s := NewSelector(items, "session-beta")
	if got := s.SelectedItem(); got == nil || got.ID != "session-beta" {
		t.Fatalf("active selection=%v", got)
	}
	s.SetFilter("gamma")
	if got := s.SelectedItem(); got == nil || got.ID != "session-gamma" {
		t.Fatalf("search did not select stable match: %v", got)
	}
	s.SetFilter("")
	if got := s.SelectedItem(); got == nil || got.ID != "session-gamma" {
		t.Fatalf("clearing search lost selected identity: %v", got)
	}
	s.Move(1)
	if got := s.SelectedItem(); got == nil || got.ID != "session-gamma" {
		t.Fatalf("session navigation should clamp at end: %v", got)
	}
	s.Move(-1)
	if got := s.SelectedItem(); got == nil || got.ID != "session-beta" {
		t.Fatalf("Up did not select prior stable identity: %v", got)
	}
	_, cancel, redraw := s.Resolve("gamma")
	if cancel || !redraw || s.Filter != "gamma" || s.SelectedItem().ID != "session-gamma" {
		t.Fatalf("Resolve should search rather than consume a numeric index: cancel=%v redraw=%v filter=%q", cancel, redraw, s.Filter)
	}
}

func TestSelectorFilterUsesSubstringsAndFirstMatchFallback(t *testing.T) {
	items := []session.SessionInfo{
		{ID: "first", FirstMessage: "Alpha response"},
		{ID: "active", FirstMessage: "Alpha beta"},
		{ID: "selected", FirstMessage: "Separate conversation"},
	}
	s := NewSelector(items, "active")
	s.Move(1)
	s.SetFilter(" ALPHA ")
	if len(s.Filtered) != 2 || s.Filtered[0].ID != "first" || s.Filtered[1].ID != "active" || s.SelectedID != "first" {
		t.Fatalf("filter must keep source order and select first match, not active item: %+v", s)
	}
	s.Move(1)
	s.SetFilter("alpha b")
	if s.SelectedID != "active" {
		t.Fatalf("retained matching identity lost: %+v", s)
	}
	for _, query := range []string{"alpr", "alpha beta response", "al be"} {
		s.SetFilter(query)
		if len(s.Filtered) != 0 || s.SelectedItem() != nil {
			t.Fatalf("query %q used fuzzy/multi-term matching: %+v", query, s.Filtered)
		}
	}
	s.SetFilter("")
	if s.SelectedID != "first" {
		t.Fatalf("empty result reset did not choose first match: %+v", s)
	}
}

func TestModelAndThinkingPickerRowsAreBoundedAndColorSafe(t *testing.T) {
	items := []session.SessionInfo{
		{ID: "provider/model-a", FirstMessage: "credential available; api", Modified: time.Unix(1, 0)},
		{ID: "provider/model-b", FirstMessage: "credential unavailable", Unavailable: "offline"},
		{ID: "provider/model-c", FirstMessage: "credential available"},
	}
	s := NewSelector(items, "provider/model-b")
	s.Models = true
	rows := s.RenderPickerRows(36, true)
	if len(rows) > 7 || !strings.Contains(strings.Join(rows, "\n"), "Models") || !strings.Contains(strings.Join(rows, "\n"), "provider/model-b") {
		t.Fatalf("model picker rows: %q", rows)
	}
	if strings.Contains(strings.Join(rows, "\n"), "\x1b[") {
		t.Fatalf("NO_COLOR picker emitted SGR: %q", rows)
	}
	for _, row := range rows {
		if terminal.CellWidth(row) > 35 {
			t.Fatalf("picker row exceeded width: %q (%d cells)", row, terminal.CellWidth(row))
		}
	}
	s.Move(1)
	if got := s.SelectedItem(); got == nil || got.ID != "provider/model-c" {
		t.Fatalf("model list did not move forward: %v", got)
	}
	s.Move(1)
	if got := s.SelectedItem(); got == nil || got.ID != "provider/model-a" {
		t.Fatalf("model list did not wrap: %v", got)
	}

	thinking := NewSelector([]session.SessionInfo{{ID: "off", FirstMessage: "disabled"}, {ID: "high", FirstMessage: "effective effort high"}}, "off")
	thinking.Thinking = true
	if !strings.Contains(strings.Join(thinking.RenderPickerRows(60, true), "\n"), "Thinking levels") {
		t.Fatal("thinking picker label missing")
	}
}

func TestPickerTailGeometryRetainsSelectionAcrossPlanSizes(t *testing.T) {
	items := make([]session.SessionInfo, 40)
	for i := range items {
		items[i] = session.SessionInfo{ID: "provider/gpt-model-" + strings.Repeat("x", i+1), FirstMessage: "credential available"}
	}
	selector := NewSelector(items, items[20].ID)
	selector.Models = true
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {40, 12}, {20, 6}} {
		width, height := size[0], size[1]
		view := terminal.ComposeConsoleViewComponents(width, height, nil, 0, terminal.PresentationSnapshot{NoColor: true}, "", nil, selector.RenderPickerRows(width, true))
		if len(view.Rows) > height || view.CursorRow < 0 || view.CursorRow >= len(view.Rows) {
			t.Fatalf("geometry %dx%d: rows=%d cursor=%d", width, height, len(view.Rows), view.CursorRow)
		}
		for _, row := range view.Rows {
			if terminal.CellWidth(row) > width {
				t.Fatalf("geometry %dx%d overflowed: %q (%d)", width, height, row, terminal.CellWidth(row))
			}
		}
		if width == 20 && !strings.Contains(strings.Join(view.Rows, "\n"), "gpt-model") {
			t.Fatalf("tiny picker hid the selected model: %q", view.Rows)
		}
	}
}
