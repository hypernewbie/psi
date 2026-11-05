package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestEmptySessionMetadata(t *testing.T) {
	store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Flat) != 0 || len(projection.Entries) != 0 {
		t.Fatalf("empty projection = %+v", projection)
	}
	state, err := store.ModelState()
	if err != nil {
		t.Fatal(err)
	}
	if state.HasModel || state.ThinkingLevel != "off" {
		t.Fatalf("empty model state = %+v", state)
	}
	if _, ok := store.LastContextTokens(); ok {
		t.Fatal("empty store returned context usage")
	}
}

func TestProjectionCacheRejectsExternalRewrite(t *testing.T) {
	path := twoMessageFile(t)
	store := openPersisted(t, path)
	if _, err := store.Projection(); err != nil {
		t.Fatal(err)
	}
	rewriteSecondLineSameID(path)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	// The loaded session keeps serving its generation; the changed file
	// takes no new entries.
	if _, err := store.Projection(); err != nil {
		t.Fatalf("projection of the loaded session failed: %v", err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "after"}}); err == nil || !strings.Contains(err.Error(), "changed externally") {
		t.Fatalf("external rewrite accepted: %v", err)
	}
}

func TestProjectionIfCachedLifecycle(t *testing.T) {
	store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	if store.ProjectionIfCached() != nil {
		t.Fatal("new store exposed a projection before it was built")
	}
	first, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Projection(); err != nil {
		t.Fatal(err)
	}
	if store.ProjectionIfCached() == nil {
		t.Fatal("projection was not cached")
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if store.ProjectionIfCached() == nil {
		t.Fatal("append should grow the cached projection")
	}
	if err := store.Branch(first); err != nil {
		t.Fatal(err)
	}
	if store.ProjectionIfCached() != nil {
		t.Fatal("branch did not invalidate the projection")
	}
	if _, err := store.Projection(); err != nil {
		t.Fatal(err)
	}
	if store.ProjectionIfCached() == nil {
		t.Fatal("projection was not rebuilt after branch")
	}
}

func TestProjectionCacheInvalidatesOnCompaction(t *testing.T) {
	store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Flat) != 2 {
		t.Fatalf("initial flat = %d", len(before.Flat))
	}
	if _, err := store.AppendCompaction("summary", userID, 100, nil); err != nil {
		t.Fatal(err)
	}
	if store.ProjectionIfCached() != nil {
		t.Fatal("compaction did not invalidate the projection")
	}
	after, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Flat) != 3 || after.Flat[0].Role != "compactionSummary" {
		t.Fatalf("compaction context = %+v", after.Flat)
	}
	if len(before.Flat) != 2 {
		t.Fatal("compaction mutation changed the old snapshot")
	}
}
