package app

import (
	"fmt"
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
	rows := s.RenderPickerRows(36, 19, true)
	if len(rows) > 7 || !strings.Contains(strings.Join(rows, "\n"), "Models") || !strings.Contains(strings.Join(rows, "\n"), "→ ✓ model-b [provider]") {
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
	if !strings.Contains(strings.Join(thinking.RenderPickerRows(60, 19, true), "\n"), "Thinking levels") {
		t.Fatal("thinking picker label missing")
	}
}

func TestModelPickerUsesPiRowsAndSelectedOnlyDetails(t *testing.T) {
	items := make([]session.SessionInfo, 26)
	for i := range items {
		items[i] = session.SessionInfo{ID: fmt.Sprintf("provider/model-%02d", i), FirstMessage: fmt.Sprintf("credential fixture %02d; api, context 8192, output 4096", i)}
	}
	s := NewSelector(items, items[12].ID)
	s.Models = true
	s.Move(1)
	rows := s.RenderPickerRows(120, 35, true)
	text := strings.Join(rows, "\n")
	if s.visibleCount != 10 || len(rows) != 13 {
		t.Fatalf("Pi list should have ten entries plus title, selected detail, help: %q", rows)
	}
	if !strings.Contains(text, "  ✓ model-12 [provider]") || !strings.Contains(text, "→   model-13 [provider]") {
		t.Fatalf("Pi current/selected markers were conflated: %q", rows)
	}
	if strings.Count(text, "credential fixture") != 1 || !strings.Contains(text, items[13].FirstMessage) {
		t.Fatalf("model metadata must appear only for the selected item: %q", rows)
	}
	if strings.Contains(text, "provider/model-") {
		t.Fatalf("model ID should precede a provider badge, not a provider/ID path: %q", rows)
	}
	rows = s.RenderPickerRows(40, 7, true)
	if strings.Contains(strings.Join(rows, "\n"), "credential fixture") || s.visibleCount != 5 {
		t.Fatalf("short layout should drop detail before reducing list rows: %q", rows)
	}
}

func TestSessionPickerUsesNameOrPromptBeforeCompactMetadata(t *testing.T) {
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	s := NewSelector(nil, "")
	for _, item := range []session.SessionInfo{
		{ID: "long-session-identifier", FirstMessage: "Fix the failing test", Modified: now.Add(-90 * time.Minute), MessageCount: 12},
		{ID: "named-session-identifier", Name: "Saved task", FirstMessage: "not the primary label", Modified: now.Add(-2 * 24 * time.Hour)},
	} {
		name, metadata := s.pickerText(item, now)
		want := item.Name
		if want == "" {
			want = item.FirstMessage
		}
		if name != want || !strings.Contains(metadata, shortID(item.ID)) || strings.Contains(metadata, "2026-") {
			t.Fatalf("session primary/secondary text is not Pi-derived: name=%q metadata=%q", name, metadata)
		}
	}
	item := session.SessionInfo{ID: "unavailable-session", FirstMessage: "Cannot open this session", Unavailable: "locked"}
	_, metadata := s.pickerText(item, now)
	if !strings.Contains(metadata, "unavailable: locked") {
		t.Fatalf("unavailable reason disappeared: %q", metadata)
	}
	s = NewSelector([]session.SessionInfo{{ID: "opaque-id", FirstMessage: "Prompt preview", Modified: now, Unavailable: "locked by another psi process"}}, "opaque-id")
	wide := strings.Join(s.RenderPickerRows(80, 19, true), "\n")
	if strings.Contains(wide, "unavailable") || !strings.Contains(wide, "› ✓ Prompt preview") {
		t.Fatalf("own active-session lock must not hide its prompt with an unavailable reason: %q", wide)
	}
	rows := s.RenderPickerRows(20, 2, true)
	if !strings.HasPrefix(rows[0], "› ✓ Prompt preview") || strings.Contains(strings.Join(rows, "\n"), "opaque-id") {
		t.Fatalf("narrow session row must prioritize prompt, not identity metadata: %q", rows)
	}
}

func TestPickerLabelsAndSelectedDetailsRemainSanitized(t *testing.T) {
	for _, models := range []bool{false, true} {
		s := NewSelector([]session.SessionInfo{{
			ID: "provider/model", Name: "name\x1b[2J", FirstMessage: "safe\x1b]52;c;SECRET\a\nsecond line",
			Unavailable: "offline\x1b[31m", Modified: time.Now(),
		}}, "provider/model")
		s.Models = models
		for _, noColor := range []bool{false, true} {
			for _, row := range s.RenderPickerRows(80, 19, noColor) {
				plain := terminal.Sanitize(row)
				if strings.ContainsAny(plain, "\r\n") || terminal.CellWidth(plain) > 79 || strings.Contains(row, "SECRET") || strings.Contains(row, "\x1b[2J") || strings.Contains(row, "\x1b]52;") {
					t.Fatalf("unsafe picker row models=%v: %q", models, row)
				}
				if noColor && strings.Contains(row, "\x1b[") {
					t.Fatalf("NO_COLOR emitted SGR: %q", row)
				}
			}
		}
	}
}

func TestSessionAgeUsesPiThresholds(t *testing.T) {
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		age  time.Duration
		want string
	}{
		{-time.Hour, "now"}, {59 * time.Second, "now"}, {time.Minute, "1m"},
		{59 * time.Minute, "59m"}, {time.Hour, "1h"}, {23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"}, {6 * 24 * time.Hour, "6d"}, {7 * 24 * time.Hour, "1w"},
		{29 * 24 * time.Hour, "4w"}, {30 * 24 * time.Hour, "1mo"}, {364 * 24 * time.Hour, "12mo"},
		{365 * 24 * time.Hour, "1y"},
	} {
		if got := formatSessionAge(now.Add(-test.age), now); got != test.want {
			t.Errorf("age %s: got %q, want %q", test.age, got, test.want)
		}
	}
	if got := formatSessionAge(time.Time{}, now); got != "?" {
		t.Fatalf("unknown modification time became a fabricated age: %q", got)
	}
}

func TestPickerPagesByActualVisibleCount(t *testing.T) {
	items := make([]session.SessionInfo, 40)
	for i := range items {
		items[i].ID = fmt.Sprintf("provider/model-%02d", i)
	}
	for _, size := range [][3]int{{120, 40, 10}, {80, 24, 10}, {60, 16, 9}, {40, 12, 5}, {20, 6, 2}} {
		s := NewSelector(items, items[17].ID)
		s.Models = true
		width, height, visible := size[0], size[1], size[2]
		rows := s.RenderPickerRows(width, terminal.EditorComponentRowBudget(height), true)
		if s.visibleCount != visible || !strings.Contains(strings.Join(rows, "\n"), "→ ✓ model-17") {
			t.Fatalf("%dx%d lost selection or visible range: count=%d rows=%q", width, height, s.visibleCount, rows)
		}
		s.MovePage(1)
		if s.Selected != 17+visible {
			t.Fatalf("%dx%d PageDown moved to %d, want %d", width, height, s.Selected, 17+visible)
		}
		s.MovePage(-1)
		if s.Selected != 17 {
			t.Fatalf("%dx%d PageUp did not return to selection: %d", width, height, s.Selected)
		}
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
		view := terminal.ComposeConsoleViewPicker(width, height, nil, 0, terminal.PresentationSnapshot{NoColor: true}, "", nil, selector.RenderPickerRows(width, terminal.EditorComponentRowBudget(height), true), terminal.PickerSearch)
		if len(view.Rows) > height || view.CursorRow < 0 || view.CursorRow >= len(view.Rows) {
			t.Fatalf("geometry %dx%d: rows=%d cursor=%d", width, height, len(view.Rows), view.CursorRow)
		}
		for _, row := range view.Rows {
			if terminal.CellWidth(row) > width {
				t.Fatalf("geometry %dx%d overflowed: %q (%d)", width, height, row, terminal.CellWidth(row))
			}
		}
		if !strings.Contains(strings.Join(view.Rows, "\n"), "→ ✓ gpt-model") {
			t.Fatalf("picker hid the actual selected row at %dx%d: %q", width, height, view.Rows)
		}
	}
}
