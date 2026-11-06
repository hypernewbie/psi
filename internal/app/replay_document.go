package app

import (
	"fmt"

	"psi/internal/session"
	"psi/internal/terminal"
)

func transcriptDocumentBlock(it session.TranscriptItem) terminal.DocumentBlock {
	block := terminal.DocumentBlock{Text: it.Text, ToolName: it.ToolName, ToolState: it.ToolState,
		Summary: it.ToolSummary, Historical: it.Historical, CallID: it.SourceID}
	switch it.Kind {
	case session.KindUser:
		block.Kind = terminal.DocumentUser
	case session.KindAssistant:
		block.Kind = terminal.DocumentAssistant
	case session.KindThinking:
		block.Kind = terminal.DocumentThinking
	case session.KindTool:
		block.Kind = terminal.DocumentTool
		if it.Note != "" && !block.Summary.Available {
			block.Summary.Note = it.Note
		}
	case session.KindCompaction:
		block.Kind = terminal.DocumentMetadata
		block.Label = "[compaction] "
		if it.Historical {
			block.Label = "[historical compaction] "
		}
	case session.KindBranchSummary:
		block.Kind, block.Label = terminal.DocumentMetadata, "[branch] "
	case session.KindSystemMarker:
		block.Kind, block.Label = terminal.DocumentMetadata, "[session] "
	case session.KindHistoryBoundary:
		block.Kind = terminal.DocumentBoundary
	case session.KindNotice:
		block.Kind = terminal.DocumentRaw
	default:
		block.Kind = terminal.DocumentRaw
	}
	return block
}

// replayActivationDocument retains only occurrences that reach the event-loop
// callback. A resize rebuild cannot expose entries from an interrupted walk.
func replayActivationDocument(add func(terminal.DocumentBlock) bool, act *Activation, isNew bool, activeID string) bool {
	if act.Store.Header().ID == activeID && !isNew {
		return true
	}
	if !add(terminal.DocumentBlock{Kind: terminal.DocumentBoundary, Text: "session " + terminal.Sanitize(act.Store.Header().ID)}) {
		return false
	}
	if act.Offline && act.OfflineErr != nil {
		text := "Offline: " + act.OfflineErr.Error() + " (history readable, continuation blocked)"
		if !add(terminal.DocumentBlock{Kind: terminal.DocumentNotice, Text: text, NoticeKind: terminal.NoticeWarning}) ||
			!add(terminal.DocumentBlock{Kind: terminal.DocumentGap}) {
			return false
		}
	}
	visible := map[string]bool{}
	toolPending := false
	complete, err := act.Store.Timeline(false, 0, func(it session.TranscriptItem) bool {
		visible[it.SourceID] = true
		if it.Kind != session.KindTool && toolPending {
			toolPending = false
		}
		if it.Kind == session.KindTool {
			toolPending = true
		}
		return add(transcriptDocumentBlock(it))
	})
	if err != nil {
		add(terminal.DocumentBlock{Kind: terminal.DocumentNotice, Text: "History unavailable: " + err.Error(), NoticeKind: terminal.NoticeError})
		return false
	}
	if !complete {
		return false
	}
	if toolPending && !add(terminal.DocumentBlock{Kind: terminal.DocumentGap}) {
		return false
	}
	omitted := 0
	for _, entry := range act.Path {
		if entry.Kind == "message" && !visible[entry.ID()] && (entry.Message.Message.User != nil || entry.Message.Message.Assistant != nil) {
			omitted++
		}
	}
	if omitted > 0 {
		return add(terminal.DocumentBlock{Kind: terminal.DocumentRaw,
			Text: fmt.Sprintf("(%d earlier messages summarized. Use /history all to view.)\r\n", omitted)})
	}
	return true
}

func renderHistoryDocument(store *session.Store, arg string, add func(terminal.DocumentBlock) bool) (bool, error) {
	all, n, err := historySelection(arg)
	if err != nil {
		return false, err
	}
	if !all && !add(terminal.DocumentBlock{Kind: terminal.DocumentBoundary, Text: "Conversation history (display only, not model input)"}) {
		return false, nil
	}
	toolPending := false
	complete, err := store.Timeline(all, n, func(it session.TranscriptItem) bool {
		if it.Kind != session.KindTool && toolPending {
			toolPending = false
		}
		if it.Kind == session.KindTool {
			toolPending = true
		}
		return add(transcriptDocumentBlock(it))
	})
	if err != nil || !complete {
		return complete, err
	}
	if toolPending && !add(terminal.DocumentBlock{Kind: terminal.DocumentGap}) {
		return false, nil
	}
	return true, nil
}
