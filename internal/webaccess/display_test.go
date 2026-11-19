package webaccess

import (
	"context"
	"strings"
	"testing"
	"time"

	"psi/internal/event"
)

func TestWebFetchCallDisplay(t *testing.T) {
	client := New(Config{FetchModes: []string{"readable", "raw"}})
	cases := []struct {
		name string
		args map[string]any
		want event.WebCall
	}{
		{
			name: "single URL drops userinfo query and fragment",
			args: map[string]any{"url": "https://user:pass@example.com/a/b?token=SECRET#frag"},
			want: event.WebCall{Title: "fetch https://example.com/a/b"},
		},
		{
			name: "one-item urls array takes the single branch",
			args: map[string]any{"urls": []any{"https://example.com/page"}},
			want: event.WebCall{Title: "fetch https://example.com/page"},
		},
		{
			name: "explicit raw mode",
			args: map[string]any{"url": "https://example.com/page", "mode": "raw"},
			want: event.WebCall{Title: "fetch https://example.com/page", Mode: "raw"},
		},
		{
			name: "explicit readable mode is not printed",
			args: map[string]any{"url": "https://example.com/page", "mode": "readable"},
			want: event.WebCall{Title: "fetch https://example.com/page"},
		},
		{
			name: "both url and urls is invalid",
			args: map[string]any{"url": "https://example.com/a", "urls": []any{"https://example.com/b"}},
			want: event.WebCall{Title: "fetch", Invalid: true},
		},
		{
			name: "missing url",
			args: map[string]any{"url": "   "},
			want: event.WebCall{Title: "fetch", NoURL: true},
		},
		{
			name: "unparseable url keeps a fixed label",
			args: map[string]any{"url": "javascript:alert(1)"},
			want: event.WebCall{Title: "fetch", Invalid: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := client.DisplayCall(ToolFetch, tc.args)
			if !ok {
				t.Fatal("fetch is not reported as a web tool")
			}
			if got.Title != tc.want.Title || got.Mode != tc.want.Mode || got.Invalid != tc.want.Invalid || got.NoURL != tc.want.NoURL {
				t.Fatalf("display = %+v, want %+v", got, tc.want)
			}
			if strings.Contains(got.Title, "SECRET") || strings.Contains(got.Title, "user:pass") || strings.Contains(got.Title, "frag") {
				t.Fatalf("call display retained sensitive URL parts: %q", got.Title)
			}
		})
	}
	if _, ok := client.DisplayCall("read", map[string]any{"path": "a.txt"}); ok {
		t.Fatal("non-web tool reported as a card")
	}
}

func TestWebPageResultDisplayBounds(t *testing.T) {
	client := New(Config{FetchModes: []string{"readable", "raw"}})
	text := "FIRST LINE\r\n" + strings.Repeat("😀", 600) + "\n\x1b[31mTAIL\n\nTHIRD"
	result := client.pageResultDisplay(pageData{Title: " Title\nWith Break ", Text: text}, true, 350*time.Millisecond)
	if result == nil {
		t.Fatal("missing result display")
	}
	if result.Title != "Title With Break" {
		t.Fatalf("title = %q", result.Title)
	}
	if !result.HasChars || result.Chars != int64(utf16Units(text)) {
		t.Fatalf("chars = %d, want %d", result.Chars, utf16Units(text))
	}
	if !result.Truncated {
		t.Fatal("truncation flag lost")
	}
	if len(result.Preview) == 0 || len(result.Preview) > event.WebMaxPreviewLines {
		t.Fatalf("preview lines = %d", len(result.Preview))
	}
	units := 0
	for _, line := range result.Preview {
		if strings.ContainsAny(line, "\x1b\n\r") {
			t.Fatalf("preview retained a control character: %q", line)
		}
		units += utf16Units(line)
	}
	if units > event.WebMaxPreviewUnits {
		t.Fatalf("preview units = %d", units)
	}
	if empty := client.pageResultDisplay(pageData{Text: "body"}, false, 0); empty.Title != "Untitled" {
		t.Fatalf("title fallback = %q", empty.Title)
	}
}

func TestWebSearchCallDisplay(t *testing.T) {
	client := New(Config{})
	cases := []struct {
		name string
		args map[string]any
		want event.WebCall
	}{
		{name: "single query", args: map[string]any{"query": "golang"}, want: event.WebCall{Title: `search "golang"`}},
		{
			name: "multiple queries",
			args: map[string]any{"queries": []any{"first", "second"}},
			want: event.WebCall{Title: "search 2 queries", Entries: []string{`"first"`, `"second"`}},
		},
		{name: "no query", args: map[string]any{}, want: event.WebCall{Title: "search", NoQuery: true}},
		{name: "both query and queries", args: map[string]any{"query": "a", "queries": []any{"b"}}, want: event.WebCall{Title: "search", Invalid: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := client.DisplayCall(ToolSearch, tc.args)
			if !ok {
				t.Fatal("search is not reported as a web tool")
			}
			if got.Title != tc.want.Title || got.NoQuery != tc.want.NoQuery || got.Invalid != tc.want.Invalid || len(got.Entries) != len(tc.want.Entries) {
				t.Fatalf("display = %+v, want %+v", got, tc.want)
			}
			for i := range got.Entries {
				if got.Entries[i] != tc.want.Entries[i] {
					t.Fatalf("entry %d = %q, want %q", i, got.Entries[i], tc.want.Entries[i])
				}
			}
		})
	}
}

