package webaccess

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf16"

	"psi/internal/event"
)

// DisplayCall returns the bounded card data for one web tool invocation. It
// reports false for tools outside the web card set. The same argument parser
// rules decide the display shape, but this function performs no I/O and never
// changes validation, admission, or the arguments the tool receives.
func (c *Client) DisplayCall(name string, args map[string]any) (event.WebCall, bool) {
	switch name {
	case ToolFetch:
		return c.fetchCallDisplay(args), true
	case ToolSearch:
		return c.searchCallDisplay(args), true
	case ToolContent:
		return c.contentCallDisplay(args), true
	}
	return event.WebCall{}, false
}

// displayContextKey marks whether the request that produced a result
// advertised get_search_content. The flag is display-only state; it never
// changes admission or tool behavior.
type displayContextKey struct{}

// WithRetrievalDisplay records the advertised retrieval tool for the current
// request. Producers use it to decide whether a stored result can actually be
// paged, instead of reading the current settings at redraw time.
func WithRetrievalDisplay(ctx context.Context, advertised bool) context.Context {
	return context.WithValue(ctx, displayContextKey{}, advertised)
}

func retrievalDisplayAdvertised(ctx context.Context) bool {
	advertised, _ := ctx.Value(displayContextKey{}).(bool)
	return advertised
}

// fetchCallDisplay renders the call title from the same argument parser the
// tool executes with. An explicit non-default mode becomes its own row.
func (c *Client) fetchCallDisplay(args map[string]any) event.WebCall {
	call := event.WebCall{Title: event.WebFetch}
	_, hasURL := args["url"]
	_, hasURLs := args["urls"]
	if !hasURL && !hasURLs {
		call.NoURL = true
		return call
	}
	parsed, err := parseFetchArgs(args, c.cfg)
	if err != nil {
		// A blank sole `url` is Pi's "no URL" case. A blank `url` beside a
		// `urls` array is a conflicting-input error, not a missing URL.
		if !hasURLs {
			if text, ok := args["url"].(string); ok && strings.TrimSpace(text) == "" {
				call.NoURL = true
				return call
			}
		}
		call.Invalid = true
		return call
	}
	if len(parsed.urls) == 1 {
		display, ok := c.displayURL(parsed.urls[0], event.WebMaxCallStringBytes)
		if !ok {
			call.Invalid = true
			return call
		}
		call.Title = event.WebFetch + " " + display
	} else {
		call.Title = fmt.Sprintf("%s %d URLs", event.WebFetch, len(parsed.urls))
		for i, target := range parsed.urls {
			if i == event.WebMaxCallEntries {
				break
			}
			display, ok := c.displayURL(target, event.WebMaxCallStringBytes)
			if !ok {
				// A batch with an unshowable target gets the fixed label; the
				// input is never echoed back.
				return event.WebCall{Title: event.WebFetch, Invalid: true}
			}
			call.Entries = append(call.Entries, display)
		}
		if len(parsed.urls) > event.WebMaxCallEntries {
			call.Overflow = len(parsed.urls) - event.WebMaxCallEntries
		}
	}
	if mode, ok := args["mode"].(string); ok {
		mode = strings.TrimSpace(mode)
		// Pi prints the mode only when the call names a non-default one.
		if mode != "" && mode != "readable" && mode == parsed.mode {
			call.Mode = clipMeta(mode, 32)
		}
	}
	return call
}

// searchCallDisplay mirrors Pi's quotes for one query and count line for a
// multi-query search. psi accepts at most three queries, so the five-entry
// overflow line is unreachable for a valid search but stays bounded.
func (c *Client) searchCallDisplay(args map[string]any) event.WebCall {
	call := event.WebCall{Title: event.WebSearch}
	_, hasQuery := args["query"]
	_, hasQueries := args["queries"]
	if !hasQuery && !hasQueries {
		call.NoQuery = true
		return call
	}
	parsed, err := parseSearchArgs(args, c.cfg)
	if err != nil {
		call.Invalid = true
		return call
	}
	if len(parsed.queries) == 1 {
		call.Title = event.WebSearch + " \"" + c.displayText(parsed.queries[0], 4096) + "\""
		return call
	}
	call.Title = fmt.Sprintf("%s %d queries", event.WebSearch, len(parsed.queries))
	for i, query := range parsed.queries {
		if i == event.WebMaxCallEntries {
			break
		}
		call.Entries = append(call.Entries, c.displayQuoted(query, 4096))
	}
	if len(parsed.queries) > event.WebMaxCallEntries {
		call.Overflow = len(parsed.queries) - event.WebMaxCallEntries
	}
	return call
}

