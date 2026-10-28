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
