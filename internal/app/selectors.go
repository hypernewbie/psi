package app

import (
	"fmt"
	"strconv"
	"strings"

	"psi/internal/session"
)

// Selector is the bounded inline flow (P2-039, Section 7.2).
// Twenty items per page, filter, stable IDs, active/unavailable markers. No fullscreen.
type Selector struct {
	Items    []session.SessionInfo
	Filtered []session.SessionInfo
	Page     int
	Filter   string
	ActiveID string
	PageSize int
}

func NewSelector(items []session.SessionInfo, activeID string) *Selector {
	s := &Selector{Items: items, ActiveID: activeID, PageSize: 20}
	s.apply()
	return s
}

func (s *Selector) apply() {
	if s.Filter == "" {
		s.Filtered = append([]session.SessionInfo(nil), s.Items...)
	} else {
		q := strings.ToLower(s.Filter)
		s.Filtered = nil
		for _, it := range s.Items {
			if strings.Contains(strings.ToLower(it.ID), q) || strings.Contains(strings.ToLower(it.FirstMessage), q) {
				s.Filtered = append(s.Filtered, it)
			}
		}
	}
	// Stable order: preserve discovery order (recency). Page bounds.
	if s.Page < 0 || len(s.Filtered) == 0 {
		s.Page = 0
	}
	if s.Page*s.PageSize >= len(s.Filtered) && len(s.Filtered) > 0 {
		s.Page = (len(s.Filtered) - 1) / s.PageSize
	}
}

func (s *Selector) SetFilter(q string) {
	s.Filter = q
	s.Page = 0
	s.apply()
}

func (s *Selector) NextPage() { s.Page++; s.apply() }
func (s *Selector) PrevPage() {
	if s.Page > 0 {
		s.Page--
	}
	s.apply()
}

// PageItems returns the current 20-item page.
func (s *Selector) PageItems() []session.SessionInfo {
	start := s.Page * s.PageSize
	if start >= len(s.Filtered) {
		return nil
	}
	end := start + s.PageSize
	if end > len(s.Filtered) {
		end = len(s.Filtered)
	}
	return s.Filtered[start:end]
}

// Render returns bounded inline lines (no fullscreen, no full-transcript loading).
func (s *Selector) Render() []string {
	items := s.PageItems()
	var out []string
	totalPages := (len(s.Filtered) + s.PageSize - 1) / s.PageSize
	if totalPages == 0 {
		totalPages = 1
	}
	out = append(out, fmt.Sprintf("Sessions (page %d/%d, %d matches, filter: %q)", s.Page+1, totalPages, len(s.Filtered), s.Filter))
	for i, it := range items {
		marker := " "
		if it.ID == s.ActiveID {
			marker = "*"
		}
		preview := firstLine(it.FirstMessage)
		out = append(out, fmt.Sprintf("%s%2d) %s %s — %s", marker, s.Page*s.PageSize+i+1, it.ID, it.Modified.Format("01-02 15:04"), preview))
	}
	out = append(out, "n/p: page, /text: filter, number: select, empty: cancel")
	return out
}

// Resolve parses selector input: number, n/p, /filter, or empty cancel.
func (s *Selector) Resolve(line string) (selected *session.SessionInfo, cancel bool, redraw bool) {
	t := strings.TrimSpace(line)
	if t == "" {
		return nil, true, false
	}
	if t == "n" || t == "next" {
		s.NextPage()
		return nil, false, true
	}
	if t == "p" || t == "prev" {
		s.PrevPage()
		return nil, false, true
	}
	if strings.HasPrefix(t, "/") {
		s.SetFilter(strings.TrimPrefix(t, "/"))
		return nil, false, true
	}
	n, err := strconv.Atoi(t)
	if err == nil && n >= 1 && n <= len(s.Filtered) {
		it := s.Filtered[n-1]
		return &it, false, false
	}
	return nil, false, true
}
