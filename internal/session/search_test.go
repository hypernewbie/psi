package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func searchStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(OpenOptions{Cwd: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func appendUser(t *testing.T, store *Store, text string) {
	t.Helper()
	if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: text, Timestamp: 1}}); err != nil {
		t.Fatalf("append user: %v", err)
	}
}

func TestSearchVisibleMatchesOnlyVisibleText(t *testing.T) {
	store := searchStore(t)
	appendUser(t, store, "the needle sits in a user turn")
	if _, err := store.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant",
		Content: []ContentBlock{
			{Type: "thinking", Thinking: &ThinkingContent{Type: "thinking", Thinking: "needle in thinking"}},
			{Type: "toolCall", Call: &ToolCall{Type: "toolCall", Name: "read", Arguments: map[string]any{"needle": "argument"}}},
			{Type: "text", Text: &TextContent{Type: "text", Text: "needle in the answer"}},
		},
	}}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	appendUser(t, store, "a later turn without a hit")

	page, err := store.SearchVisible(context.Background(), SearchOptions{Query: "needle"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !page.Complete || page.NextIndex != 3 {
		t.Fatalf("page = %+v, want complete over three entries", page)
	}
	if len(page.Matches) != 2 {
		t.Fatalf("matches = %d, want 2: %+v", len(page.Matches), page.Matches)
	}
	if page.Matches[0].Role != "user" || page.Matches[1].Role != "assistant" {
		t.Fatalf("roles = %q, %q", page.Matches[0].Role, page.Matches[1].Role)
	}
	if strings.Contains(page.Matches[1].Snippet, "thinking") || strings.Contains(page.Matches[1].Snippet, "argument") {
		t.Fatalf("hidden data leaked into snippet %q", page.Matches[1].Snippet)
	}
}

func TestSearchVisibleFoldsCaseForUnicode(t *testing.T) {
	store := searchStore(t)
	appendUser(t, store, "ÜBER den Wolken")
	page, err := store.SearchVisible(context.Background(), SearchOptions{Query: "über"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Matches) != 1 {
		t.Fatalf("matches = %+v, want one case-folded hit", page.Matches)
	}
}

func TestSearchVisibleTreatsQueryLiterally(t *testing.T) {
	store := searchStore(t)
	appendUser(t, store, "cost is a.b dollars")
	appendUser(t, store, "cost is axb dollars")
	page, err := store.SearchVisible(context.Background(), SearchOptions{Query: "a.b"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Matches) != 1 || !strings.Contains(page.Matches[0].Snippet, "a.b") {
		t.Fatalf("matches = %+v, want the literal dotted line only", page.Matches)
	}
}

func TestSearchVisibleResumesAtStartIndex(t *testing.T) {
	store := searchStore(t)
	for _, text := range []string{"hit one", "miss", "hit two", "miss", "hit three"} {
		appendUser(t, store, text)
	}
	first, err := store.SearchVisible(context.Background(), SearchOptions{Query: "hit", Budget: 5 * time.Second})
	if err != nil || !first.Complete || len(first.Matches) != 3 || first.NextIndex != 5 {
		t.Fatalf("first page = %+v, err %v", first, err)
	}
	// Resuming past an inspected prefix returns only what is left, pinned to
	// the anchor the first page reported.
	later, err := store.SearchVisible(context.Background(), SearchOptions{
		Query: "hit", AnchorLeafID: first.AnchorID, StartIndex: 4, Budget: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(later.Matches) != 1 || later.Matches[0].Snippet != "hit three" {
		t.Fatalf("resumed matches = %+v, want the last hit only", later.Matches)
	}
	if !later.Complete || later.NextIndex != 5 {
		t.Fatalf("resumed page = %+v", later)
	}
	if _, err := store.SearchVisible(context.Background(), SearchOptions{Query: "hit", StartIndex: 99}); !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("start beyond branch err = %v, want unavailable", err)
	}
}

func TestSearchVisibleRejectsForeignAnchor(t *testing.T) {
	store := searchStore(t)
	appendUser(t, store, "first branch")
	root := store.LeafID()
	appendUser(t, store, "second branch")
	page, err := store.SearchVisible(context.Background(), SearchOptions{Query: "branch", AnchorLeafID: root})
	if err != nil {
		t.Fatalf("search at ancestor: %v", err)
	}
	if len(page.Matches) != 1 {
		t.Fatalf("ancestor page = %+v, want only the ancestor turn", page.Matches)
	}
	appendUser(t, store, "third branch")
	if _, err := store.SearchVisible(context.Background(), SearchOptions{Query: "branch", AnchorLeafID: "missing"}); !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("missing anchor err = %v, want unavailable", err)
	}
}

func TestSearchVisibleRejectsCancelledContext(t *testing.T) {
	store := searchStore(t)
	appendUser(t, store, "slow needle")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.SearchVisible(ctx, SearchOptions{Query: "needle"}); !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("cancelled err = %v, want unavailable", err)
	}
	// An exhausted budget answers unavailable as a whole, never a partial
	// page that looks complete.
	if _, err := store.SearchVisible(context.Background(), SearchOptions{Query: "needle", Budget: time.Nanosecond}); !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("budget err = %v, want unavailable", err)
	}
}

func TestSnippetAroundStaysValidUTF8(t *testing.T) {
	text := strings.Repeat("é", SearchMaxSnippetBytes)
	snippet, ok := snippetAround(text, len(text)/2, len(text)/2+1)
	if !ok {
		t.Fatal("snippet rejected")
	}
	if len(snippet) > SearchMaxSnippetBytes {
		t.Fatalf("snippet = %d bytes, want <= %d", len(snippet), SearchMaxSnippetBytes)
	}
	if !strings.HasPrefix(snippet, "é") || !strings.HasSuffix(snippet, "é") {
		t.Fatal("snippet cut a rune")
	}
}
