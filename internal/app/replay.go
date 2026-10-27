package app

import (
	"fmt"

	"psi/internal/session"
	"psi/internal/terminal"
)

// renderTimeline appends the current-view transcript to native scrollback (P2-035,036,037,038).
// It uses one display projector for startup, --continue, --session, --resume, /resume, /history.
// Replay never calls provider/tools, never mutates the session, and shows settled states.
func renderTimeline(print func(string), items []session.TranscriptItem) {
	// Blank line between logical messages, not between fragments (3.1).
	for _, it := range items {
		switch it.Kind {
		case session.KindUser:
			print("You")
			print(it.Text)
			print("")
		case session.KindAssistant:
			print("Assistant")
			print(it.Text)
			print("")
		case session.KindTool:
			// Compact final state only (no args/bodies/diffs).
			note := ""
			if it.Note != "" {
				note = " (" + it.Note + ")"
			}
			print(terminal.Sanitize(it.Text + note))
		case session.KindCompaction:
			print("Context compacted: " + it.Text)
			print("")
		case session.KindBranchSummary:
			print(it.Text)
			print("")
		case session.KindSystemMarker:
			print("[" + it.Text + "]")
		case session.KindHistoryBoundary:
			print("--- " + it.Text + " ---")
		case session.KindNotice:
			print(it.Text)
		}
	}
}

// replayActivation displays divider + identity + current view before the next prompt (P2-035).
// Switching sessions appends a divider; previous sessions stay in scrollback.
// Resuming the active session is a no-op (no duplicate transcript).
func replayActivation(print func(string), act *Activation, isNew bool, activeID string) {
	if act.Store.Header().ID == activeID && !isNew {
		return // no-op, no duplicate
	}
	print("--- session " + act.Store.Header().ID + " ---")
	if act.Offline && act.OfflineErr != nil {
		print("offline: " + act.OfflineErr.Error() + " (history readable, continuation blocked)")
	}
	renderTimeline(print, act.Timeline)
	// Concise notice pointing to /history all when older messages exist.
	// If current view omitted pre-compaction content, all-history has more.
	all := session.AllHistoryView(act.Path)
	if len(all) > len(act.Timeline)+2 {
		print(fmt.Sprintf("(%d historical messages omitted; /history all to view)", len(all)-len(act.Timeline)))
	}
}
