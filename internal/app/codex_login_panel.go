package app

import (
	"github.com/hypernewbie/psi/internal/terminal"
	"github.com/hypernewbie/psi/internal/theme"
)

// codexLoginPanel is REPL-owned. Its contents are only rendered in the editor
// region, never appended to the document or forwarded to the bridge.
type codexLoginPanel struct {
	// heading overrides the panel title. Empty keeps the Codex title.
	heading            string
	link, code         string
	temporaryPort      bool
	manualOpen         bool
	openDevice         func()
	status, inputError string
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
	deviceCodeRows := wrap("Enter code: " + p.code)
	// Preserve room for instructions before adding the heading.
	instructionRows := 1
	if p.code != "" {
		instructionRows = len(deviceCodeRows) + len(linkRows)
	}
	heading := "Login to OpenAI Codex"
	if p.heading != "" {
		heading = p.heading
	}
	headingRows := wrap(heading)
	if !p.temporaryPort && !p.manualOpen && len(headingRows)+instructionRows <= budget {
		appendRows(headingRows, terminal.TokenAccent, true)
	}
	switch {
	case p.status != "":
		add(p.status, terminal.TokenMuted, false)
	case p.code != "":
		appendRows(deviceCodeRows, terminal.TokenWarning, false)
		if len(linkRows) <= budget-len(rows) {
			appendRows(linkRows, terminal.TokenAccent, false)
			if p.manualOpen {
				add("The browser did not open. Open the link above manually.", terminal.TokenWarning, false)
			}
		} else {
			add("Enlarge the terminal for the verification link.", terminal.TokenWarning, false)
		}
		add("Waiting for authentication…", terminal.TokenMuted, false)
		if p.openDevice != nil {
			add("Enter open browser · Esc cancel · Ctrl+D quit", terminal.TokenMuted, false)
		} else {
			add("Esc cancel · Ctrl+D quit", terminal.TokenMuted, false)
		}
	case p.link != "":
		if p.temporaryPort {
			add("Temporary port. If OpenAI rejects it, close the tab, cancel, and choose device code.", terminal.TokenWarning, false)
		}
		add(p.inputError, terminal.TokenError, false)
		if p.manualOpen {
			// A partial authorization URL cannot be opened. Give the link
			// priority over optional instructions when auto-launch failed.
			instruction := wrap("The browser did not open. Open the link below manually.")
			if len(instruction)+len(linkRows) <= budget-len(rows) {
				appendRows(instruction, terminal.TokenWarning, false)
				appendRows(linkRows, terminal.TokenAccent, false)
			} else {
				add("Enlarge the terminal for the sign-in link.", terminal.TokenWarning, false)
			}
		} else {
			add("Complete OpenAI Codex sign-in in your browser.", terminal.TokenText, false)
			if len(linkRows)+1 <= budget-len(rows) {
				appendRows(linkRows, terminal.TokenAccent, false)
			}
		}
		add("Or paste redirect URL or code below · Enter submit · Esc cancel", terminal.TokenMuted, false)
	default:
		add("Starting sign-in… · Esc cancel · Ctrl+D quit", terminal.TokenMuted, false)
	}
	return rows
}
