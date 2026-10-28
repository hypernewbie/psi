package app

import (
	"fmt"
	"strings"

	"psi/internal/session"
	"psi/internal/terminal"
)

// Selector owns a stable, searchable collection. The regular-mode picker uses
// keyboard selection in the editor tail; Page/Render remain for bounded legacy
// text summaries, not as an alternate selection UI.
type Selector struct {
	Items    []session.SessionInfo
	Filtered []session.SessionInfo
	Page     int
	Filter   string
	ActiveID string
	PageSize int
	Models   bool
	Thinking bool

	Selected   int
	SelectedID string
}

func NewSelector(items []session.SessionInfo, activeID string) *Selector {
	s := &Selector{Items: items, ActiveID: activeID, SelectedID: activeID, PageSize: 20}
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
		for i, it := range s.Filtered {
			if it.ID == s.ActiveID {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		selected = min(max(s.Selected, 0), len(s.Filtered)-1)
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
	lowerQuery, lowerSearch := strings.ToLower(query), strings.ToLower(searchable)
	if strings.Contains(lowerSearch, lowerQuery) {
		return true
	}
	// Keep punctuation-heavy IDs exact. Fuzzy matching a model name against its
	// credential description can otherwise produce unrelated matches.
	if strings.ContainsAny(query, "-./_:") {
		return false
	}
	for _, term := range strings.Fields(query) {
		if !fuzzySubsequence(term, lowerSearch) {
			return false
		}
	}
	return true
}

// fuzzySubsequence follows Pi's forgiving search behavior while preserving the
// source ordering: every query rune must occur in order in the item text.
func fuzzySubsequence(query, text string) bool {
	q := []rune(strings.ToLower(query))
	if len(q) == 0 {
		return true
	}
	candidate := []rune(strings.ToLower(text))
	at := 0
	for _, want := range q {
		for at < len(candidate) && candidate[at] != want {
			at++
		}
		if at == len(candidate) {
			return false
		}
		at++
	}
	return true
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
	s.Move(delta * 5)
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
func (s *Selector) RenderPickerRows(width int, noColor bool) []string {
	label := "Sessions"
	if s.Models {
		label = "Models"
	} else if s.Thinking {
		label = "Thinking levels"
	}
	header := terminal.StyledRow{Spans: []terminal.StyledSpan{{Text: fmt.Sprintf("%s · %d matches", label, len(s.Filtered)), Token: terminal.TokenMuted}}}
	if selected := s.SelectedItem(); selected != nil {
		name, _ := s.pickerText(*selected)
		header.Spans = append(header.Spans, terminal.StyledSpan{Text: " · selected ", Token: terminal.TokenMuted}, terminal.StyledSpan{Text: name, Token: terminal.TokenAccent})
	}
	rows := []string{terminal.FormatStyledRow(header, width, noColor)}
	if len(s.Filtered) == 0 {
		rows = append(rows, terminal.FormatStyledRow(terminal.StyledRow{Spans: []terminal.StyledSpan{{Text: "No matches", Token: terminal.TokenWarning}}}, width, noColor))
		rows = append(rows, terminal.FormatStyledRow(terminal.StyledRow{Spans: []terminal.StyledSpan{{Text: "Type to search · Esc/Ctrl+C cancel", Token: terminal.TokenMuted}}}, width, noColor))
		return rows
	}
	start := max(0, min(s.Selected-1, len(s.Filtered)-5))
	end := min(len(s.Filtered), start+5)
	for i := start; i < end; i++ {
		item := s.Filtered[i]
		selected := i == s.Selected
		marker, markerToken := "  ", terminal.TokenMuted
		if selected {
			marker, markerToken = "› ", terminal.TokenAccent
		}
		spans := []terminal.StyledSpan{{Text: marker, Token: markerToken}}
		if item.ID == s.ActiveID {
			spans = append(spans, terminal.StyledSpan{Text: "* ", Token: terminal.TokenSuccess})
		} else {
			spans = append(spans, terminal.StyledSpan{Text: "  ", Token: terminal.TokenMuted})
		}
		name, detail := s.pickerText(item)
		nameToken := terminal.TokenText
		if item.Unavailable != "" {
			nameToken = terminal.TokenWarning
		}
		spans = append(spans, terminal.StyledSpan{Text: name, Token: nameToken})
		if detail != "" {
			spans = append(spans, terminal.StyledSpan{Text: "  " + detail, Token: terminal.TokenMuted})
		}
		rows = append(rows, terminal.FormatStyledRow(terminal.StyledRow{Spans: spans}, width, noColor))
	}
	rows = append(rows, terminal.FormatStyledRow(terminal.StyledRow{Spans: []terminal.StyledSpan{{Text: "↑/↓ select · PageUp/PageDown move · Enter apply · Esc cancel", Token: terminal.TokenMuted}}}, width, noColor))
	return rows
}

func (s *Selector) pickerText(item session.SessionInfo) (string, string) {
	if s.Thinking {
		return item.ID, item.FirstMessage
	}
	if s.Models {
		return item.ID, item.FirstMessage
	}
	name := item.ID
	if item.Name != "" {
		name = firstLine(item.Name) + " · " + shortID(item.ID)
	} else {
		name = shortID(item.ID)
	}
	detail := item.Modified.Format("2006-01-02 15:04")
	if preview := firstLine(item.FirstMessage); preview != "" {
		detail += " · " + preview
	}
	if item.Unavailable != "" {
		detail += " · unavailable: " + item.Unavailable
	}
	return name, detail
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Render returns the bounded text summary used by /session and diagnostics.
func (s *Selector) Render() []string {
	items := s.PageItems()
	var out []string
	totalPages := (len(s.Filtered) + s.PageSize - 1) / s.PageSize
	if totalPages == 0 {
		totalPages = 1
	}
	label := "Sessions"
	if s.Models {
		label = "Models"
	} else if s.Thinking {
		label = "Thinking levels"
	}
	out = append(out, fmt.Sprintf("%s (page %d/%d, %d matches, filter: %q)", label, s.Page+1, totalPages, len(s.Filtered), s.Filter))
	for _, it := range items {
		marker := " "
		if it.ID == s.ActiveID {
			marker = "*"
		}
		preview := firstLine(it.FirstMessage)
		identity := it.ID
		if !s.Models && !s.Thinking {
			identity = shortID(identity)
			if it.Name != "" {
				identity = firstLine(it.Name) + " (" + identity + ")"
			}
			identity += " " + it.Modified.Format("01-02 15:04")
			if it.Unavailable != "" && it.ID != s.ActiveID {
				identity += " [unavailable: " + it.Unavailable + "]"
			}
		}
		out = append(out, fmt.Sprintf("%s %s — %s", marker, identity, preview))
	}
	out = append(out, "type a search query; empty cancels")
	return out
}
