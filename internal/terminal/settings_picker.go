package terminal

import (
	"fmt"
	"strings"

	pitheme "psi/internal/theme"
)

// SettingsValue is one selectable value for a settings row.
type SettingsValue struct {
	Label string
	Value any
}

// SettingsItem is one row in the inline settings list.
type SettingsItem struct {
	ID          string
	Label       string
	Description string
	Values      []SettingsValue
	Selected    int
}

// Current returns the selected value and whether the row has one.
func (i SettingsItem) Current() (SettingsValue, bool) {
	if i.Selected < 0 || i.Selected >= len(i.Values) {
		return SettingsValue{}, false
	}
	return i.Values[i.Selected], true
}

// SettingsPicker is a bounded inline list. It shares the picker region with
// the selector and never touches configuration or the filesystem.
type SettingsPicker struct {
	Items        []SettingsItem
	Index        int
	visibleCount int
}

// Move moves the selection and wraps at either end.
func (p *SettingsPicker) Move(delta int) {
	if p == nil || delta == 0 {
		return
	}
	n := len(p.Items)
	if n == 0 {
		return
	}
	p.Index = (p.Index + delta%n + n) % n
}

// Page moves by one visible window, clamped to the list.
func (p *SettingsPicker) Page(delta int) {
	if p == nil || delta == 0 {
		return
	}
	n := len(p.Items)
	if n == 0 {
		return
	}
	step := max(1, p.visibleCount)
	p.Index = min(max(p.Index+delta*step, 0), n-1)
}

// Cycle selects the next value of the current row and wraps.
func (p *SettingsPicker) Cycle() (SettingsItem, SettingsValue, bool) {
	if p == nil || p.Index < 0 || p.Index >= len(p.Items) {
		return SettingsItem{}, SettingsValue{}, false
	}
	item := &p.Items[p.Index]
	if len(item.Values) == 0 {
		return SettingsItem{}, SettingsValue{}, false
	}
	item.Selected = (item.Selected + 1) % len(item.Values)
	return *item, item.Values[item.Selected], true
}

// SetIndex selects a row by index, clamped to the list.
func (p *SettingsPicker) SetIndex(index int) {
	if p == nil || len(p.Items) == 0 {
		return
	}
	p.Index = min(max(index, 0), len(p.Items)-1)
}

// SelectedItem returns the row under the cursor.
func (p *SettingsPicker) SelectedItem() (SettingsItem, bool) {
	if p == nil || p.Index < 0 || p.Index >= len(p.Items) {
		return SettingsItem{}, false
	}
	return p.Items[p.Index], true
}

// Rows renders the list into at most rowBudget physical rows. The selected
// row and its keys stay available at short heights; the description goes
// first, then the hint.
func (p *SettingsPicker) Rows(width, rowBudget int, noColor bool, palette ...*pitheme.Theme) []string {
	if p == nil || rowBudget <= 0 || len(p.Items) == 0 {
		return nil
	}
	p.visibleCount = 0
	var selectedTheme *pitheme.Theme
	if len(palette) > 0 {
		selectedTheme = palette[0]
	}
	width = max(1, width)
	rows := make([]string, 0, rowBudget)
	appendRow := func(text string, token ThemeToken) {
		rows = append(rows, FormatStyledRow(StyledRow{Spans: []StyledSpan{{Text: text, Token: token}}}, width, noColor, selectedTheme))
	}

	help := rowBudget >= 3
	helpRows := 0
	if help {
		helpRows = 1
	}
	item, ok := p.SelectedItem()
	descText := ""
	if ok {
		descText = SanitizeStatusRow(item.Description)
	}
	desc := descText != "" && rowBudget >= 4
	descRows := 0
	if desc {
		descRows = 1
	}

	listBudget := max(1, rowBudget-helpRows-descRows)
	count := min(10, min(len(p.Items), listBudget))
	indicator := false
	if count < len(p.Items) && listBudget >= 2 {
		count--
		indicator = true
	}
	p.visibleCount = count
	start := max(0, min(p.Index-count/2, len(p.Items)-count))
	end := min(len(p.Items), start+count)

	labelWidth := 0
	for _, entry := range p.Items {
		labelWidth = max(labelWidth, CellWidth(SanitizeStatusRow(entry.Label)))
	}
	labelWidth = min(labelWidth, max(10, min(40, width-12)))

	for i := start; i < end; i++ {
		entry := p.Items[i]
		selected := i == p.Index
		marker, markerToken := "  ", TokenMuted
		labelToken, valueToken := TokenText, TokenMuted
		if selected {
			marker, markerToken = "→ ", TokenAccent
			labelToken, valueToken = TokenAccent, TokenText
		}
		label := SanitizeStatusRow(entry.Label)
		label = TruncateToWidth(label, labelWidth)
		labelText := label + strings.Repeat(" ", max(0, labelWidth-CellWidth(label)))
		valueText := ""
		if current, has := entry.Current(); has {
			valueText = SanitizeStatusRow(current.Label)
			valueText = TruncateToWidth(valueText, max(1, width-3-labelWidth))
		}
		row := StyledRow{Spans: []StyledSpan{
			{Text: marker, Token: markerToken},
			{Text: labelText, Token: labelToken},
			{Text: "  ", Token: TokenMuted},
			{Text: valueText, Token: valueToken},
		}}
		if selected {
			row.Surface = TokenSelectedBg
		}
		rows = append(rows, FormatStyledRow(row, width, noColor, selectedTheme))
	}
	if indicator {
		appendRow(fmt.Sprintf("  (%d/%d)", p.Index+1, len(p.Items)), TokenMuted)
	}
	if desc {
		appendRow("  "+TruncateToWidth(descText, max(1, width-3)), TokenMuted)
	}
	if help {
		hint := FitHint(width, "Enter/Space change · Esc close", "Enter change · Esc")
		appendRow(hint, TokenMuted)
	}
	return rows
}
