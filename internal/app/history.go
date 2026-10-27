package app

import (
	"fmt"
	"strconv"
	"strings"

	"psi/internal/session"
)

func historyItems(path []*session.Entry, arg string) ([]session.TranscriptItem, error) {
	arg = strings.TrimSpace(arg)
	if arg == "all" {
		return session.AllHistoryView(path), nil
	}
	if arg == "" {
		arg = "20"
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("use /history [N|all]")
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
