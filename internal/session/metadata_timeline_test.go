package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactionReplayDoesNotLoadSummaryUnlessHistoryIsExplicit(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := New(OpenOptions{Cwd: cwd, SessionDir: filepath.Join(root, "sessions"), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "retain this"}})
	if err != nil {
		t.Fatal(err)
	}
	const hidden = "large-compaction-summary-canary"
	summary := strings.Repeat(hidden, 2_000)
	compactionID, err := store.AppendCompaction(summary, userID, 28_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	lazy, err := OpenFile(store.File(), cwd, true)
	if err != nil {
		t.Fatal(err)
	}
	loaded := false
	lazy.LoadHook = func(meta RecordMeta) {
		if meta.ID == compactionID {
			loaded = true
		}
	}
	var current []TranscriptItem
	complete, err := lazy.Timeline(false, 0, func(item TranscriptItem) bool { current = append(current, item); return true })
	if err != nil || !complete {
		t.Fatalf("current timeline complete=%v err=%v", complete, err)
	}
	if loaded {
		t.Fatal("default replay loaded the full compaction summary")
	}
	for _, item := range current {
		if strings.Contains(item.Text, hidden) {
			t.Fatal("default replay exposed the full summary")
		}
	}
	var history []TranscriptItem
	complete, err = lazy.Timeline(true, 0, func(item TranscriptItem) bool { history = append(history, item); return true })
	if err != nil || !complete || !loaded {
		t.Fatalf("explicit history did not load summary: complete=%v loaded=%v err=%v", complete, loaded, err)
	}
	found := false
	for _, item := range history {
		if item.Kind == KindCompaction && strings.Contains(item.Text, hidden) {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit history omitted the stored compaction summary")
	}
}

func TestCompactionTimelineKeepsDefaultMarkerCompactAndHistoryExplicit(t *testing.T) {
	entry := &Entry{Kind: "compaction", Compaction: &CompactionEntry{Summary: "private synthetic summary details", TokensBefore: 28_000}}
	current := timelineEntry(entry, nil, nil, false)
	history := timelineEntry(entry, nil, nil, true)
	if len(current) != 1 || current[0].Text != "Context compacted · ~28000 tokens before" {
		t.Fatalf("default transcript marker=%+v", current)
	}
	if strings.Contains(current[0].Text, "private synthetic summary") {
		t.Fatal("default transcript exposed the full compaction summary")
	}
	if len(history) != 1 || history[0].Text != entry.Compaction.Summary || !history[0].Historical {
		t.Fatalf("explicit history lost compaction summary: %+v", history)
	}
}
