package app

import (
	"fmt"
	"strings"
	"time"

	"psi/internal/session"
	"psi/internal/terminal"
)

// Selector owns a stable, searchable collection. The regular-mode picker uses
// keyboard selection in the picker region. The page fields remain only for
// bounded legacy metadata discovery, not as an alternate selection UI.
type Selector struct {
	Items    []session.SessionInfo
	Filtered []session.SessionInfo
	Page     int
	Filter   string
	ActiveID string
	PageSize int
	Models   bool
	Thinking bool

	Selected     int
	SelectedID   string
	visibleCount int
}

func NewSelector(items []session.SessionInfo, activeID string) *Selector {
	s := &Selector{Items: items, ActiveID: activeID, SelectedID: activeID, PageSize: 20, visibleCount: 10}
	s.apply()
	return s
}

func (s *Selector) apply() {
	s.Filtered = s.Filtered[:0]
	for _, it := range s.Items {
		if selectorMatches(s.Filter, it) {
			s.Filtered = append(s.Filtered, it)
		}
	}
	if len(s.Filtered) == 0 {
		s.Selected, s.Page = 0, 0
		s.SelectedID = ""
		return
	}
	selected := -1
	for i, it := range s.Filtered {
		if it.ID == s.SelectedID {
			selected = i
			break
		}
	}
	if selected < 0 {
		selected = 0
	}
	s.Selected = selected
	s.SelectedID = s.Filtered[selected].ID
	if s.PageSize < 1 {
		s.PageSize = 20
	}
}

func selectorMatches(query string, item session.SessionInfo) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return true
	}
	searchable := strings.Join([]string{item.ID, item.Name, item.Cwd, item.FirstMessage, item.Unavailable}, " ")
	// PLAN4 deliberately retains psi's substring search and source ordering,
	// rather than porting Pi's fuzzy matching/ranking.
	return strings.Contains(strings.ToLower(searchable), strings.ToLower(query))
}

func (s *Selector) SetFilter(q string) {
	previous := s.SelectedID
	s.Filter = strings.TrimSpace(q)
	s.SelectedID = previous
	s.Selected, s.Page = 0, 0
	s.apply()
}

// Resolve now treats input as a search query; numeric row selection belongs to
// the picker key handler, not the input language.
func (s *Selector) Resolve(line string) (selected *session.SessionInfo, cancel bool, redraw bool) {
	if strings.TrimSpace(line) == "" {
		return nil, true, false
	}
	s.SetFilter(line)
	return nil, false, true
}

func (s *Selector) Move(delta int) {
	if len(s.Filtered) == 0 || delta == 0 {
		return
	}
	next := s.Selected + delta
	if s.Models || s.Thinking {
		next = (next%len(s.Filtered) + len(s.Filtered)) % len(s.Filtered)
	} else {
		next = min(max(next, 0), len(s.Filtered)-1)
	}
	s.Selected = next
	s.SelectedID = s.Filtered[next].ID
	s.Page = next / max(1, s.PageSize)
}

func (s *Selector) MovePage(delta int) {
	if len(s.Filtered) == 0 || delta == 0 {
		return
	}
	s.Move(delta * max(1, s.visibleCount))
}

func (s *Selector) SelectedItem() *session.SessionInfo {
	if s.Selected < 0 || s.Selected >= len(s.Filtered) {
		return nil
	}
	item := s.Filtered[s.Selected]
	return &item
}

func (s *Selector) NextPage() {
	s.Page++
	if s.Page*s.PageSize >= len(s.Filtered) && len(s.Filtered) > 0 {
		s.Page = (len(s.Filtered) - 1) / s.PageSize
	}
}
func (s *Selector) PrevPage() {
	if s.Page > 0 {
		s.Page--
	}
}

// PageItems returns the current bounded compatibility page.
func (s *Selector) PageItems() []session.SessionInfo {
	start := s.Page * s.PageSize
	if start >= len(s.Filtered) {
		return nil
	}
	end := min(start+s.PageSize, len(s.Filtered))
	return s.Filtered[start:end]
}

