package terminal

import pitheme "psi/internal/theme"

// PickerMode selects how the picker region is composed.
type PickerMode uint8

const (
	// PickerSearch renders a search query row above the list, like Pi's
	// selector search input.
	PickerSearch PickerMode = iota
	// PickerChoice renders only the supplied rows, like Pi's startup selector.
	PickerChoice
)

// ChoiceOption is one bounded option in an editor-tail choice picker.
type ChoiceOption struct {
	Label       string
	Description string
	Current     bool
}

// FitHint returns the first candidate that fits one physical row at the given
// width. A narrow terminal gets a shorter hint instead of a clipped word.
func FitHint(width int, candidates ...string) string {
	limit := max(1, width-1)
	for _, text := range candidates {
		if CellWidth(text) <= limit {
			return text
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[len(candidates)-1]
}

// ChoicePicker is used for startup trust and confirmation decisions. It shares
// psi's bordered editor tail and keyboard precedence without opening an overlay.
type ChoicePicker struct {
	Title    string
	Context  string
	Options  []ChoiceOption
	Selected int
}

func (p *ChoicePicker) Move(delta int) {
	if p == nil || len(p.Options) == 0 || delta == 0 {
		return
	}
	p.Selected = min(max(p.Selected+delta, 0), len(p.Options)-1)
}

func (p *ChoicePicker) SelectedOption() (ChoiceOption, bool) {
	if p == nil || p.Selected < 0 || p.Selected >= len(p.Options) {
		return ChoiceOption{}, false
	}
	return p.Options[p.Selected], true
}

func (p *ChoicePicker) Rows(width int, noColor bool, palette ...*pitheme.Theme) []string {
	var selectedTheme *pitheme.Theme
	if len(palette) > 0 {
		selectedTheme = palette[0]
	}
	if p == nil {
		return nil
	}
	var rows []string
	appendRow := func(spans []StyledSpan) {
		rows = append(rows, FormatStyledRow(StyledRow{Spans: spans}, width, noColor, selectedTheme))
	}
	// Pi's trust selector shows a title, the visible folder, and the options
	// with a selection pointer and a current marker. psi keeps its reduced
	// allow/deny set and its explicit cancel action.
	appendRow([]StyledSpan{{Text: p.Title, Token: TokenAccent, Bold: true}})
	for i, option := range p.Options {
		prefix, prefixToken, labelToken := "  ", TokenMuted, TokenText
		if i == p.Selected {
			prefix, prefixToken, labelToken = "→ ", TokenAccent, TokenAccent
		}
		spans := []StyledSpan{{Text: prefix, Token: prefixToken}}
		if option.Current {
			spans = append(spans, StyledSpan{Text: "✓ ", Token: TokenSuccess})
		} else {
			spans = append(spans, StyledSpan{Text: "  ", Token: TokenMuted})
		}
		spans = append(spans, StyledSpan{Text: option.Label, Token: labelToken})
		if option.Description != "" && width >= 48 {
			spans = append(spans, StyledSpan{Text: "  " + option.Description, Token: TokenMuted})
		}
		row := StyledRow{Spans: spans}
		if i == p.Selected {
			row.Surface = TokenSelectedBg
		}
		rows = append(rows, FormatStyledRow(row, width, noColor, selectedTheme))
	}
	if p.Context != "" {
		appendRow([]StyledSpan{{Text: p.Context, Token: TokenMuted}})
	}
	appendRow([]StyledSpan{{Text: FitHint(width, "↑↓ navigate · Enter save · Esc cancel", "↑↓ · Enter · Esc", "Enter Esc"), Token: TokenMuted}})
	return rows
}
