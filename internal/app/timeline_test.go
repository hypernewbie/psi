package app

import (
	"psi/internal/session"
	"testing"
)

func activationTimeline(t *testing.T, act *Activation) []session.TranscriptItem {
	t.Helper()
	var items []session.TranscriptItem
	_, err := act.Store.Timeline(false, 0, func(it session.TranscriptItem) bool { items = append(items, it); return true })
	if err != nil {
		t.Fatal(err)
	}
	return items
}
