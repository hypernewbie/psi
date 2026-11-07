package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactionReplayNeverExposesTheStoredSummary(t *testing.T) {
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
	if _, err := store.AppendCompaction(summary, userID, 28_000, nil); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFile(store.File(), cwd, true)
	if err != nil {
		t.Fatal(err)
	}
	var current []TranscriptItem
	complete, err := reopened.Timeline(false, 0, func(item TranscriptItem) bool { current = append(current, item); return true })
	if err != nil || !complete {
		t.Fatalf("current timeline complete=%v err=%v", complete, err)
	}
	for _, item := range current {
		if strings.Contains(item.Text, hidden) {
			t.Fatal("default replay exposed the full summary")
		}
	}
	// Pi renders every compaction entry collapsed; the summary only appears
	// through the interactive expand, which psi does not implement.
	for _, item := range current {
		if item.Kind == KindCompaction && item.Text != CompactionCollapsedText(28_000) {
			t.Fatalf("compaction marker=%q", item.Text)
		}
	}
}

func TestCompactionTimelineKeepsEveryMarkerCollapsed(t *testing.T) {
	entry := &Entry{Kind: "compaction", Compaction: &CompactionEntry{Summary: "private synthetic summary details", TokensBefore: 28_000}}
	current := timelineEntry(entry, nil, nil, false)
	history := timelineEntry(entry, nil, nil, true)
	want := "Compacted from 28,000 tokens (Ctrl+O to expand)"
	if len(current) != 1 || current[0].Text != want {
		t.Fatalf("default transcript marker=%+v", current)
	}
	if strings.Contains(current[0].Text, "private synthetic summary") {
		t.Fatal("default transcript exposed the full compaction summary")
	}
	if len(history) != 1 || history[0].Text != want {
		t.Fatalf("historical transcript marker=%+v", history)
	}
}
