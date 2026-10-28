package terminal

// ChoiceOption is one bounded option in an editor-tail choice picker.
type ChoiceOption struct {
	Label       string
	Description string
	Current     bool
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

func (p *ChoicePicker) Rows(width int, noColor bool) []string {
	if p == nil {
		return nil
	}
	selected, hasSelected := p.SelectedOption()
	header := StyledRow{Spans: []StyledSpan{{Text: p.Title, Token: TokenMuted}}}
	if hasSelected {
		header.Spans = append(header.Spans, StyledSpan{Text: " · selected ", Token: TokenMuted}, StyledSpan{Text: selected.Label, Token: TokenAccent})
	}
	rows := []string{FormatStyledRow(header, width, noColor)}
	// Put alternatives before optional context and help so a short terminal
	// still shows the current decision and at least one other choice.
	for i, option := range p.Options {
		if i == p.Selected {
			continue
		}
		spans := []StyledSpan{{Text: "  ", Token: TokenMuted}}
		if option.Current {
			spans = append(spans, StyledSpan{Text: "✓ ", Token: TokenSuccess})
		} else {
			spans = append(spans, StyledSpan{Text: "  ", Token: TokenMuted})
		}
		spans = append(spans, StyledSpan{Text: option.Label, Token: TokenText})
		if option.Description != "" {
			spans = append(spans, StyledSpan{Text: "  " + option.Description, Token: TokenMuted})
		}
		rows = append(rows, FormatStyledRow(StyledRow{Spans: spans}, width, noColor))
	}
	if p.Context != "" {
		rows = append(rows, FormatStyledRow(StyledRow{Spans: []StyledSpan{{Text: p.Context, Token: TokenMuted}}}, width, noColor))
	}
	rows = append(rows, FormatStyledRow(StyledRow{Spans: []StyledSpan{{Text: "↑/↓ select · Enter confirm · Esc/Ctrl+C cancel", Token: TokenMuted}}}, width, noColor))
	return rows
}