// contentCallDisplay uses Pi's target order: URL, then urlIndex, then the
// call's responseId. A bare offset is shown only when no page target exists;
// findText reports one search term and never its text.
func (c *Client) contentCallDisplay(args map[string]any) event.WebCall {
	call := event.WebCall{Title: event.WebContent}
	parsed, err := parseContentArgs(args, c.cfg)
	if err != nil {
		call.Invalid = true
		return call
	}
	switch {
	case parsed.hasURL:
		display, ok := c.displayURL(parsed.url, event.WebMaxCallStringBytes)
		if !ok {
			call.Invalid = true
			return call
		}
		call.Title += " " + abbreviateDisplay(display, 30, 27)
	case parsed.hasIndex:
		call.Title += fmt.Sprintf(" urlIndex=%d", parsed.urlIndex)
	default:
		call.Title += " " + utf16Prefix(c.redact(parsed.responseID, 128), 8)
	}
	if parsed.hasOffset && !parsed.hasURL && !parsed.hasIndex {
		call.Title += fmt.Sprintf(" offset=%d", parsed.offset)
	}
	if parsed.hasFind {
		call.Title += " · find 1"
	}
	return call
}

// displayText redacts and sanitizes one call field, then clips it to the
// shared call-string bound.
func (c *Client) displayText(text string, redactLimit int) string {
	return clipMeta(c.redact(strings.TrimSpace(text), redactLimit), event.WebMaxCallStringBytes)
}

// displayQuoted wraps a query in the display quotes and keeps the stored entry
// inside the per-entry byte bound, including the quotes.
func (c *Client) displayQuoted(text string, redactLimit int) string {
	value := c.displayText(text, redactLimit)
	if len(value) > event.WebMaxCallStringBytes-2 {
		value = runePrefix(value, event.WebMaxCallStringBytes-2)
	}
	return "\"" + value + "\""
}

// abbreviateDisplay applies Pi's 30-character URL abbreviation: URLs longer
// than 30 UTF-16 units keep 27 units and an ellipsis.
func abbreviateDisplay(text string, limit, keep int) string {
	if utf16Units(text) <= limit {
		return text
	}
	return utf16Prefix(text, keep) + "..."
}

// displayURL redacts configured secrets, then removes userinfo, query, and
// fragment from the display copy. The network request keeps the original URL.
func (c *Client) displayURL(raw string, limit int) (string, bool) {
	redacted := c.redact(strings.TrimSpace(raw), maxURLBytes)
	parsed, err := url.Parse(redacted)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", false
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", false
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return runePrefix(parsed.String(), limit), true
}

// pageResultDisplay builds the single-page result card from the redacted page
// text. The character count uses UTF-16 units, matching Pi's JavaScript length
// and the terminal width helpers. A measured duration is stored only for a
// call that actually executed.
func (c *Client) pageResultDisplay(page pageData, truncated bool, duration time.Duration) *event.WebResult {
	title := clipMeta(page.Title, 300)
	if title == "" {
		title = "Untitled"
	}
	return &event.WebResult{
		Kind:        event.WebResultPage,
		Title:       title,
		Chars:       int64(utf16Units(page.Text)),
		HasChars:    true,
		Truncated:   truncated,
		Preview:     previewLines(page.Text, event.WebMaxPreviewUnits, event.WebMaxPreviewLines),
		DurationMs:  max(0, duration.Milliseconds()),
		HasDuration: true,
	}
}

// batchResultDisplay reports the actual fetch counts. The stored label is a
// settled fact: it requires a successful store write and an advertised
// retrieval tool for this request. The expanded rows come from successful
// pages in input order; the collapsed card never shows them.
func (c *Client) batchResultDisplay(ctx context.Context, ok, total int, responseID string, pages []pageData, errs []error) *event.WebResult {
	return &event.WebResult{
		Kind:      event.WebResultBatch,
		OK:        ok,
		Total:     total,
		HasCounts: true,
		Stored:    responseID != "" && retrievalDisplayAdvertised(ctx),
		Expanded:  batchPagePreview(pages, errs),
	}
}