// RenderPickerRows returns a bounded, searchable picker for the inline editor.
// It never opens a transcript or reads session contents beyond supplied metadata.
func (s *Selector) RenderPickerRows(width, rowBudget int, noColor bool) []string {
	s.visibleCount = 0
	if rowBudget <= 0 {
		return nil
	}
	label := "Sessions"
	// Pi's no-match treatment is component-specific: the model selector says
	// "No matching models", the session list says "No sessions found", and the
	// thinking selector inherits the shared SelectList wording.
	noMatch := "  No sessions found"
	if s.Models {
		label, noMatch = "Models", "  No matching models"
	} else if s.Thinking {
		label, noMatch = "Thinking levels", "  No matching commands"
	}
	var rows []string
	appendRow := func(text string, token terminal.ThemeToken) {
		rows = append(rows, terminal.FormatStyledRow(terminal.StyledRow{Spans: []terminal.StyledSpan{{Text: text, Token: token}}}, width, noColor))
	}
	// At short heights retain real selectable rows, not a duplicate selected
	// label in a header. Optional metadata disappears before list entries.
	if rowBudget >= 3 {
		appendRow(fmt.Sprintf("%s · %d matches", label, len(s.Filtered)), terminal.TokenMuted)
	}
	if len(s.Filtered) == 0 {
		appendRow(noMatch, terminal.TokenMuted)
		if rowBudget-len(rows) > 0 {
			appendRow("Type to search · Esc/Ctrl+C cancel", terminal.TokenMuted)
		}
		return rows
	}
	help := rowBudget >= 4
	detail := s.Models && width >= 60 && rowBudget >= min(10, len(s.Filtered))+3
	helpRows, detailRows := 0, 0
	if help {
		helpRows = 1
	}
	if detail {
		detailRows = 1
	}
	count := min(10, max(1, rowBudget-len(rows)-helpRows-detailRows))
	count = min(count, len(s.Filtered))
	s.visibleCount = count
	// Pi centers the selection in its bounded list. Use the actual visible
	// count, so a short terminal never clips away the selected row.
	start := max(0, min(s.Selected-count/2, len(s.Filtered)-count))
	end := min(len(s.Filtered), start+count)
	scrolled := start > 0 || end < len(s.Filtered)
	if scrolled && len(rows)+count+1+helpRows+detailRows > rowBudget {
		// Pi's scroll indicator needs a row. The optional selected detail goes
		// first; the list entries stay.
		if detailRows > 0 {
			detail, detailRows = false, 0
		}
	}
	if scrolled && len(rows)+count+1+helpRows+detailRows > rowBudget {
		scrolled = false
	}
	now := time.Now()
	for i := start; i < end; i++ {
		item := s.Filtered[i]
		selected := i == s.Selected
		marker, markerToken := "  ", terminal.TokenMuted
		if selected {
			marker, markerToken = "› ", terminal.TokenAccent
			if s.Models || s.Thinking {
				// Pi's model selector and its shared SelectList use →; only the
				// session list uses ›.
				marker = "→ "
			}
		}
		spans := []terminal.StyledSpan{{Text: marker, Token: markerToken}}
		if item.ID == s.ActiveID {
			spans = append(spans, terminal.StyledSpan{Text: "✓ ", Token: terminal.TokenAccent})
		} else {
			spans = append(spans, terminal.StyledSpan{Text: "  ", Token: terminal.TokenMuted})
		}
		name, metadata := s.pickerText(item, now)
		name = terminal.SanitizeStatusRow(name)
		metadata = terminal.SanitizeStatusRow(metadata)
		nameToken := terminal.TokenText
		if item.Unavailable != "" && item.ID != s.ActiveID {
			nameToken = terminal.TokenWarning
		}
		if selected {
			nameToken = terminal.TokenAccent
		}
		if width < 48 && !s.Models {
			metadata = ""
		}
		if metadata != "" && !s.Models && !s.Thinking {
			// Like Pi's session list, keep compact age/identity at the right
			// rather than letting a long prompt push it out of view.
			metadata = terminal.TruncateToWidth(metadata, max(0, (width-7)/2))
			name = terminal.TruncateToWidth(name, max(1, width-7-terminal.CellWidth(metadata)))
		}
		spans = append(spans, terminal.StyledSpan{Text: name, Token: nameToken})
		if metadata != "" {
			gap := 2
			if s.Models {
				gap = 1
			} else if !s.Thinking {
				gap = max(2, width-5-terminal.CellWidth(name)-terminal.CellWidth(metadata))
			}
			spans = append(spans, terminal.StyledSpan{Text: strings.Repeat(" ", gap) + metadata, Token: terminal.TokenMuted})
		}
		rows = append(rows, terminal.FormatStyledRow(terminal.StyledRow{Spans: spans}, width, noColor))
	}
	if scrolled {
		// Pi shows the position inside the filtered list when the bounded
		// window cannot show every item.
		appendRow(fmt.Sprintf("  (%d/%d)", s.Selected+1, len(s.Filtered)), terminal.TokenMuted)
	}
	if detail {
		appendRow("  "+s.Filtered[s.Selected].FirstMessage, terminal.TokenMuted)
	}
	if help {
		appendRow(pickerHelpText(width), terminal.TokenMuted)
	}
	return rows
}

// pickerHelpText keeps the key hint on one row. A narrow terminal gets a
// shorter hint instead of a clipped word.
func pickerHelpText(width int) string {
	return terminal.FitHint(width,
		"↑/↓ select · PageUp/PageDown move · Enter apply · Esc cancel",
		"↑↓ select · PgUp/PgDn · Enter · Esc",
		"↑↓ select · Enter apply · Esc",
		"↑↓ · Enter · Esc",
		"Enter Esc",
	)
}

func (s *Selector) pickerText(item session.SessionInfo, now time.Time) (string, string) {
	if s.Thinking {
		return item.ID, item.FirstMessage
	}
	if s.Models {
		provider, model, found := strings.Cut(item.ID, "/")
		if found {
			return model, "[" + provider + "]"
		}
		return item.ID, ""
	}
	name := strings.TrimSpace(terminal.SanitizeStatusRow(item.Name))
	if name == "" {
		name = strings.TrimSpace(terminal.SanitizeStatusRow(item.FirstMessage))
	}
	if name == "" {
		name = shortID(item.ID)
	}
	metadata := fmt.Sprintf("%d %s · %s", item.MessageCount, formatSessionAge(item.Modified, now), shortID(item.ID))
	if item.Unavailable != "" && item.ID != s.ActiveID {
		metadata = "unavailable: " + item.Unavailable + " · " + shortID(item.ID)
	}
	return name, metadata
}

// formatSessionAge follows Pi's session-selector.ts formatSessionDate. Unknown
// timestamps stay unknown instead of becoming a fabricated age.
func formatSessionAge(modified, now time.Time) string {
	if modified.IsZero() {
		return "?"
	}
	age := now.Sub(modified)
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return fmt.Sprintf("%dm", age/time.Minute)
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", age/time.Hour)
	case age < 7*24*time.Hour:
		return fmt.Sprintf("%dd", age/(24*time.Hour))
	case age < 30*24*time.Hour:
		return fmt.Sprintf("%dw", age/(7*24*time.Hour))
	case age < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", age/(30*24*time.Hour))
	default:
		return fmt.Sprintf("%dy", age/(365*24*time.Hour))
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
