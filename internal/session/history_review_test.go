package session

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHistoryReadKeepsAllParallelToolResults(t *testing.T) {
	store := newHistoryStore(t)
	assistant := historyToolCall("call-a", "read", map[string]any{"path": "a"})
	assistant.Assistant.Content = append(assistant.Assistant.Content, ContentBlock{
		Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: "call-b", Name: "read", Arguments: map[string]any{"path": "b"}},
	})
	callID, err := store.AppendMessage(assistant)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := store.AppendMessage(historyToolResult("call-a", "read", strings.Repeat("a", 4000)))
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := store.AppendMessage(historyToolResult("call-b", "read", strings.Repeat("b", 4000)))
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{callID, firstID, secondID} {
		read := ReadHistory(path, selected, selected, 500)
		if !read.OK || len(read.ReturnedIDs) != 3 {
			t.Fatalf("selected %s: result=%+v", selected, read)
		}
		for _, id := range []string{callID, firstID, secondID} {
			if !strings.Contains(read.Text, "--- entry "+id+" |") {
				t.Errorf("selected %s: missing frame for %s", selected, id)
			}
		}
		if read.EstimatedTokens > 500 {
			t.Fatalf("selected %s: %d estimated tokens exceeds 500", selected, read.EstimatedTokens)
		}
	}
}

func TestHistoryReadRefusesGroupWhoseFramesCannotFit(t *testing.T) {
	store := newHistoryStore(t)
	assistant := historyToolCall("call-0", "read", nil)
	for i := 1; i < 50; i++ {
		assistant.Assistant.Content = append(assistant.Assistant.Content, ContentBlock{
			Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: fmt.Sprintf("call-%d", i), Name: "read"},
		})
	}
	callID, err := store.AppendMessage(assistant)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := store.AppendMessage(historyToolResult(fmt.Sprintf("call-%d", i), "read", "result")); err != nil {
			t.Fatal(err)
		}
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	read := ReadHistory(path, callID, callID, 500)
	if read.OK || len(read.ReturnedIDs) != 0 || !strings.Contains(read.Text, "cannot fit") {
		t.Fatalf("oversized atomic group must not silently omit members: %+v", read)
	}
}

func TestHistoryReadPagesWithinExactBound(t *testing.T) {
	store := newHistoryStore(t)
	var first, last string
	for i := 0; i < 45; i++ {
		id, err := store.AppendMessage(historyUser(fmt.Sprintf("entry-%d %s", i, strings.Repeat("x", 100))))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = id
		}
		last = id
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for start := first; start != ""; {
		page := ReadHistory(path, start, last, 500)
		if !page.OK || page.EstimatedTokens > 500 || len(page.Text) > 2000 {
			t.Fatalf("unbounded history page: %+v", page)
		}
		for _, id := range page.ReturnedIDs {
			if seen[id] {
				t.Fatalf("entry %s repeated across pages", id)
			}
			seen[id] = true
		}
		if page.MoreAvailable && page.NextEntryID == "" {
			t.Fatal("continuation lacks next ID")
		}
		start = page.NextEntryID
	}
	if len(seen) != 45 {
		t.Fatalf("read %d entries, want 45", len(seen))
	}
}

func TestHistorySearchFindsContextEditedMessageWithoutCompaction(t *testing.T) {
	store := newHistoryStore(t)
	id, err := store.AppendMessage(historyUser("hidden edited detail"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendContextEdit(id, []byte("null")); err != nil {
		t.Fatal(err)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(flatText(projection.Flat), "hidden edited detail") {
		t.Fatal("context edit did not hide the original")
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	result := SearchHistory(path, "hidden edited detail", 0)
	if !result.OK || len(result.ReturnedIDs) != 1 || result.ReturnedIDs[0] != id {
		t.Fatalf("raw context-edited history missing: %+v", result)
	}
}

func TestHistorySearchExcludesSiblingBranches(t *testing.T) {
	store := newHistoryStore(t)
	root, err := store.AppendMessage(historyUser("shared"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(historyUser("sibling secret")); err != nil {
		t.Fatal(err)
	}
	if err := store.Branch(root); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(historyUser("active branch")); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	if result := SearchHistory(path, "sibling secret", 0); !result.OK || len(result.ReturnedIDs) != 0 {
		t.Fatalf("sibling branch leaked: %+v", result)
	}
	if result := SearchHistory(path, "active branch", 0); !result.OK || len(result.ReturnedIDs) != 1 {
		t.Fatalf("active branch missing: %+v", result)
	}
}

func TestHistorySearchBoundsEchoedQuery(t *testing.T) {
	store := newHistoryStore(t)
	if _, err := store.AppendMessage(historyUser("short entry")); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	result := SearchHistory(path, strings.Repeat("x", 10_000), 0)
	if !result.OK || len(result.Text) > 600 {
		t.Fatalf("unbounded query echo: %d bytes", len(result.Text))
	}
}

func TestHistorySearchExcerptPreservesUnicodeAndMatch(t *testing.T) {
	store := newHistoryStore(t)
	// The Kelvin sign folds to ASCII k, so lowercasing changes byte offsets.
	text := strings.Repeat("K", 300) + " MATCHME " + strings.Repeat("é", 300)
	if _, err := store.AppendMessage(historyUser(text)); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	result := SearchHistory(path, "matchme", 0)
	if !result.OK || !strings.Contains(result.Text, "MATCHME") || !utf8.ValidString(result.Text) {
		t.Fatalf("search lost or corrupted the match: %q", result.Text)
	}
}

func TestHistoryIndexesOnlyTextFromGenericContentBlocks(t *testing.T) {
	store := newHistoryStore(t)
	message := AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "searchable text"},
		map[string]any{"type": "image", "mimeType": "image/png", "data": "IMAGE_DATA_CANARY"},
	}}}
	if _, err := store.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
	// Reopening decodes interface-valued message content into generic blocks.
	reopened, err := OpenFile(store.File(), store.Cwd(), true)
	if err != nil {
		t.Fatal(err)
	}
	path, err := reopened.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	list := ListHistory(path, 0)
	if !strings.Contains(list.Text, "searchable text") || strings.Contains(list.Text, "IMAGE_DATA_CANARY") {
		t.Fatalf("unexpected list: %q", list.Text)
	}
	result := SearchHistory(path, "IMAGE_DATA_CANARY", 0)
	if !result.OK || len(result.ReturnedIDs) != 0 {
		t.Fatalf("image data was indexed: %+v", result)
	}
}
