package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"psi/internal/event"
	toolsummary "psi/internal/tools"
)

type TranscriptKind string

const (
	KindUser            TranscriptKind = "user"
	KindAssistant       TranscriptKind = "assistant"
	KindTool            TranscriptKind = "tool"
	KindCompaction      TranscriptKind = "compaction"
	KindBranchSummary   TranscriptKind = "branch_summary"
	KindSystemMarker    TranscriptKind = "system_marker"
	KindHistoryBoundary TranscriptKind = "history_boundary"
	KindNotice          TranscriptKind = "notice"
)

// TranscriptItem contains visible content and provenance, never hidden tool data.
type TranscriptItem struct {
	Kind        TranscriptKind
	Text        string
	ToolName    string
	ToolState   string
	ToolSummary event.ToolSummary
	SourceID    string
	BlockIDs    []string
	Historical  bool
	Note        string
}

func CurrentView(path []*Entry) []TranscriptItem {
	return timeline(BuildContextEntries(path), path, false)
}

func AllHistoryView(path []*Entry) []TranscriptItem {
	out := []TranscriptItem{{Kind: KindHistoryBoundary, Text: "Historical messages (display only, not model input)"}}
	return append(out, timeline(path, path, true)...)
}

type displayTool struct {
	name, state, note string
	call              bool
	summary           event.ToolSummary
}

// Join once across the whole selected view. A per-entry projection loses the
// result linkage and wrongly labels every completed historical call interrupted.
func displayTools(entries []*Entry) map[string]*displayTool {
	out := map[string]*displayTool{}
	for _, e := range entries {
		if e.Kind != "message" {
			continue
		}
		m := e.Message.Message
		if m.Assistant != nil {
			for _, b := range m.Assistant.Content {
				if b.Call == nil {
					continue
				}
				t := out[b.Call.ID]
				if t == nil {
					t = &displayTool{state: "interrupted", note: "outcome unknown"}
					out[b.Call.ID] = t
				}
				t.name, t.call = b.Call.Name, true
			}
		}
		if tr := m.ToolResult; tr != nil {
			t := out[tr.ToolCallID]
			if t == nil {
				t = &displayTool{}
				out[tr.ToolCallID] = t
			}
			if t.name == "" {
				t.name = tr.ToolName
			}
			t.state, t.note = "done", ""
			if tr.IsError {
				t.state = "failed"
			}
			var details struct {
				Outcome string `json:"psiOutcome"`
			}
			if json.Unmarshal(tr.Details, &details) == nil && tr.IsError {
				switch details.Outcome {
				case "cancelled":
					t.state = "cancelled"
				case "not_started":
					t.state = "not started"
				}
			}
		}
	}
	return out
}

func clearToolSummaries(display map[string]*displayTool, e *Entry) {
	if e == nil || e.Message == nil || e.Message.Message.Assistant == nil {
		return
	}
	for _, block := range e.Message.Message.Assistant.Content {
		if block.Call != nil {
			if t := display[block.Call.ID]; t != nil {
				t.summary = event.ToolSummary{}
			}
		}
	}
}

func addToolSummaries(display map[string]*displayTool, e *Entry, cwd, home string) {
	if e == nil || e.Message == nil || e.Message.Message.Assistant == nil {
		return
	}
	for _, block := range e.Message.Message.Assistant.Content {
		if block.Call == nil {
			continue
		}
		if t := display[block.Call.ID]; t != nil {
			if summary, ok := e.toolSummaries[block.Call.ID]; ok {
				t.summary = toolsummary.RebaseSummaryPath(summary, cwd, home)
			} else if block.Call.Arguments != nil {
				t.summary = toolsummary.SummarizeArguments(block.Call.Name, block.Call.Arguments, cwd, home)
			}
		}
	}
}

func provenanceEntries(path []*Entry) map[string]*Entry {
	out := map[string]*Entry{}
	for _, e := range path {
		target := e.provenanceTarget
		if target == "" {
			var data json.RawMessage
			if e.Custom != nil && e.Custom.CustomType == "psi.input" {
				data = e.Custom.Data
			}
			if e.CustomMessage != nil && e.CustomMessage.CustomType == "psi.input" {
				data = e.CustomMessage.Details
			}
			var p struct {
				TargetID string `json:"targetId"`
			}
			if json.Unmarshal(data, &p) == nil {
				target = p.TargetID
			}
		}
		if target != "" {
			out[target] = e
		}
	}
	return out
}

func inputProvenance(path []*Entry) map[string]string {
	out := map[string]string{}
	for _, e := range path {
		var data json.RawMessage
		if e.Custom != nil && e.Custom.CustomType == "psi.input" {
			data = e.Custom.Data
		}
		if e.CustomMessage != nil && e.CustomMessage.CustomType == "psi.input" {
			data = e.CustomMessage.Details
		}
		if len(data) == 0 {
			continue
		}
		var p struct {
			TargetID   string `json:"targetId"`
			Invocation string `json:"invocation"`
		}
		if json.Unmarshal(data, &p) == nil && p.TargetID != "" && p.Invocation != "" {
			out[p.TargetID] = p.Invocation
		}
	}
	return out
}

