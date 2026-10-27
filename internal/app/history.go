package app

import (
	"fmt"
	"strconv"
	"strings"

	"psi/internal/session"
)

func historySelection(arg string) (bool, int, error) {
	arg = strings.TrimSpace(arg)
	if arg == "all" {
		return true, 0, nil
	}
	if arg == "" {
		arg = "20"
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n <= 0 {
		return false, 0, fmt.Errorf("use /history [N|all]")
	}
	return false, n, nil
}

func historyItems(path []*session.Entry, arg string) ([]session.TranscriptItem, error) {
	all, n, err := historySelection(arg)
	if err != nil {
		return nil, err
	}
	if all {
		return session.AllHistoryView(path), nil
	}
	items := session.CurrentView(path)
	start, count := 0, 0
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == session.KindUser {
			count++
			if count == n {
				start = i
				break
			}
		}
	}
	out := []session.TranscriptItem{{Kind: session.KindHistoryBoundary, Text: "Conversation history (display only, not model input)"}}
	return append(out, items[start:]...), nil
}

func renderStoreHistory(store *session.Store, arg string, emit func(string) bool) (bool, error) {
	all, n, err := historySelection(arg)
	if err != nil {
		return false, err
	}
	if !all && !emit("--- Conversation history (display only, not model input) ---") {
		return false, nil
	}
	return store.Timeline(all, n, func(it session.TranscriptItem) bool { return renderTimelineChecked(emit, []session.TranscriptItem{it}) })
}
