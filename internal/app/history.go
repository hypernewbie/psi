package app

import (
	"fmt"
	"strconv"
	"strings"

	"psi/internal/session"
	"psi/internal/terminal"
	pitheme "psi/internal/theme"
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

func renderStoreHistory(store *session.Store, arg string, emit func(string) bool, palette ...*pitheme.Theme) (bool, error) {
	return renderStoreHistoryAtWidth(store, arg, emit, 80, palette...)
}

func renderStoreHistoryAtWidth(store *session.Store, arg string, emit func(string) bool, width int, palette ...*pitheme.Theme) (bool, error) {
	return renderStoreHistoryAtWidthWithDisplay(store, arg, emit, width, terminal.DefaultDisplaySettings(), palette...)
}

func renderStoreHistoryAtWidthWithDisplay(store *session.Store, arg string, emit func(string) bool, width int, display terminal.DisplaySettings, palette ...*pitheme.Theme) (bool, error) {
	return renderStoreHistoryAtWidthWithDisplayAndColor(store, arg, emit, width, display, false, palette...)
}

func renderStoreHistoryAtWidthWithDisplayAndColor(store *session.Store, arg string, emit func(string) bool, width int, display terminal.DisplaySettings, noColor bool, palette ...*pitheme.Theme) (bool, error) {
	all, n, err := historySelection(arg)
	if err != nil {
		return false, err
	}
	if !all && !emit("--- Conversation history (display only, not model input) ---") {
		return false, nil
	}
	var spacing toolBatchSpacing
	complete, err := store.Timeline(all, n, func(it session.TranscriptItem) bool {
		return renderTimelineItems(emit, []session.TranscriptItem{it}, width, display, noColor, &spacing, palette...)
	})
	if err != nil || !complete {
		return complete, err
	}
	return spacing.finish(emit), nil
}