func timeline(entries, path []*Entry, historical bool) []TranscriptItem {
	tools, inputs := displayTools(entries), inputProvenance(path)
	var out []TranscriptItem
	for _, e := range entries {
		addToolSummaries(tools, e, "", "")
		out = append(out, timelineEntry(e, tools, inputs, historical)...)
		clearToolSummaries(tools, e)
	}
	return out
}

func timelineEntry(e *Entry, tools map[string]*displayTool, inputs map[string]string, historical bool) []TranscriptItem {
	var out []TranscriptItem
	add := func(e *Entry, kind TranscriptKind, text string) {
		out = append(out, TranscriptItem{Kind: kind, Text: text, SourceID: e.ID(), Historical: historical})
	}
	switch e.Kind {
	case "message":
		m := e.Message.Message
		switch {
		case m.User != nil:
			text := timelineUserText(m.User.Content, nil)
			if invocation, ok := inputs[e.ID()]; ok {
				text = invocation
			}
			add(e, KindUser, text)
		case m.Assistant != nil:
			var text strings.Builder
			flush := func() {
				if text.Len() > 0 {
					add(e, KindAssistant, text.String())
					text.Reset()
				}
			}
			for _, b := range m.Assistant.Content {
				if b.Text != nil {
					text.WriteString(b.Text.Text)
				}
				if b.Call != nil {
					flush()
					t := tools[b.Call.ID]
					out = append(out, TranscriptItem{Kind: KindTool, Text: t.name + ": " + t.state, ToolName: t.name, ToolState: t.state, ToolSummary: t.summary, SourceID: e.ID(), BlockIDs: []string{b.Call.ID}, Note: t.note, Historical: historical})
				}
			}
			flush()
		case m.ToolResult != nil:
			t := tools[m.ToolResult.ToolCallID]
			if !t.call {
				name := t.name
				if name == "" {
					name = "tool"
				}
				out = append(out, TranscriptItem{Kind: KindTool, Text: name + ": " + t.state, ToolName: name, ToolState: t.state, ToolSummary: event.ToolSummary{Kind: name, Note: "historical result without retained call"}, SourceID: e.ID(), BlockIDs: []string{m.ToolResult.ToolCallID}, Note: "historical result without retained call", Historical: historical})
			}
		case m.BashExecution != nil:
			b := m.BashExecution
			state := "done"
			if b.Cancelled {
				state = "cancelled"
			} else if b.ExitCode == nil {
				state = "interrupted"
			} else if *b.ExitCode != 0 {
				state = "failed"
			}
			summary, ok := e.toolSummaries[e.ID()]
			if ok {
				summary = toolsummary.RebaseSummaryPath(summary, "", "")
			} else {
				summary = toolsummary.SummarizeArguments("bash", map[string]any{"command": b.Command}, "", "")
			}
			if b.ExitCode != nil {
				code := *b.ExitCode
				summary.ExitCode = &code
			}
			out = append(out, TranscriptItem{Kind: KindTool, Text: "bash: " + bashOutcome(b), ToolName: "bash", ToolState: state, ToolSummary: summary, SourceID: e.ID(), Historical: historical})
		}
	case "compaction":
		text := e.Compaction.Summary
		if !historical {
			text = "Context compacted"
			if e.Compaction.TokensBefore > 0 {
				text += fmt.Sprintf(" · ~%.0f tokens before", e.Compaction.TokensBefore)
			}
		}
		add(e, KindCompaction, text)
	case "branch_summary":
		add(e, KindBranchSummary, "Branch: "+e.BranchSummary.Summary)
	case "custom_message":
		if e.CustomMessage.Display {
			add(e, KindAssistant, "["+e.CustomMessage.CustomType+"] "+timelineUserText(e.CustomMessage.Content, nil))
		}
	case "model_change":
		add(e, KindSystemMarker, fmt.Sprintf("model: %s/%s", e.ModelChange.Provider, e.ModelChange.ModelID))
	case "thinking_level_change":
		add(e, KindSystemMarker, "thinking: "+e.ThinkingLevelChange.ThinkingLevel)
	case "context_edit":
		add(e, KindSystemMarker, "Context edited (model context updated, transcript preserved).")
	}
	return out
}

func PromptRecall(path []*Entry) []string {
	inputs := inputProvenance(path)
	var out []string
	for _, e := range path {
		if e.Kind != "message" || e.Message.Message.User == nil {
			continue
		}
		text := timelineUserText(e.Message.Message.User.Content, nil)
		if invocation, ok := inputs[e.ID()]; ok {
			text = invocation
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, text)
	}
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	return out
}

func timelineUserText(content any, raw []byte) string {
	blocks, err := UserBlocks(content)
	if err != nil {
		return "[unsupported content]"
	}
	var parts []string
	for _, b := range blocks {
		if b.Text != nil {
			parts = append(parts, b.Text.Text)
		}
		if b.Image != nil {
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

func bashOutcome(b *BashExecutionMessage) string {
	if b.Cancelled {
		return "cancelled"
	}
	if b.ExitCode == nil {
		return "outcome unknown"
	}
	if *b.ExitCode != 0 {
		return fmt.Sprintf("exit %d", *b.ExitCode)
	}
	return "done"
}
