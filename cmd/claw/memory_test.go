package main

import (
	"strings"
	"testing"
)

func TestMemorySearchAndGet(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{
		"MEMORY.md":            "# Facts\n\nThe release train leaves on Friday.\n\nOwner prefers tea.",
		"memory/2026-01-02.md": "Decision: use the small model for summaries.",
		"notes.txt":            "not indexed",
	})
	index, err := loadMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	hits := index.search("release friday", 6, 0)
	if len(hits) != 1 || hits[0].Path != "MEMORY.md" || hits[0].StartLine != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, "Friday") {
		t.Fatalf("snippet = %q", hits[0].Snippet)
	}
	if hits := index.search("missing term", 6, 0); len(hits) != 0 {
		t.Fatalf("unexpected hits = %+v", hits)
	}
	text, err := index.get("memory/2026-01-02.md", 1, 1)
	if err != nil || !strings.Contains(text, "Decision") {
		t.Fatalf("get = %q, %v", text, err)
	}
	if _, err := index.get("../notes.txt", 1, 1); err == nil {
		t.Fatal("expected an unindexed path error")
	}
}

func TestMemorySearchRequiresEveryTerm(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{
		"MEMORY.md":       "alpha beta",
		"memory/other.md": "gamma delta",
	})
	index, err := loadMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hits := index.search("alpha gamma", 6, 0); len(hits) != 0 {
		t.Fatalf("cross-file match = %+v", hits)
	}
	if hits := index.search("Alpha BETA", 6, 0); len(hits) != 1 {
		t.Fatalf("case-insensitive match = %+v", hits)
	}
}
