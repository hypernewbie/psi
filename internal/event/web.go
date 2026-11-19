package event

// Web tool display titles. They are shown on the card and never replace the
// provider tool names.
const (
	WebFetch   = "fetch"
	WebSearch  = "search"
	WebContent = "get_content"
)

// Provider tool names for the three web tools whose rows use a card.
const (
	ToolNameFetch   = "fetch_content"
	ToolNameSearch  = "web_search"
	ToolNameContent = "get_search_content"
)

// Bounds shared by every web card producer and reader. They cap the display
// payload only; tool arguments and results keep their existing limits.
const (
	// WebMaxCallStringBytes bounds one displayed URL, query, or target.
	WebMaxCallStringBytes = 256
	// WebMaxTitleBytes bounds the whole first row, which adds a tool title
	// and, for search, quotes around a bounded query.
	WebMaxTitleBytes = 512
	// WebMaxCallEntries bounds the logical continuation entries of one call.
	WebMaxCallEntries = 5
	// WebMaxPreviewLines bounds the source lines retained for a result preview.
	WebMaxPreviewLines = 12
	// WebMaxPreviewUnits bounds the result preview in UTF-16 units, the unit
	// Pi uses for character counts.
	WebMaxPreviewUnits = 500
	// WebMaxCallsPerEntry matches the existing 64-summary-per-assistant limit.
	WebMaxCallsPerEntry = 64
	// WebMaxMetaBytes bounds one encoded `psiWeb` envelope. The per-call and
	// per-result caps are much smaller; this is the assistant-wide ceiling.
	WebMaxMetaBytes = 256 * 1024
)

// IsWebTool reports whether name is one of the three web tools that use the
// card layout. The check is display-only and never changes enablement.
func IsWebTool(name string) bool {
	switch name {
	case ToolNameFetch, ToolNameSearch, ToolNameContent:
		return true
	}
	return false
}

// WebTitle maps a provider tool name to Pi's display title. Unknown names keep
// their own value so a non-web caller cannot lose its label.
func WebTitle(name string) string {
	switch name {
	case ToolNameFetch:
		return WebFetch
	case ToolNameSearch:
		return WebSearch
	case ToolNameContent:
		return WebContent
	}
	return name
}

// WebCall is the bounded display data for one web tool call. It never carries
// raw arguments: structured URLs are stored without userinfo, query, or
// fragment, and invalid input is represented by a fixed marker.
type WebCall struct {
	// Title is the first card row, such as "fetch https://example.com/page".
	Title string `json:"title,omitempty"`
	// Entries are continuation rows, one logical entry each.
	Entries []string `json:"entries,omitempty"`
	// Overflow counts logical entries that the card does not print.
	Overflow int `json:"overflow,omitempty"`
	// Mode is set only for an explicit non-default fetch mode.
	Mode string `json:"mode,omitempty"`
	// Invalid marks a call whose arguments cannot be displayed. The renderer
	// prints a fixed label and never echoes the input.
	Invalid bool `json:"invalid,omitempty"`
	// NoURL marks a fetch call that supplied no URL.
	NoURL bool `json:"noUrl,omitempty"`
	// NoQuery marks a search call that supplied no query.
	NoQuery bool `json:"noQuery,omitempty"`
}

// WebResultKind selects the collapsed result row shape. It is display data
// only; the tool name and the message outcome stay authoritative.
type WebResultKind string

const (
	// WebPage is a single fetched page: title, character count, preview.
	WebResultPage WebResultKind = "page"
	// WebBatch is a multi-URL fetch: success count, total, storage label.
	WebResultBatch WebResultKind = "batch"
	// WebSearch is a search: successful queries, total queries, sources,
	// optional content label and inline preview.
	WebResultSearch WebResultKind = "search"
	// WebContent is a paged retrieval: title, scalar total, optional range.
	WebResultContent WebResultKind = "content"
)

// WebResult is the bounded result display for one web tool result.
type WebResult struct {
	// Kind selects the result row shape. An empty kind is treated as
	// WebPage for records written before the kind field existed.
	Kind WebResultKind `json:"kind,omitempty"`
	// Title is the producer title or a fixed fallback such as "Untitled".
	Title string `json:"title,omitempty"`
	// Chars is the displayed character count. HasChars distinguishes a real
	// zero from a result that has no defined count.
	Chars    int64 `json:"chars,omitempty"`
	HasChars bool  `json:"charsKnown,omitempty"`
	// Truncated reports source or output truncation, not preview clipping.
	Truncated bool `json:"truncated,omitempty"`
	// Preview holds at most WebMaxPreviewLines single-line rows, with at most
	// WebMaxPreviewUnits UTF-16 units in total.
	Preview []string `json:"preview,omitempty"`
	// Expanded holds extra rows shown only when the card is expanded, such as
	// batch page snippets or the returned retrieval slice.
	Expanded []string `json:"expanded,omitempty"`
	// DurationMs is the measured single-fetch duration in milliseconds. It is
	// set only for a call that actually executed.
	DurationMs  int64 `json:"durationMs,omitempty"`
	HasDuration bool  `json:"durationKnown,omitempty"`
	// OK and Total are the successful and expected attempt counts for a batch
	// fetch or a multi-query search.
	OK        int  `json:"ok,omitempty"`
	Total     int  `json:"total,omitempty"`
	HasCounts bool `json:"countsKnown,omitempty"`
	// Sources is the post-deduplication search result count.
	Sources    int  `json:"sources,omitempty"`
	HasSources bool `json:"sourcesKnown,omitempty"`
	// Stored is the settled fact that content was retained and the request
	// advertised get_search_content. It is never recomputed on redraw.
	Stored bool `json:"stored,omitempty"`
	// RangeStart and RangeEnd are the actual half-open scalar range returned
	// by a retrieval. RangeEnd is exclusive and may equal Chars.
	RangeStart int64 `json:"rangeStart,omitempty"`
	RangeEnd   int64 `json:"rangeEnd,omitempty"`
	HasRange   bool  `json:"rangeKnown,omitempty"`
}

// WebProgress is a bounded, transient progress snapshot for one running web
// tool. It carries completed attempts, never assumed successes or byte
// estimates, and is never written to the session.
type WebProgress struct {
	// ToolCallID identifies the call that owns this snapshot.
	ToolCallID string
	// Phase names the active operation boundary, for example "searching" or
	// "fetching pages".
	Phase string
	// Label is a redacted query or fixed phase detail. It never carries a
	// findText term or raw URL query.
	Label string
	// Completed and Total are attempt counts. Total may be zero when unknown.
	Completed int
	Total     int
}

// WebDisplay joins the call and result halves of one card. Result is nil until
// the call settles or while a pending card is shown.
type WebDisplay struct {
	Call   WebCall
	Result *WebResult
}

// WebMeta is the `psiWeb` message-envelope payload. An assistant entry maps
// retained call IDs to call data; a tool-result entry carries one result for
// its own call ID. The model-facing message never contains this object.
type WebMeta struct {
	Calls  map[string]WebCall `json:"calls,omitempty"`
	CallID string             `json:"callId,omitempty"`
	Result *WebResult         `json:"result,omitempty"`
}
