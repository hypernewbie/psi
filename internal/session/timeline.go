// Package session timeline implements the display projector (P2-011).
// Reference: temp/pi-mono/packages/coding-agent/src/core/session-manager.ts
// (buildContextEntries, sessionEntryToContextMessages, buildSessionProjection)
// and messages.ts (convertToLlm, bashExecutionToText).
// Display has two explicit views; model context rules are unchanged and live in projection.go.
package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TranscriptKind is the visible role for terminal display.
type TranscriptKind string

const (
	KindUser            TranscriptKind = "user"
	KindAssistant       TranscriptKind = "assistant"
	KindTool            TranscriptKind = "tool"
	KindCompaction      TranscriptKind = "compaction"
	KindBranchSummary   TranscriptKind = "branch_summary"
	KindSystemMarker    TranscriptKind = "system_marker" // model/thinking change, context-edit note
	KindHistoryBoundary TranscriptKind = "history_boundary"
	KindNotice          TranscriptKind = "notice"
)

// TranscriptItem is a safe, human-visible item with source provenance.
// It never holds tool args/bodies, credentials, or raw system prompts.
type TranscriptItem struct {
	Kind       TranscriptKind
	Text       string // plain source, already sanitized by renderer
	SourceID   string // entry ID
	BlockIDs   []string
	Historical bool   // true in all-history view for pre-compaction content
	Note       string // boundary/continuation note (e.g. "interrupted", "historical")
}

// CurrentView returns Pi compaction-aware active entries with visible text.
// It shows every visible item (no last-ten sampling), one labeled compaction summary,
// retained conversation, and markers. System bodies stay hidden.
func CurrentView(path []*Entry) []TranscriptItem {
	ctx := BuildContextEntries(path)
	var out []TranscriptItem
	seenCompaction := false
	for _, e := range ctx {
		switch e.Kind {
		case "message":
			m := e.Message.Message
			switch {
			case m.System != nil:
				// Do not print body (4.4). Checkpoint internals hidden.
				continue
			case m.User != nil:
				out = append(out, TranscriptItem{Kind: KindUser, Text: timelineUserText(m.User.Content, m.User.Raw), SourceID: e.ID()})
			case m.Assistant != nil:
				txt := assistantText(e)
				if txt != "" {
					out = append(out, TranscriptItem{Kind: KindAssistant, Text: txt, SourceID: e.ID()})
				}
				// Tool calls joined by full logical ID (4.4): one final compact state per call.
				for _, tc := range assistantToolCalls(e) {
					state, note := toolDisplayState(ctx, tc.ID)
					out = append(out, TranscriptItem{Kind: KindTool, Text: tc.Name + ": " + state, SourceID: e.ID(), BlockIDs: []string{tc.ID}, Note: note})
				}
			case m.ToolResult != nil:
				// Tool results join to their call; standalone (no retained call) gets historical boundary note.
				tr := m.ToolResult
				if !hasCall(ctx, tr.ToolCallID) {
					out = append(out, TranscriptItem{Kind: KindTool, Text: toolNameForResult(ctx, tr.ToolCallID) + ": done (historical)", SourceID: e.ID(), BlockIDs: []string{tr.ToolCallID}, Note: "historical result without retained call"})
				}
				// Otherwise hidden here (already shown with call).
			case m.BashExecution != nil:
				b := m.BashExecution
				out = append(out, TranscriptItem{Kind: KindTool, Text: "bash: " + bashOutcome(b), SourceID: e.ID()})
			}
		case "compaction":
			if seenCompaction {
				continue // older compaction: no duplicate summaries in current view
			}
			seenCompaction = true
			out = append(out, TranscriptItem{Kind: KindCompaction, Text: "Context compacted: " + e.Compaction.Summary, SourceID: e.ID()})
		case "branch_summary":
			out = append(out, TranscriptItem{Kind: KindBranchSummary, Text: "Branch: " + e.BranchSummary.Summary, SourceID: e.ID()})
		case "custom_message":
			cm := e.CustomMessage
			if !cm.Display {
				continue
			}
			out = append(out, TranscriptItem{Kind: KindAssistant, Text: fmt.Sprintf("[%s] %s", cm.CustomType, customText(cm.Content)), SourceID: e.ID()})
		case "model_change":
			mc := e.ModelChange
			out = append(out, TranscriptItem{Kind: KindSystemMarker, Text: fmt.Sprintf("model: %s/%s", mc.Provider, mc.ModelID), SourceID: e.ID()})
		case "thinking_level_change":
			out = append(out, TranscriptItem{Kind: KindSystemMarker, Text: "thinking: " + e.ThinkingLevelChange.ThinkingLevel, SourceID: e.ID()})
		case "context_edit":
			// Preserve original visible conversation; mark changed context without fabricating a turn.
			out = append(out, TranscriptItem{Kind: KindSystemMarker, Text: "Context edited (model context updated, transcript preserved).", SourceID: e.ID()})
		}
	}
	return out
}

