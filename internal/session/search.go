package session

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Transcript search bounds. Phi renders one page at a time, so a read that
// cannot finish inside these limits answers unavailable instead of returning
// a partial page that looks complete.
const (
	SearchMaxEntries      = 256
	SearchMaxMatches      = 50
	SearchMaxSnippetBytes = 2048
	SearchMaxRecordBytes  = 8 << 20
	SearchMaxPageBytes    = 32 << 20
)

// SearchOptions describes one page read.
type SearchOptions struct {
	Query string
	// AnchorLeafID pins the branch the page searches. The empty value uses
	// the current leaf, which is what an initial request does.
	AnchorLeafID string
	// StartIndex resumes at the next uninspected path entry.
	StartIndex int
	Budget     time.Duration
}

// SearchMatch is one visible-text hit. It never carries hidden data.
type SearchMatch struct {
	EntryID string
	Role    string
	Snippet string
}

// SearchPage is one bounded answer. NextIndex is the number of path entries
// inspected, so a caller can resume exactly where this page stopped.
type SearchPage struct {
	SessionID string
	AnchorID  string
	Matches   []SearchMatch
	NextIndex int
	Complete  bool
}

// ErrSearchUnavailable reports a page that cannot be answered honestly:
// wrong branch, stale anchor, oversized records, or an expired budget.
var ErrSearchUnavailable = errors.New("session: transcript search unavailable")

// SearchVisible searches visible text on one active branch. It reads record
// payloads outside the store lock, from a snapshot captured under it, so a
// long search never blocks typing or settlement.
func (s *Store) SearchVisible(ctx context.Context, opts SearchOptions) (SearchPage, error) {
	if opts.Query == "" {
		return SearchPage{}, fmt.Errorf("%w: empty query", ErrSearchUnavailable)
	}
	pattern, err := regexp.Compile("(?i)" + regexp.QuoteMeta(opts.Query))
	if err != nil {
		return SearchPage{}, fmt.Errorf("%w: invalid query", ErrSearchUnavailable)
	}
	if opts.StartIndex < 0 {
		return SearchPage{}, fmt.Errorf("%w: negative start", ErrSearchUnavailable)
	}
	budget := opts.Budget
	if budget <= 0 {
		budget = 1500 * time.Millisecond
	}
	deadline := time.Now().Add(budget)

	s.mu.Lock()
	leafID := opts.AnchorLeafID
	if leafID == "" {
		leafID = s.leafID
	}
	active, err := s.pathLocked()
	if err != nil {
		s.mu.Unlock()
		return SearchPage{}, err
	}
	if !onPath(active, leafID) {
		// The anchor left the active branch: this page would mix branches.
		s.mu.Unlock()
		return SearchPage{}, fmt.Errorf("%w: anchor is not on the active branch", ErrSearchUnavailable)
	}
	path, err := buildPathByID(s.byID, s.entries, leafID)
	if err != nil {
		s.mu.Unlock()
		return SearchPage{}, fmt.Errorf("%w: anchor is unusable", ErrSearchUnavailable)
	}
	sessionID := ""
	if s.header != nil {
		sessionID = s.header.ID
	}
	id := leafID
	s.mu.Unlock()

	page := SearchPage{SessionID: sessionID, AnchorID: id}
	if opts.StartIndex > len(path) {
		return SearchPage{}, fmt.Errorf("%w: start beyond the branch", ErrSearchUnavailable)
	}
	pageBytes := 0
	for i := opts.StartIndex; i < len(path); i++ {
		if err := ctx.Err(); err != nil {
			return SearchPage{}, fmt.Errorf("%w: cancelled", ErrSearchUnavailable)
		}
		if time.Now().After(deadline) {
			return SearchPage{}, fmt.Errorf("%w: budget expired", ErrSearchUnavailable)
		}
		if i-opts.StartIndex >= SearchMaxEntries {
			page.NextIndex = i
			return page, nil
		}
		entry := path[i]
		page.NextIndex = i + 1
		size := int64(len(entry.Raw))
		if size <= 0 || size > SearchMaxRecordBytes {
			return SearchPage{}, fmt.Errorf("%w: record too large", ErrSearchUnavailable)
		}
		pageBytes += int(size)
		if pageBytes > SearchMaxPageBytes {
			return SearchPage{}, fmt.Errorf("%w: page too large", ErrSearchUnavailable)
		}
		if err := s.searchEntry(ctx, pattern, entry, opts, &page); err != nil {
			return SearchPage{}, err
		}
		if len(page.Matches) >= SearchMaxMatches {
			return page, nil
		}
	}
	page.Complete = true
	return page, nil
}