// batchPagePreview keeps one bounded source line per successful page in input
// order. It never includes a failed page or its error text.
func batchPagePreview(pages []pageData, errs []error) []string {
	var out []string
	units := 0
	for i := range pages {
		if errs[i] != nil {
			continue
		}
		line := firstLineText(pages[i].Text)
		if line == "" {
			continue
		}
		label := pages[i].FinalURL
		if label == "" {
			label = pages[i].URL
		}
		row := utf16Prefix(label+": "+line, min(event.WebMaxPreviewUnits-units, 200))
		if row == "" {
			break
		}
		out = append(out, strings.Clone(row))
		units += utf16Units(row)
		if len(out) == event.WebMaxPreviewLines || units >= event.WebMaxPreviewUnits {
			break
		}
	}
	return out
}

// searchResultDisplay reports successful queries, post-dedup sources, and the
// first inline content lines. The content label requires a successful store
// write, at least one stored page with inline content, and an advertised
// retrieval tool. The collapsed view shows the first 120 units; expansion
// shows the full bounded preview.
func (c *Client) searchResultDisplay(ctx context.Context, okQueries, totalQueries, sources int, responseID string, results []storedResult) *event.WebResult {
	hasInline := false
	for _, result := range results {
		if result.Content != "" {
			hasInline = true
			break
		}
	}
	return &event.WebResult{
		Kind:       event.WebResultSearch,
		OK:         okQueries,
		Total:      totalQueries,
		HasCounts:  true,
		Sources:    sources,
		HasSources: true,
		Stored:     responseID != "" && hasInline && retrievalDisplayAdvertised(ctx),
		Preview:    inlineContentPreview(results),
	}
}

// inlineContentPreview takes the bounded lines of the first inline page in
// result order that has an eligible non-empty line.
func inlineContentPreview(results []storedResult) []string {
	for _, result := range results {
		if result.Content == "" {
			continue
		}
		if lines := previewLines(result.Content, event.WebMaxPreviewUnits, event.WebMaxPreviewLines); len(lines) > 0 {
			return lines
		}
	}
	return nil
}

// firstLineText returns the first non-empty, control-free line of text. It
// scans line by line so a large body is never split into a slice of strings.
func firstLineText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for len(text) > 0 {
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			text = ""
		}
		line = strings.TrimSpace(censorControl(line))
		if line != "" {
			return line
		}
	}
	return ""
}

// contentResultDisplay reports the scalar total and the actual half-open range
// the producer returned. The end is the real next offset, never a requested
// offset plus limit. Expanded rows are the actually returned slice.
func contentResultDisplay(title string, length, start, next int, truncated bool, expanded []string) *event.WebResult {
	title = clipMeta(title, 300)
	if title == "" {
		title = "Content"
	}
	result := &event.WebResult{
		Kind:      event.WebResultContent,
		Title:     title,
		Chars:     int64(length),
		HasChars:  true,
		Truncated: truncated,
		Expanded:  expanded,
	}
	if start != 0 || next < length {
		end := length
		if next < length {
			end = next
		}
		result.HasRange = true
		result.RangeStart = int64(start)
		result.RangeEnd = int64(end)
	}
	return result
}

// utf16Units counts UTF-16 code units, the display unit Pi uses. A character
// outside the Basic Multilingual Plane, such as an emoji, counts as two.
func utf16Units(s string) int { return len(utf16.Encode([]rune(s))) }

// previewLines keeps at most maxLines non-empty source lines and at most
// maxUnits UTF-16 units. Control characters are folded here, before the
// display is stored, so a page body cannot inject a terminal sequence. Lines
// are scanned one at a time so a large body is never split into a full slice.
// The final line is clipped on a scalar boundary; the renderer wraps by
// grapheme.
func previewLines(text string, maxUnits, maxLines int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var out []string
	units := 0
	for len(text) > 0 && len(out) < maxLines && units < maxUnits {
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			text = ""
		}
		line = strings.TrimSpace(censorControl(line))
		if line == "" {
			continue
		}
		line = utf16Prefix(line, maxUnits-units)
		if line == "" {
			break
		}
		// Clone so the bounded preview never keeps a larger page body alive.
		out = append(out, strings.Clone(line))
		units += utf16Units(line)
	}
	return out
}

// utf16Prefix returns the prefix of s that fits in max UTF-16 units without
// splitting a scalar value.
func utf16Prefix(s string, max int) string {
	units := 0
	for i, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > max {
			return s[:i]
		}
		units += width
	}
	return s
}