// AllHistoryView returns original human-visible messages on the active branch,
// including before compaction, labeled historical. No model request, no file change.
func AllHistoryView(path []*Entry) []TranscriptItem {
	var out []TranscriptItem
	out = append(out, TranscriptItem{Kind: KindHistoryBoundary, Text: "Historical messages (display only, not model input):"})
	compactionSeen := false
	for _, e := range path {
		if e.Kind == "compaction" {
			compactionSeen = true
			out = append(out, TranscriptItem{Kind: KindCompaction, Text: "Historical compaction: " + e.Compaction.Summary, SourceID: e.ID(), Historical: true})
			continue
		}
		items := CurrentView([]*Entry{e})
		for _, it := range items {
			// In all-history, pre-compaction content is historical.
			if !compactionSeen {
				// Before any compaction, still historical when invoked via /history all? Mark all as historical for clarity.
			}
			it.Historical = true
			out = append(out, it)
		}
	}
	return out
}

// PromptRecall returns up to 200 selected-branch user submissions (P2-021).
// It prefers original invocation provenance (psi.input custom entries) and excludes
// selector responses, tool results, and generated summaries. No plaintext history file.
func PromptRecall(path []*Entry) []string {
	var out []string
	// Map psi.input provenance: target entry -> visible invocation.
	provenance := map[string]string{}
	for _, e := range path {
		if e.Kind == "custom" && e.Custom != nil && e.Custom.CustomType == "psi.input" {
			var det struct {
				TargetID   string `json:"targetId"`
				Invocation string `json:"invocation"`
			}
			if err := jsonUnmarshal(e.Custom.Data, &det); err == nil && det.TargetID != "" && det.Invocation != "" {
				provenance[det.TargetID] = det.Invocation
			}
		}
		// Back-compat: older custom_message provenance (if any).
		if e.Kind == "custom_message" && e.CustomMessage.CustomType == "psi.input" {
			var det struct {
				TargetID   string `json:"targetId"`
				Invocation string `json:"invocation"`
			}
			if err := jsonUnmarshal(e.CustomMessage.Details, &det); err == nil && det.TargetID != "" && det.Invocation != "" {
				provenance[det.TargetID] = det.Invocation
			}
		}
	}
	for _, e := range path {
		if e.Kind != "message" {
			continue
		}
		m := e.Message.Message
		if m.User == nil {
			continue
		}
		text := timelineUserText(m.User.Content, m.User.Raw)
		if strings.TrimSpace(text) == "" {
			continue
		}
		// Prefer original invocation when provenance exists (skill submissions).
		if inv, ok := provenance[e.ID()]; ok {
			text = inv
		}
		// Exclude generated summaries (compaction/branch text never appears as user message here).
		out = append(out, text)
	}
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	return out
}

func timelineUserText(content any, raw []byte) string {
	switch c := content.(type) {
	case string:
		return c
	case []UserContentBlock:
		var parts []string
		for _, b := range c {
			if b.Text != nil {
				parts = append(parts, b.Text.Text)
			} else if b.Image != nil {
				parts = append(parts, "[image]")
			}
		}
		return strings.Join(parts, "\n")
	case []any:
		var parts []string
		for _, b := range c {
			if m, ok := b.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				} else if m["type"] == "image" {
					parts = append(parts, "[image]")
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func assistantText(e *Entry) string {
	m := e.Message.Message
	if m.Assistant == nil {
		return ""
	}
	var parts []string
	for _, b := range m.Assistant.Content {
		if b.Type == "text" && b.Text != nil {
			parts = append(parts, b.Text.Text)
		}
	}
	return strings.Join(parts, "")
}

func customText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", content)
}

func bashOutcome(b *BashExecutionMessage) string {
	if b.Cancelled {
		return "cancelled"
	}
	if b.ExitCode != nil && *b.ExitCode != 0 {
		return fmt.Sprintf("exit %d", *b.ExitCode)
	}
	return "done"
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func assistantToolCalls(e *Entry) []ToolCall {
	if e.Kind != "message" || e.Message.Message.Assistant == nil {
		return nil
	}
	var out []ToolCall
	for _, b := range e.Message.Message.Assistant.Content {
		if b.Type == "toolCall" && b.Call != nil {
			out = append(out, *b.Call)
		}
	}
	return out
}

func toolDisplayState(ctx []*Entry, callID string) (string, string) {
	// Join call and result by full logical call ID (4.4). Settled states only in replay.
	for _, e := range ctx {
		if e.Kind != "message" {
			continue
		}
		m := e.Message.Message
		if m.ToolResult != nil && m.ToolResult.ToolCallID == callID {
			// Check for error/cancel markers in result content.
			txt := ""
			for _, b := range m.ToolResult.Content {
				if b.Text != nil {
					txt += b.Text.Text
				}
			}
			lower := strings.ToLower(txt)
			if strings.Contains(lower, "cancel") {
				return "cancelled", ""
			}
			if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "exit 1") {
				return "failed", ""
			}
			return "done", ""
		}
	}
	// No result: unresolved -> interrupted/outcome unknown, never done.
	if unresolvedHasCall(ctx, callID) {
		return "interrupted", "outcome unknown"
	}
	return "interrupted", "outcome unknown"
}

func unresolvedHasCall(ctx []*Entry, callID string) bool {
	for _, e := range ctx {
		for _, tc := range assistantToolCalls(e) {
			if tc.ID == callID {
				return true
			}
		}
	}
	return false
}

func hasCall(ctx []*Entry, callID string) bool { return unresolvedHasCall(ctx, callID) }

func toolNameForResult(ctx []*Entry, callID string) string {
	for _, e := range ctx {
		for _, tc := range assistantToolCalls(e) {
			if tc.ID == callID {
				return tc.Name
			}
		}
	}
	return "tool"
}
