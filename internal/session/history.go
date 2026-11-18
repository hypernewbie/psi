package session

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Session-history reads for the read_session tool. The index walks the raw
// active-branch entries (not the compaction-aware projection), so details that
// a compaction summary dropped are still reachable in a bounded excerpt.
//
// Behavior mirrors the pi-ac session-reader extension: list newest first,
// search literal text case-insensitively, read a bounded entry range, and keep
// tool call/result pairs together.
const (
	DefaultHistoryReadTokens = 4_000
	MaxHistoryReadTokens     = 8_000
	HistoryListLimit         = 40
	HistorySearchLimit       = 20
	HistoryPreviewChars      = 500
)

const (
	HistoryActionList   = "list"
	HistoryActionSearch = "search"
	HistoryActionRead   = "read"
)

// HistoryItem is one raw entry projected to context messages.
type HistoryItem struct {
	ID       string
	Entry    *Entry
	Messages []AgentMessage
	Role     string
	Text     string
	Tokens   int
}

// HistoryResult is the tool result: display text plus bounded details.
type HistoryResult struct {
	OK              bool     `json:"ok"`
	Action          string   `json:"action"`
	ReturnedIDs     []string `json:"returnedIds"`
	NextEntryID     string   `json:"nextEntryId,omitempty"`
	MoreAvailable   bool     `json:"moreAvailable"`
	EstimatedTokens int      `json:"estimatedTokens"`
	Error           string   `json:"error,omitempty"`
	Text            string   `json:"-"`
}

// BuildHistoryIndex projects entries to searchable history items. Entries with
// no model-visible messages are skipped.
func BuildHistoryIndex(entries []*Entry) []HistoryItem {
	index := make([]HistoryItem, 0, len(entries))
	for _, entry := range entries {
		messages := EntryToContextMessages(entry)
		if len(messages) == 0 {
			continue
		}
		text := historySearchText(messages)
		index = append(index, HistoryItem{
			ID:       entry.ID(),
			Entry:    entry,
			Messages: messages,
			Role:     historyRole(entry, messages),
			Text:     text,
			Tokens:   historyTokens(text),
		})
	}
	return index
}

// ListHistory renders the newest entries first.
func ListHistory(entries []*Entry, limit int) HistoryResult {
	index := BuildHistoryIndex(entries)
	items := lastItems(index, clampHistoryLimit(limit, HistoryListLimit))
	lines := make([]string, 0, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, "id: "+item.ID+" | "+item.Role+" | ~"+strconv.Itoa(item.Tokens)+" tokens | "+quoteHistoryPreview(item.Text))
		ids = append(ids, item.ID)
	}
	text := "Session history index is empty."
	if len(lines) > 0 {
		text = "Session history index (newest first)\n" + strings.Join(lines, "\n")
	}
	return historySuccess(HistoryActionList, ids, text, false, "")
}

// SearchHistory scans the raw index for a literal, case-insensitive query.
func SearchHistory(entries []*Entry, query string, limit int) HistoryResult {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return historyFailure(HistoryActionSearch, "Search requires a non-empty query.")
	}
	limit = clampHistoryLimit(limit, HistorySearchLimit)
	var items []HistoryItem
	index := BuildHistoryIndex(entries)
	for i := len(index) - 1; i >= 0 && len(items) < limit; i-- {
		if strings.Contains(strings.ToLower(index[i].Text), needle) {
			items = append(items, index[i])
		}
	}
	lines := make([]string, 0, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, "id: "+item.ID+" | "+item.Role+" | ~"+strconv.Itoa(item.Tokens)+" tokens | "+quoteHistoryPreview(matchExcerpt(item.Text, needle)))
		ids = append(ids, item.ID)
	}
	text := "No raw session entries match " + strconv.Quote(query) + "."
	if len(lines) > 0 {
		text = "Session history matches for " + strconv.Quote(query) + " (newest first)\n" + strings.Join(lines, "\n")
	}
	return historySuccess(HistoryActionSearch, ids, text, false, "")
}

