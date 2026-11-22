package app

import (
	"github.com/hypernewbie/psi/internal/i18n"
	"github.com/hypernewbie/psi/internal/terminal"
	"github.com/hypernewbie/psi/internal/theme"
)

// codexLoginPanel is REPL-owned. Its contents are only rendered in the editor
// region, never appended to the document or forwarded to the bridge.
type codexLoginPanel struct {
	link, code         string
	temporaryPort      bool
	manualOpen         bool
	openDevice         func()
	status, inputError string
	// chinese selects the panel text language. It is fixed when the panel is
	// created so a language change cannot mix two languages in one attempt.
	chinese bool
}

func (p *codexLoginPanel) text(key i18n.Key, args ...any) string {
	return loginText(p.chinese, key, args...)
}

func (p *codexLoginPanel) rows(width, budget int, noColor bool, palette *theme.Theme) []string {
	budget = max(1, budget)
	var rows []string
	wrap := func(text string) []string {
		if text == "" {
			return nil
		}
		return terminal.LayoutDraft([]rune(terminal.SanitizeStatusRow(text)), 0, max(1, width-1)).Rows
	}
	appendRows := func(lines []string, token terminal.ThemeToken, bold bool) {
		for _, line := range lines[:min(len(lines), budget-len(rows))] {
			rows = append(rows, terminal.FormatStyledRow(terminal.StyledRow{Spans: []terminal.StyledSpan{{Text: " " + line, Token: token, Bold: bold}}}, width, noColor, palette))
		}
	}
	add := func(text string, token terminal.ThemeToken, bold bool) {
		appendRows(wrap(text), token, bold)
	}
	linkRows := wrap(p.link)
	deviceCodeRows := wrap(p.text("panel.enter-code", p.code))
	// Preserve room for instructions before adding the heading.
	instructionRows := 1
	if p.code != "" {
		instructionRows = len(deviceCodeRows) + len(linkRows)
	}
	heading := wrap(p.text("panel.heading"))
	if !p.temporaryPort && !p.manualOpen && len(heading)+instructionRows <= budget {
		appendRows(heading, terminal.TokenAccent, true)
	}
	switch {
	case p.status != "":
		add(p.status, terminal.TokenMuted, false)
	case p.code != "":
		appendRows(deviceCodeRows, terminal.TokenWarning, false)
		if len(linkRows) <= budget-len(rows) {
			appendRows(linkRows, terminal.TokenAccent, false)
			if p.manualOpen {
				add(p.text("panel.browser-manual-above"), terminal.TokenWarning, false)
			}
		} else {
			add(p.text("panel.enlarge-verification"), terminal.TokenWarning, false)
		}
		add(p.text("panel.waiting"), terminal.TokenMuted, false)
		if p.openDevice != nil {
			add(p.text("panel.keys-open"), terminal.TokenMuted, false)
		} else {
			add(p.text("panel.keys"), terminal.TokenMuted, false)
		}
	case p.link != "":
		if p.temporaryPort {
			add(p.text("panel.temporary-port"), terminal.TokenWarning, false)
		}
		add(p.inputError, terminal.TokenError, false)
		if p.manualOpen {
			// A partial authorization URL cannot be opened. Give the link
			// priority over optional instructions when auto-launch failed.
			instruction := wrap(p.text("panel.browser-manual-below"))
			if len(instruction)+len(linkRows) <= budget-len(rows) {
				appendRows(instruction, terminal.TokenWarning, false)
				appendRows(linkRows, terminal.TokenAccent, false)
			} else {
				add(p.text("panel.enlarge-signin"), terminal.TokenWarning, false)
			}
		} else {
			add(p.text("panel.complete"), terminal.TokenText, false)
			if len(linkRows)+1 <= budget-len(rows) {
				appendRows(linkRows, terminal.TokenAccent, false)
			}
		}
		add(p.text("panel.paste-hint"), terminal.TokenMuted, false)
	default:
		add(p.text("panel.starting"), terminal.TokenMuted, false)
	}
	return rows
}
