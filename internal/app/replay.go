package app

import (
	"fmt"
	"strings"

	"psi/internal/session"
	"psi/internal/terminal"
)

func renderTimeline(print func(string), items []session.TranscriptItem) {
	renderTimelineChecked(func(s string) bool { print(s); return true }, items)
}

// The checked path lets the actual event loop stop between bounded output
// batches. Merely splitting a string into batches does not make replay cancellable.
func renderTimelineChecked(emit func(string) bool, items []session.TranscriptItem) bool {
	return renderTimelineAtWidth(emit, items, 80)
}

func renderTimelineAtWidth(emit func(string) bool, items []session.TranscriptItem, width int) bool {
	for _, it := range items {
		var lines []string
		switch it.Kind {
		case session.KindUser, session.KindAssistant:
			lines = terminal.FormatMessage(it.Text, width, false)
		case session.KindTool:
			text := it.Text
			if it.Note != "" {
				text += " (" + it.Note + ")"
			}
			lines = []string{text}
		case session.KindCompaction:
			label := "Context compacted: "
			if it.Historical {
				label = "Historical compaction: "
			}
			lines = []string{label + it.Text, ""}
		case session.KindBranchSummary:
			lines = []string{it.Text, ""}
		case session.KindSystemMarker:
			lines = []string{"[" + it.Text + "]"}
		case session.KindHistoryBoundary:
			lines = []string{"--- " + it.Text + " ---"}
		case session.KindNotice:
			lines = []string{it.Text}
		}
		for _, line := range lines {
			if it.Kind != session.KindUser && it.Kind != session.KindAssistant {
				line = terminal.Sanitize(line)
			}
			if !emit(line) {
				return false
			}
		}
	}
	return true
}

func replayActivation(print func(string), act *Activation, isNew bool, activeID string) {
	replayActivationChecked(func(s string) bool { print(s); return true }, act, isNew, activeID)
}

func replayActivationChecked(emit func(string) bool, act *Activation, isNew bool, activeID string) bool {
	return replayActivationAtWidth(emit, act, isNew, activeID, 80)
}

func replayActivationAtWidth(emit func(string) bool, act *Activation, isNew bool, activeID string, width int) bool {
	if act.Store.Header().ID == activeID && !isNew {
		return true
	}
	if !emit("--- session " + terminal.Sanitize(act.Store.Header().ID) + " ---") {
		return false
	}
	if act.Offline && act.OfflineErr != nil {
		notice := strings.Join(terminal.FormatNotice(terminal.NoticeWarning, "Offline: "+act.OfflineErr.Error()+" (history readable, continuation blocked)", width, false), "\n")
		if !emit(notice) {
			return false
		}
	}
	visible := map[string]bool{}
	complete, err := act.Store.Timeline(false, 0, func(it session.TranscriptItem) bool {
		visible[it.SourceID] = true
		return renderTimelineAtWidth(emit, []session.TranscriptItem{it}, width)
	})
	if err != nil {
		notice := strings.Join(terminal.FormatNotice(terminal.NoticeError, "History unavailable: "+err.Error(), width, false), "\n")
		emit(notice)
		return false
	}
	if !complete {
		return false
	}
	omitted := 0
	for _, e := range act.Path {
		if e.Kind == "message" && !visible[e.ID()] && (e.Message.Message.User != nil || e.Message.Message.Assistant != nil) {
			omitted++
		}
	}
	if omitted > 0 {
		return emit(fmt.Sprintf("(%d earlier messages summarized. Use /history all to view.)", omitted))
	}
	return true
}