// ReadHistory renders a bounded excerpt from startID through endID. Tool
// call/result pairs are always returned whole, and an over-long first group is
// bounded head/tail instead of being split.
func ReadHistory(entries []*Entry, startID, endID string, requestedTokens int) HistoryResult {
	if startID == "" || endID == "" {
		return historyFailure(HistoryActionRead, "Read requires both startEntryId and endEntryId from list or search.")
	}
	index := BuildHistoryIndex(entries)
	start, end := -1, -1
	for i, item := range index {
		if item.ID == startID {
			start = i
		}
		if item.ID == endID {
			end = i
		}
	}
	if start < 0 || end < 0 {
		return historyFailure(HistoryActionRead, "One or both entry IDs are not on the active raw session branch.")
	}
	if start > end {
		return historyFailure(HistoryActionRead, "startEntryId must come before or equal endEntryId on the active branch.")
	}
	pairs := historyToolPairs(index)
	start, end = expandHistoryPairs(start, end, pairs)
	maxTokens := clampHistoryTokens(requestedTokens)
	// Reserve room for the stable header lines so the whole result stays
	// within the requested bound, not merely its body.
	maxChars := int(math.Max(500, float64(maxTokens*4-600)))
	var rendered []string
	var returnedIDs []string
	cursor := start
	usedChars := 0
	for cursor <= end {
		groupEnd := cursor
		if bound, ok := pairs[cursor]; ok && bound.end > groupEnd {
			groupEnd = bound.end
		}
		group := index[cursor : groupEnd+1]
		remaining := maxChars - usedChars
		if remaining <= 0 {
			break
		}
		groupText := renderHistoryGroup(group, remaining)
		if len(groupText) > remaining && len(rendered) > 0 {
			break
		}
		if len(groupText) > remaining {
			bounded := boundHistoryText(renderHistoryGroup(group, int(math.Max(500, float64(remaining)))), remaining)
			rendered = append(rendered, bounded)
			for _, item := range group {
				returnedIDs = append(returnedIDs, item.ID)
			}
			cursor = groupEnd + 1
			break
		}
		rendered = append(rendered, groupText)
		for _, item := range group {
			returnedIDs = append(returnedIDs, item.ID)
		}
		usedChars += len(groupText)
		cursor = groupEnd + 1
	}
	if len(returnedIDs) == 0 {
		return historyFailure(HistoryActionRead, "The requested range could not fit the bounded output. Use list or search to select a narrower range.")
	}
	moreAvailable := cursor <= end
	nextEntryID := ""
	if moreAvailable {
		nextEntryID = index[cursor].ID
	}
	body := strings.Join(rendered, "\n\n")
	moreText := "no"
	if moreAvailable {
		moreText = "yes (continue from " + nextEntryID + ")"
	}
	header := strings.Join([]string{
		"Session history excerpt",
		"- Source: active branch raw entries",
		"- Returned: " + returnedIDs[0] + " .. " + returnedIDs[len(returnedIDs)-1],
		"- Estimated size: ~" + strconv.Itoa(historyTokens(body)) + " tokens",
		"- More available: " + moreText,
	}, "\n")
	return historySuccess(HistoryActionRead, returnedIDs, header+"\n\n"+body, moreAvailable, nextEntryID)
}

func historySuccess(action string, returnedIDs []string, text string, moreAvailable bool, nextEntryID string) HistoryResult {
	if returnedIDs == nil {
		returnedIDs = []string{}
	}
	return HistoryResult{
		OK: true, Action: action, ReturnedIDs: returnedIDs, NextEntryID: nextEntryID,
		MoreAvailable: moreAvailable, EstimatedTokens: historyTokens(text), Text: text,
	}
}

func historyFailure(action, message string) HistoryResult {
	return HistoryResult{
		Action: action, ReturnedIDs: []string{}, Error: message,
		Text: "Cannot read session history: " + message,
	}
}

func clampHistoryTokens(value int) int {
	if value <= 0 {
		return DefaultHistoryReadTokens
	}
	return int(math.Max(500, math.Min(MaxHistoryReadTokens, float64(value))))
}

func clampHistoryLimit(value, max int) int {
	if value <= 0 {
		return max
	}
	return int(math.Max(1, math.Min(float64(max), float64(value))))
}

func lastItems(items []HistoryItem, limit int) []HistoryItem {
	if len(items) <= limit {
		out := make([]HistoryItem, len(items))
		for i := range items {
			out[i] = items[len(items)-1-i]
		}
		return out
	}
	out := make([]HistoryItem, 0, limit)
	for i := len(items) - 1; i >= len(items)-limit; i-- {
		out = append(out, items[i])
	}
	return out
}

// historyTokens is a bytes/4 estimate. It is a conservative superset of Pi's
// UTF-16/4 heuristic for non-ASCII text, which keeps bounded reads bounded.
func historyTokens(text string) int { return (len(text) + 3) / 4 }

func historyRole(entry *Entry, messages []AgentMessage) string {
	switch entry.Kind {
	case "compaction":
		return "compaction summary"
	case "branch_summary":
		return "branch summary"
	}
	if len(messages) == 1 {
		message := messages[0]
		if message.Role == "toolResult" && message.ToolResult != nil {
			name := message.ToolResult.ToolName
			if name == "" {
				name = "tool"
			}
			return "tool result (" + name + ")"
		}
		return message.Role
	}
	return "messages"
}

