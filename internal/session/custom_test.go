package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCustomEntriesPersistStateWithoutEnteringModelContext(t *testing.T) {
	store, err := New(OpenOptions{Cwd: t.TempDir(), AgentDir: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendCustomEntry("psi.loop.state", map[string]any{"active": true}); err != nil {
		t.Fatal(err)
	}
	entries := store.Entries()
	if len(entries) != 1 || entries[0].Custom == nil || entries[0].Custom.CustomType != "psi.loop.state" {
		t.Fatalf("custom state entry = %#v", entries)
	}
	var state map[string]any
	if err := json.Unmarshal(entries[0].Custom.Data, &state); err != nil || state["active"] != true {
		t.Fatalf("custom state data = %s, err=%v", entries[0].Custom.Data, err)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Flat) != 0 {
		t.Fatalf("state-only custom entry entered model context: %#v", projection.Flat)
	}
}

func TestHiddenCustomMessageEntersModelContextButNotTimeline(t *testing.T) {
	store, err := New(OpenOptions{Cwd: t.TempDir(), AgentDir: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendCustomMessage("psi.loop.nudge", "continue working", false, nil); err != nil {
		t.Fatal(err)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	llm := ConvertToLLM(projection.Flat)
	if len(llm) != 1 || llm[0].Role != "user" || llm[0].User == nil {
		t.Fatalf("hidden custom message model projection = %#v", llm)
	}
	blocks, err := UserBlocks(llm[0].User.Content)
	if err != nil || len(blocks) != 1 || blocks[0].Text == nil || blocks[0].Text.Text != "continue working" {
		t.Fatalf("hidden custom message content = %#v, err=%v", blocks, err)
	}
	var items []TranscriptItem
	if _, err := store.Timeline(true, 0, func(item TranscriptItem) bool {
		items = append(items, item)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if strings.Contains(item.Text, "continue working") {
			t.Fatalf("hidden custom message appeared in timeline: %#v", items)
		}
	}
}

func TestCustomEntriesRequireType(t *testing.T) {
	store, err := New(OpenOptions{Cwd: t.TempDir(), AgentDir: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendCustomEntry(" ", nil); err == nil {
		t.Fatal("accepted empty custom entry type")
	}
	if _, err := store.AppendCustomMessage("", "text", false, nil); err == nil {
		t.Fatal("accepted empty custom message type")
	}
}
