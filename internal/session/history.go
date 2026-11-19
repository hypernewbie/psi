package session

import (
	"encoding/json"
	"regexp"
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
	MinHistoryReadTokens     = 500
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

// historyItem keeps only metadata until an action actually needs the text.
// Raw session bodies can be large, and list/read should not duplicate every
// older tool result merely to choose a few entry IDs.
func historyItem(entry *Entry) (HistoryItem, bool) {
	messages := EntryToContextMessages(entry)
	if len(messages) == 0 {
		return HistoryItem{}, false
	}
	return HistoryItem{ID: entry.ID(), Messages: messages, Role: historyRole(entry, messages)}, true
}

func loadHistoryText(item *HistoryItem) {
	item.Text = historySearchText(item.Messages)
	item.Tokens = historyTokens(item.Text)
}

// ListHistory renders the newest entries first.
func ListHistory(entries []*Entry, limit int) HistoryResult {
	limit = clampHistoryLimit(limit, HistoryListLimit)
	var items []HistoryItem
	for i := len(entries) - 1; i >= 0 && len(items) < limit; i-- {
		if item, ok := historyItem(entries[i]); ok {
			loadHistoryText(&item)
			items = append(items, item)
		}
	}
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
	needle := strings.TrimSpace(query)
	if needle == "" {
		return historyFailure(HistoryActionSearch, "Search requires a non-empty query.")
	}
	// FindStringIndex returns offsets into the original text, even when Unicode
	// case folding changes the byte length of a character.
	pattern, err := regexp.Compile("(?i)" + regexp.QuoteMeta(needle))
	if err != nil {
		return historyFailure(HistoryActionSearch, "Search query is not valid UTF-8.")
	}
	limit = clampHistoryLimit(limit, HistorySearchLimit)
	var items []HistoryItem
	for i := len(entries) - 1; i >= 0 && len(items) < limit; i-- {
		item, ok := historyItem(entries[i])
		if !ok {
			continue
		}
		loadHistoryText(&item)
		if pattern.MatchString(item.Text) {
			items = append(items, item)
		}
	}
	lines := make([]string, 0, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, "id: "+item.ID+" | "+item.Role+" | ~"+strconv.Itoa(item.Tokens)+" tokens | "+quoteHistoryPreview(matchExcerpt(item.Text, pattern)))
		ids = append(ids, item.ID)
	}
	queryPreview := quoteHistoryPreview(query)
	text := "No raw session entries match " + queryPreview + "."
	if len(lines) > 0 {
		text = "Session history matches for " + queryPreview + " (newest first)\n" + strings.Join(lines, "\n")
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
	index := make([]HistoryItem, 0, len(entries))
	for _, entry := range entries {
		if item, ok := historyItem(entry); ok {
			index = append(index, item)
		}
	}
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
	maxChars := maxTokens*4 - 600
	var rendered []string
	var returnedIDs []string
	cursor := start
	usedChars := 0
	for cursor <= end {
		_, groupEnd := expandHistoryPairs(cursor, cursor, pairs)
		group := index[cursor : groupEnd+1]
		for i := range group {
			loadHistoryText(&group[i])
		}
		remaining := maxChars - usedChars
		if len(rendered) > 0 {
			remaining -= len("\n\n")
		}
		if remaining <= 0 {
			break
		}
		// Do not truncate a later group merely because the preceding groups
		// filled this page. Let the caller read it on the next page instead.
		if len(rendered) > 0 && historyGroupSize(group) > remaining {
			break
		}
		groupText, fits := renderHistoryGroup(group, remaining)
		if !fits {
			if len(rendered) > 0 {
				break
			}
			return historyFailure(HistoryActionRead, "This tool-call group cannot fit within the requested bound. Increase maxTokens.")
		}
		if len(rendered) > 0 {
			usedChars += len("\n\n")
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
	text := header + "\n\n" + body
	if len(text) > maxTokens*4 {
		return historyFailure(HistoryActionRead, "Entry metadata cannot fit within the requested bound. Increase maxTokens.")
	}
	return historySuccess(HistoryActionRead, returnedIDs, text, moreAvailable, nextEntryID)
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
	if value < MinHistoryReadTokens {
		return MinHistoryReadTokens
	}
	if value > MaxHistoryReadTokens {
		return MaxHistoryReadTokens
	}
	return value
}

func clampHistoryLimit(value, max int) int {
	if value <= 0 {
		return max
	}
	if value > max {
		return max
	}
	return value
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
			if block.Type == "text" && block.Text != nil {
				parts = append(parts, block.Text.Text)
			}
		}
		return strings.Join(parts, "\n")
	case []any:
		// Reopened JSON messages decode their generic content arrays as []any.
		// Keep text blocks only, never embedded image data or opaque fields.
		var parts []string
		for _, value := range value {
			block, ok := value.(map[string]any)
			if ok && block["type"] == "text" {
				if text, ok := block["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
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

func matchExcerpt(text string, pattern *regexp.Regexp) string {
	match := pattern.FindStringIndex(text)
	if match == nil {
		return boundHistoryText(text, HistoryPreviewChars)
	}
	start := max(0, match[0]-HistoryPreviewChars/2)
	end := min(len(text), match[1]+HistoryPreviewChars/2)
	// Keep excerpt boundaries on UTF-8 rune boundaries.
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
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

func historyEntryFrame(item HistoryItem) (string, string) {
	return "--- entry " + item.ID + " | " + item.Role + " ---\n", "\n--- end entry " + item.ID + " ---"
}

func historyGroupSize(items []HistoryItem) int {
	size := 0
	for i, item := range items {
		before, after := historyEntryFrame(item)
		size += len(before) + len(item.Text) + len(after)
		if i > 0 {
			size += len("\n\n")
		}
	}
	return size
}

// renderHistoryGroup keeps every entry's frame when a paired group is too
// large; otherwise it fails rather than claiming omitted entries were returned.
func renderHistoryGroup(items []HistoryItem, maxChars int) (string, bool) {
	frameBytes := 0
	for i, item := range items {
		before, after := historyEntryFrame(item)
		frameBytes += len(before) + len(after)
		if i > 0 {
			frameBytes += len("\n\n")
		}
	}
	if frameBytes > maxChars {
		return "", false
	}
	perItem := (maxChars - frameBytes) / len(items)
	truncate := historyGroupSize(items) > maxChars
	parts := make([]string, 0, len(items))
	for _, item := range items {
		text := item.Text
		if truncate && len(text) > perItem {
			marker := "\n[... " + strconv.Itoa(len(text)) + " bytes omitted ...]\n"
			if perItem < len(marker) {
				return "", false
			}
			text = boundHistoryText(text, perItem)
		}
		before, after := historyEntryFrame(item)
		parts = append(parts, before+text+after)
	}
	return strings.Join(parts, "\n\n"), true
}

func boundHistoryText(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	// Reserve enough room for the omission count before dividing the rest
	// between head and tail. len(text) is an upper bound on the omitted bytes.
	marker := "\n[... " + strconv.Itoa(len(text)) + " bytes omitted ...]\n"
	if maxChars <= len(marker) {
		return clipHistoryHead(marker, maxChars)
	}
	available := maxChars - len(marker)
	head := (available*7 + 9) / 10
	tail := available - head
	prefix, suffix := clipHistoryHead(text, head), clipHistoryTail(text, tail)
	omitted := len(text) - len(prefix) - len(suffix)
	return prefix + "\n[... " + strconv.Itoa(omitted) + " bytes omitted ...]\n" + suffix
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
		for _, endpoint := range []int{callIndex, resultIndex} {
			if previous, ok := pairs[endpoint]; ok {
				pairs[endpoint] = historyPair{start: min(previous.start, start), end: max(previous.end, end)}
			} else {
				pairs[endpoint] = historyPair{start: start, end: end}
			}
		}
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