func historySearchText(messages []AgentMessage) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		switch {
		case message.CompactionSummary != nil:
			parts = append(parts, message.CompactionSummary.Summary)
		case message.BranchSummary != nil:
			parts = append(parts, message.BranchSummary.Summary)
		case message.ToolResult != nil:
			name := message.ToolResult.ToolName
			if name == "" {
				name = "tool"
			}
			parts = append(parts, name+"\n"+userContentText(message.ToolResult.Content))
		case message.Assistant != nil:
			var body []string
			for _, block := range message.Assistant.Content {
				if block.Text != nil {
					body = append(body, block.Text.Text)
				}
				if block.Call != nil {
					body = append(body, block.Call.Name+"\n"+safeHistoryJSON(block.Call.Arguments))
				}
			}
			parts = append(parts, strings.Join(body, "\n"))
		case message.User != nil:
			parts = append(parts, userContentText(message.User.Content))
		case message.Custom != nil:
			parts = append(parts, userContentText(message.Custom.Content))
		default:
			raw, err := json.Marshal(message)
			if err != nil {
				parts = append(parts, "[unserializable]")
			} else {
				parts = append(parts, string(raw))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func userContentText(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []UserContentBlock:
		parts := make([]string, 0, len(value))
		for _, block := range value {
			if block.Text != nil {
				parts = append(parts, block.Text.Text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return safeHistoryJSON(value)
	}
}

func safeHistoryJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "[unserializable]"
	}
	return string(raw)
}

func quoteHistoryPreview(text string) string {
	return strconv.Quote(boundHistoryText(collapseHistoryWhitespace(text), HistoryPreviewChars))
}

func collapseHistoryWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func matchExcerpt(text, needle string) string {
	index := strings.Index(strings.ToLower(text), needle)
	if index < 0 {
		return boundHistoryText(text, HistoryPreviewChars)
	}
	start := index - HistoryPreviewChars/2
	if start < 0 {
		start = 0
	}
	end := index + len(needle) + HistoryPreviewChars/2
	if end > len(text) {
		end = len(text)
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(text) {
		suffix = "…"
	}
	return prefix + text[start:end] + suffix
}

func renderHistoryGroup(items []HistoryItem, maxChars int) string {
	perItem := maxChars/len(items) - 80
	if perItem < 300 {
		perItem = 300
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, "--- entry "+item.ID+" | "+item.Role+" ---\n"+
			boundHistoryText(item.Text, perItem)+"\n--- end entry "+item.ID+" ---")
	}
	return strings.Join(parts, "\n\n")
}

func boundHistoryText(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	head := (maxChars*7 + 9) / 10
	if head < 1 {
		head = 1
	}
	tail := maxChars - head
	if tail < 1 {
		tail = 1
	}
	omitted := len(text) - head - tail
	if omitted < 0 {
		omitted = 0
	}
	return clipHistoryHead(text, head) + "\n[... " + strconv.Itoa(omitted) + " characters omitted ...]\n" + clipHistoryTail(text, tail)
}

// clipHistoryHead truncates at a rune boundary at or before n bytes.
func clipHistoryHead(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

// clipHistoryTail truncates at a rune boundary at or after len-n bytes.
func clipHistoryTail(text string, n int) string {
	if len(text) <= n {
		return text
	}
	start := len(text) - n
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

type historyPair struct {
	start int
	end   int
}

// historyToolPairs maps each tool call/result index to the other half's span.
func historyToolPairs(index []HistoryItem) map[int]historyPair {
	calls := map[string]int{}
	results := map[string]int{}
	for i, item := range index {
		for _, message := range item.Messages {
			if message.Assistant != nil {
				for _, block := range message.Assistant.Content {
					if block.Call != nil {
						calls[block.Call.ID] = i
					}
				}
			}
			if message.ToolResult != nil && message.ToolResult.ToolCallID != "" {
				results[message.ToolResult.ToolCallID] = i
			}
		}
	}
	pairs := make(map[int]historyPair, len(calls)*2)
	for callID, callIndex := range calls {
		resultIndex, ok := results[callID]
		if !ok {
			continue
		}
		start, end := callIndex, resultIndex
		if start > end {
			start, end = end, start
		}
		pairs[callIndex] = historyPair{start: start, end: end}
		pairs[resultIndex] = historyPair{start: start, end: end}
	}
	return pairs
}

func expandHistoryPairs(start, end int, pairs map[int]historyPair) (int, int) {
	for changed := true; changed; {
		changed = false
		for i := start; i <= end; i++ {
			bound, ok := pairs[i]
			if !ok {
				continue
			}
			if bound.start < start {
				start, changed = bound.start, true
			}
			if bound.end > end {
				end, changed = bound.end, true
			}
		}
	}
	return start, end
}
