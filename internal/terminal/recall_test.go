package terminal

// Phase G regression for PLAN3 F11: every draft-installation path obeys the
// editor limit. Recall admission is whole-or-skip (never silent truncation);
// the saved draft and cursor survive rejected recall.

import (
	"strings"
	"testing"
)

func overBudget(n int) string { return strings.Repeat("r", 256*1024+n) }

func TestF11SetHistoryAdmitsWhole(t *testing.T) {
	e := NewEditor()
	e.Insert("draft-keep")
	e.SetHistory([]string{"ok", overBudget(1), "also-ok", overBudget(100)})
	if len(e.History()) != 2 {
		t.Fatalf("history = %d entries, want 2 admitted", len(e.History()))
	}
	if !e.rejectedRecall {
		t.Fatal("no bounded notice for skipped entries")
	}
	// Browsing installs only admitted entries; nothing submits truncated text.
	e.PrevHistory()
	if e.Text() != "also-ok" {
		t.Fatalf("installed %q", e.Text())
	}
	e.PrevHistory()
	if e.Text() != "ok" {
		t.Fatalf("installed %q", e.Text())
	}
	if got := e.Submit(); got != "ok" {
		t.Fatalf("submit cut text: %d bytes", len(got))
	}
}

func TestF11ExactLimitAndBoundaries(t *testing.T) {
	atLimit := strings.Repeat("x", 256*1024)
	over := atLimit + "y"
	norm := "a\r\nb\rc" // normalizes to 4 bytes, well within budget
	e := NewEditor()
	e.SetHistory([]string{atLimit, over, norm})
	if len(e.History()) != 2 {
		t.Fatalf("exact-limit entry rejected or over admitted: %d", len(e.History()))
	}
	e.PrevHistory()
	if e.Text() != "a\nb\nc" {
		t.Fatalf("normalization mismatch: %q", e.Text())
	}
	e.PrevHistory()
	if len(e.Text()) != 256*1024 {
		t.Fatalf("exact-limit text altered: %d", len(e.Text()))
	}
}

func TestF11MultibyteAndGrapheme(t *testing.T) {
	// Multibyte text near the budget: admission counts normalized bytes and
	// never splits a grapheme (budget boundary, not rune boundary).
	base := strings.Repeat("é", 100) // 200 bytes
	big := strings.Repeat("é", 200*1024)
	e := NewEditor()
	e.SetHistory([]string{base, big})
	if len(e.History()) != 1 || e.History()[0] != base {
		t.Fatal("multibyte admission wrong")
	}
	e.PrevHistory()
	if e.Text() != base {
		t.Fatal("multibyte recall altered")
	}
}

func TestF11DraftRestoredOnSkip(t *testing.T) {
	e := NewEditor()
	e.Insert("unsent draft")
	e.SetHistory([]string{"short"})
	// Simulate an oversized entry arriving after admission (import path):
	// browsing must not install it and must keep the saved draft.
	e.history = append(e.history, overBudget(5))
	e.rejectedRecall = false
	e.PrevHistory() // lands on oversized -> skips to "short"
	if e.Text() != "short" {
		t.Fatalf("skip landed wrong: %q", e.Text())
	}
	if !e.rejectedRecall {
		t.Fatal("skip without notice")
	}
	// The saved draft survives for the return trip.
	e.NextHistory()
	e.NextHistory()
	if e.Text() != "unsent draft" {
		t.Fatalf("draft lost on skip: %q", e.Text())
	}
	// Per-entry vs total retention: session keeps 200 entries; the editor
	// budget applies per entry, not to the retained total.
	many := make([]string, 200)
	for i := range many {
		many[i] = "entry"
	}
	e.SetHistory(many)
	if len(e.History()) != 200 {
		t.Fatalf("retention = %d, want 200", len(e.History()))
	}
}