// onPath reports whether id is part of the given branch.
func onPath(path []*Entry, id string) bool {
	if id == "" {
		return len(path) == 0
	}
	for _, entry := range path {
		if entry.ID() == id {
			return true
		}
	}
	return false
}

// searchEntry appends at most one match for one entry's visible text.
func (s *Store) searchEntry(ctx context.Context, pattern *regexp.Regexp, entry *Entry, opts SearchOptions, page *SearchPage) error {
	if len(page.Matches) >= SearchMaxMatches {
		return nil
	}
	role, texts := visibleTexts(entry)
	if role == "" {
		return nil
	}
	for _, text := range texts {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: cancelled", ErrSearchUnavailable)
		}
		loc := pattern.FindStringIndex(text)
		if loc == nil {
			continue
		}
		snippet, ok := snippetAround(text, loc[0], loc[1])
		if !ok {
			return fmt.Errorf("%w: snippet is not representable", ErrSearchUnavailable)
		}
		page.Matches = append(page.Matches, SearchMatch{EntryID: entry.ID(), Role: role, Snippet: snippet})
		return nil
	}
	return nil
}

// visibleTexts returns the searchable text of one entry. Thinking, tool
// arguments, images, extension state, and summaries are never included.
func visibleTexts(entry *Entry) (string, []string) {
	if entry.Message == nil {
		return "", nil
	}
	message := entry.Message.Message
	switch {
	case message.User != nil:
		return "user", userTexts(message.User.Content)
	case message.Assistant != nil:
		var texts []string
		for _, block := range message.Assistant.Content {
			if block.Type == "text" && block.Text != nil {
				texts = append(texts, block.Text.Text)
			}
		}
		return "assistant", texts
	case message.ToolResult != nil:
		var texts []string
		for _, block := range message.ToolResult.Content {
			if block.Type == "text" && block.Text != nil {
				texts = append(texts, block.Text.Text)
			}
		}
		return "toolResult", texts
	}
	return "", nil
}

func userTexts(content any) []string {
	switch value := content.(type) {
	case string:
		return []string{value}
	case []UserContentBlock:
		var texts []string
		for _, block := range value {
			if block.Type == "text" && block.Text != nil {
				texts = append(texts, block.Text.Text)
			}
		}
		return texts
	}
	return nil
}

// snippetAround returns a UTF-8-safe window around one match. A snippet that
// cannot be cut safely is reported as unrepresentable, never truncated badly.
func snippetAround(text string, start, end int) (string, bool) {
	if len(text) <= SearchMaxSnippetBytes {
		return text, true
	}
	half := (SearchMaxSnippetBytes - (end - start)) / 2
	if half < 0 {
		return "", false
	}
	from := start - half
	if from < 0 {
		from = 0
	}
	to := from + SearchMaxSnippetBytes
	if to > len(text) {
		to = len(text)
		from = to - SearchMaxSnippetBytes
		if from < 0 {
			from = 0
		}
	}
	for from > 0 && !utf8.RuneStart(text[from]) {
		from++
	}
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to--
	}
	if from >= to {
		return "", false
	}
	return strings.TrimSpace(text[from:to]), true
}
