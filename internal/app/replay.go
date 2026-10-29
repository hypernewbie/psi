package app

import (
	"fmt"
	"strings"

	"psi/internal/session"
	"psi/internal/terminal"
	pitheme "psi/internal/theme"
)

func renderTimeline(print func(string), items []session.TranscriptItem) {
	renderTimelineChecked(func(s string) bool { print(s); return true }, items)
}

// The checked path lets the actual event loop stop between bounded output
// batches. Merely splitting a string into batches does not make replay cancellable.
func renderTimelineChecked(emit func(string) bool, items []session.TranscriptItem) bool {
	return renderTimelineAtWidth(emit, items, 80)
}

func renderTimelineAtWidth(emit func(string) bool, items []session.TranscriptItem, width int, palette ...*pitheme.Theme) bool {
	return renderTimelineAtWidthWithDisplay(emit, items, width, terminal.DefaultDisplaySettings(), palette...)
}

func renderTimelineAtWidthWithDisplay(emit func(string) bool, items []session.TranscriptItem, width int, display terminal.DisplaySettings, palette ...*pitheme.Theme) bool {
	return renderTimelineAtWidthWithDisplayAndColor(emit, items, width, display, false, palette...)
}

func renderTimelineAtWidthWithDisplayAndColor(emit func(string) bool, items []session.TranscriptItem, width int, display terminal.DisplaySettings, noColor bool, palette ...*pitheme.Theme) bool {
	for _, it := range items {
		var lines []string
		switch it.Kind {
		case session.KindUser:
			lines = terminal.FormatUserMessageWithDisplay(it.Text, width, noColor, display, palette...)
		case session.KindAssistant:
			lines = terminal.FormatAssistantMessageWithDisplay(it.Text, width, noColor, display, palette...)
		case session.KindTool:
			name, state := it.ToolName, it.ToolState
			if name == "" {
				name, state, _ = strings.Cut(it.Text, ": ")
			}
			summary := it.ToolSummary
			if summary.Kind == "" {
				summary.Kind = name
			}
			if it.Note != "" && !summary.Available {
				summary.Note = it.Note
			}
			lines = []string{terminal.FormatToolRowWithDisplay(summary, name, state, width, noColor, display, palette...)}
		case session.KindCompaction:
			label := "[compaction] "
			if it.Historical {
				label = "[historical compaction] "
			}
			lines = terminal.FormatMetadataMessage(label, it.Text, width, noColor, display, palette...)
		case session.KindBranchSummary:
			lines = terminal.FormatMetadataMessage("[branch] ", it.Text, width, noColor, display, palette...)
		case session.KindSystemMarker:
			lines = terminal.FormatMetadataMessage("[session] ", it.Text, width, noColor, display, palette...)
		case session.KindHistoryBoundary:
			lines = []string{"--- " + terminal.Sanitize(it.Text) + " ---"}
		case session.KindNotice:
			lines = []string{terminal.Sanitize(it.Text)}
		}
		for _, line := range lines {
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

func replayActivationAtWidth(emit func(string) bool, act *Activation, isNew bool, activeID string, width int, palette ...*pitheme.Theme) bool {
	return replayActivationAtWidthWithDisplay(emit, act, isNew, activeID, width, terminal.DefaultDisplaySettings(), palette...)
}

func replayActivationAtWidthWithDisplay(emit func(string) bool, act *Activation, isNew bool, activeID string, width int, display terminal.DisplaySettings, palette ...*pitheme.Theme) bool {
	return replayActivationAtWidthWithDisplayAndColor(emit, act, isNew, activeID, width, display, false, palette...)
}

func replayActivationAtWidthWithDisplayAndColor(emit func(string) bool, act *Activation, isNew bool, activeID string, width int, display terminal.DisplaySettings, noColor bool, palette ...*pitheme.Theme) bool {
	if act.Store.Header().ID == activeID && !isNew {
		return true
	}
	if !emit("--- session " + terminal.Sanitize(act.Store.Header().ID) + " ---") {
		return false
	}
	if act.Offline && act.OfflineErr != nil {
		notice := strings.Join(terminal.FormatNotice(terminal.NoticeWarning, "Offline: "+act.OfflineErr.Error()+" (history readable, continuation blocked)", width, noColor, palette...), "\n")
		if !emit(notice) {
			return false
		}
	}
	visible := map[string]bool{}
	complete, err := act.Store.Timeline(false, 0, func(it session.TranscriptItem) bool {
		visible[it.SourceID] = true
		return renderTimelineAtWidthWithDisplayAndColor(emit, []session.TranscriptItem{it}, width, display, noColor, palette...)
	})
	if err != nil {
		notice := strings.Join(terminal.FormatNotice(terminal.NoticeError, "History unavailable: "+err.Error(), width, noColor, palette...), "\n")
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