func TestWebContentCallDisplay(t *testing.T) {
	client := New(Config{})
	longPath := strings.Repeat("p", 40)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "url target", args: map[string]any{"responseId": "r1", "url": "https://example.com/" + longPath}, want: "get_content https://example.com/ppppppp..."},
		{name: "index target", args: map[string]any{"responseId": "r1", "urlIndex": 2}, want: "get_content urlIndex=2"},
		{name: "response id fallback", args: map[string]any{"responseId": "abcdefghijkl"}, want: "get_content abcdefgh"},
		{name: "offset without a page target", args: map[string]any{"responseId": "r1", "offset": 40}, want: "get_content r1 offset=40"},
		{name: "find term is not printed", args: map[string]any{"responseId": "r1", "findText": "needle"}, want: "get_content r1 · find 1"},
		{name: "invalid arguments", args: map[string]any{"responseId": "r1", "urlIndex": -1}, want: "get_content"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := client.DisplayCall(ToolContent, tc.args)
			if !ok {
				t.Fatal("retrieval is not reported as a web tool")
			}
			if got.Title != tc.want {
				t.Fatalf("display = %q, want %q", got.Title, tc.want)
			}
			if strings.Contains(got.Title, "needle") {
				t.Fatalf("findText reached the display: %q", got.Title)
			}
			if tc.name == "invalid arguments" && !got.Invalid {
				t.Fatalf("invalid arguments not marked: %+v", got)
			}
		})
	}
}

func TestWebResultDisplayFacts(t *testing.T) {
	client := New(Config{FetchModes: []string{"readable", "raw"}})
	plain := context.Background()
	advertised := WithRetrievalDisplay(plain, true)

	if got := client.batchResultDisplay(plain, 5, 6, "id", nil, nil); got.Kind != event.WebResultBatch || got.OK != 5 || got.Total != 6 || got.Stored {
		t.Fatalf("batch without retrieval advertisement = %+v", got)
	}
	if got := client.batchResultDisplay(advertised, 5, 6, "id", nil, nil); !got.Stored {
		t.Fatalf("batch with stored content lost its label: %+v", got)
	}
	if got := client.batchResultDisplay(advertised, 5, 6, "", nil, nil); got.Stored {
		t.Fatalf("batch without a store id claimed storage: %+v", got)
	}

	results := []storedResult{{URL: "https://example.com/1"}, {URL: "https://example.com/2", Content: "\n  first line  \nsecond"}}
	got := client.searchResultDisplay(advertised, 2, 3, 1, "id", results)
	if got.Kind != event.WebResultSearch || got.OK != 2 || got.Total != 3 || got.Sources != 1 || !got.Stored {
		t.Fatalf("search facts = %+v", got)
	}
	if len(got.Preview) != 2 || got.Preview[0] != "first line" || got.Preview[1] != "second" {
		t.Fatalf("search preview = %#v", got.Preview)
	}
	if got := client.searchResultDisplay(advertised, 2, 3, 1, "id", []storedResult{{URL: "https://example.com"}}); got.Stored {
		t.Fatalf("search without inline content claimed readiness: %+v", got)
	}
	if got := client.searchResultDisplay(plain, 2, 3, 1, "id", results); got.Stored {
		t.Fatalf("search without a retrieval advertisement claimed readiness: %+v", got)
	}
	if got := client.searchResultDisplay(advertised, 1, 1, 0, "id", results); len(got.Preview) != 2 {
		t.Fatalf("search preview lost: %+v", got)
	}
	// A blank first inline page is not eligible; the next result supplies the
	// preview instead of darkening the card.
	blankFirst := []storedResult{{URL: "https://example.com/blank", Content: "\n \n"}, {URL: "https://example.com/hit", Content: "second page line"}}
	if got := client.searchResultDisplay(advertised, 1, 1, 2, "id", blankFirst); len(got.Preview) != 1 || got.Preview[0] != "second page line" {
		t.Fatalf("search preview skipped eligible content: %+v", got.Preview)
	}

	content := contentResultDisplay("", 12, 2, 5, false, nil)
	if content.Title != "Content" || content.Chars != 12 || !content.HasChars || !content.HasRange || content.RangeStart != 2 || content.RangeEnd != 5 {
		t.Fatalf("retrieval display = %+v", content)
	}
	if whole := contentResultDisplay("Page", 12, 0, 12, false, nil); whole.HasRange {
		t.Fatalf("whole content invented a range: %+v", whole)
	}
	if tail := contentResultDisplay("Page", 12, 10, 12, false, nil); !tail.HasRange || tail.RangeEnd != 12 {
		t.Fatalf("tail range = %+v", tail)
	}
	if empty := contentResultDisplay("Page", 12, 12, 12, false, nil); !empty.HasRange || empty.RangeStart != 12 || empty.RangeEnd != 12 {
		t.Fatalf("empty end-of-content range = %+v", empty)
	}
}
