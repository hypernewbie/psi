package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"

	"psi/internal/session"
)

// Compaction estimates, boundaries, and compatible entries (Phase 9).
// The summary itself is produced by a bounded provider call; tests inject a
// fake summarizer so the normal suite stays offline.

// Estimator reports active context size without loading abandoned branches.
type Estimator struct {
	// ContextWindow from model metadata; 0 = unknown (conservative).
	ContextWindow int
}

// Estimate returns estimated tokens for the projected context + system + tools.
// Uses a conservative byte estimate and explicit image overhead. This is an
// estimate, not provider-reported usage or an exact tokenizer.
func (e Estimator) Estimate(proj *session.Projection, systemLen, toolCount int) (tokens int, conservative bool) {
	total := systemLen/3 + len(session.ReplaySystem(proj.Flat).Content)/3 + toolCount*250
	for _, m := range session.ConvertToLLM(proj.Flat) {
		if m.Role != "system" {
			total += messageTokens(m)
		}
	}
	return total, true
}

// EstimatedContext includes the effective system, actual tool schemas, and
// selected message content. It is never labeled as provider-reported usage.
func EstimatedContext(proj *session.Projection, cfg Config) int {
	n, _ := (Estimator{}).Estimate(proj, 0, 0)
	if session.ReplaySystem(proj.Flat).Content == "" {
		n += (len(cfg.System) + 2) / 3
	}
	encoded, _ := json.Marshal(cfg.Tools)
	n += (len(encoded) + 2) / 3
	return n
}

func messageTokens(m session.AgentMessage) int {
	n := 0
	switch m.Role {
	case "system":
		if m.System != nil {
			n += contentLen(m.System.Content)/4 + 50
		}
	case "user":
		if m.User != nil {
			n += userLen(m.User)/4 + 10
		}
	case "assistant":
		if m.Assistant != nil {
			for _, b := range m.Assistant.Content {
				switch b.Type {
				case "text":
					if b.Text != nil {
						n += len(b.Text.Text)/4 + 5
					}
				case "thinking":
					// Exclude opaque reasoning from the estimate budget? No:
					// reasoning still occupies context for replay. Count it.
					if b.Thinking != nil {
						n += (len(b.Thinking.Thinking)+lenString(b.Thinking.ThinkingSignature))/4 + 5
					}
				case "toolCall":
					if b.Call != nil {
						n += len(fmt.Sprint(b.Call.Arguments))/4 + 20
					}
				}
			}
		}
	case "toolResult":
		if m.ToolResult != nil {
			for _, b := range m.ToolResult.Content {
				if b.Text != nil {
					n += len(b.Text.Text)/4 + 5
				}
				if b.Image != nil {
					n += 6000 // conservative image cost
				}
			}
		}
	}
	return n
}

func contentLen(v any) int {
	switch c := v.(type) {
	case string:
		return len(c)
	case []any:
		n := 0
		for _, e := range c {
			n += len(fmt.Sprint(e))
		}
		return n
	}
	return len(fmt.Sprint(v))
}

func lenString(s *string) int {
	if s == nil {
		return 0
	}
	return len(*s)
}

func userLen(u *session.UserMessage) int {
	blocks, err := session.UserBlocks(u.Content)
	if err != nil {
		return len(u.Raw)
	}
	n := 0
	for _, b := range blocks {
		if b.Text != nil {
			n += len(b.Text.Text)
		}
		if b.Image != nil {
			n += 24000
		}
	}
	return n
}

// CompactionDisposition distinguishes legitimate no-work cases from failures.
type CompactionDisposition string

const (
	CompactionEligible     CompactionDisposition = "eligible"
	CompactionEmpty        CompactionDisposition = "empty"
	CompactionWithinBudget CompactionDisposition = "within_budget"
	CompactionAlreadyDone  CompactionDisposition = "already_compacted"
)

// CompactionPreparation is a read-only snapshot of the context to compact.
// Messages retain their entry order and are split only at Pi-safe cut points.
type CompactionPreparation struct {
	Disposition      CompactionDisposition
	FirstKeptEntryID string
	Messages         []session.AgentMessage
	TurnPrefix       []session.AgentMessage
	SplitTurn        bool
	PreviousSummary  string
	TokensBefore     int
}

// NoWork reports a legitimate manual/automatic compaction no-op.
type CompactionNoWorkError struct{ Disposition CompactionDisposition }

func (e *CompactionNoWorkError) Error() string {
	switch e.Disposition {
	case CompactionAlreadyDone:
		return "session is already compacted"
	case CompactionEmpty:
		return "nothing to compact: no conversation history"
	default:
		return "nothing to compact: recent conversation is already within the retained budget"
	}
}

// PrepareCompaction ports Pi's projected cut-point selection for psi's retained
// message roles. It does not call a provider or mutate the session.
func PrepareCompaction(path []*session.Entry, proj *session.Projection, keepRecentTokens int) (CompactionPreparation, error) {
	prepared := CompactionPreparation{Disposition: CompactionEmpty}
	if keepRecentTokens < 0 {
		return prepared, fmt.Errorf("keepRecentTokens must be non-negative")
	}
	if len(path) > 0 && path[len(path)-1].Kind == "compaction" {
		prepared.Disposition = CompactionAlreadyDone
		return prepared, nil
	}
	if proj == nil || len(proj.Entries) == 0 {
		return prepared, nil
	}
	start := 0
	for i, entry := range proj.Entries {
		if entry.Kind == "compaction" && len(proj.Messages[i]) > 0 {
			prepared.PreviousSummary = entry.Compaction.Summary
			start = i + 1
			break
		}
	}
	end := len(proj.Entries)
	cutPoints := make([]int, 0, end-start)
	for i := start; i < end; i++ {
		if proj.Entries[i].Kind != "compaction" && entryCanBeCompactionCut(proj.Messages[i]) {
			cutPoints = append(cutPoints, i)
		}
	}
	if len(cutPoints) == 0 {
		if len(proj.Flat) == 0 || len(proj.Flat) == 1 && proj.Flat[0].Role == "system" {
			return prepared, nil
		}
		prepared.Disposition = CompactionWithinBudget
		return prepared, nil
	}

	cut := cutPoints[0]
	if keepRecentTokens == 0 {
		// psi's established explicit retain-none mode is an intentional deviation
		// from Pi's ordinary cut-point policy. Keep it readable and tested.
		prepared.Messages = append([]session.AgentMessage(nil), proj.Flat...)
		if summaryTranscript(session.ConvertToLLM(prepared.Messages)) == "" {
			prepared.Messages = nil
			return prepared, nil
		}
		prepared.Disposition = CompactionEligible
		return prepared, nil
	}

	accumulated := 0
	exceededBudget := false
	for i := end - 1; i >= start; i-- {
		tokens := 0
		for _, message := range proj.Messages[i] {
			tokens += piEstimateTokens(message)
		}
		if tokens == 0 {
			continue
		}
		accumulated += tokens
		if accumulated >= keepRecentTokens {
			exceededBudget = true
			cut = cutPoints[len(cutPoints)-1]
			for _, candidate := range cutPoints {
				if candidate >= i {
					cut = candidate
					break
				}
			}
			break
		}
	}
	if recoveryOmissionSuffix(proj, cut, end, exceededBudget) {
		cut++
	}
	for cut > start {
		previous := cut - 1
		if proj.Entries[previous].Kind == "compaction" || len(proj.Messages[previous]) > 0 {
			break
		}
		cut--
	}
	turnStart := -1
	if !entryStartsCompactionTurn(proj.Messages[cut]) {
		for i := cut; i >= start; i-- {
			if entryStartsCompactionTurn(proj.Messages[i]) {
				turnStart = i
				break
			}
		}
	}
	prepared.SplitTurn = turnStart >= 0
	if prepared.SplitTurn {
		prepared.Messages = flattenProjectionMessages(proj.Messages[start:turnStart])
		prepared.TurnPrefix = flattenProjectionMessages(proj.Messages[turnStart:cut])
	} else {
		prepared.Messages = flattenProjectionMessages(proj.Messages[start:cut])
	}
	historyText := summaryTranscript(session.ConvertToLLM(prepared.Messages))
	turnPrefixText := summaryTranscript(session.ConvertToLLM(prepared.TurnPrefix))
	if strings.TrimSpace(historyText) == "" && strings.TrimSpace(turnPrefixText) == "" {
		prepared.Messages = nil
		prepared.TurnPrefix = nil
		prepared.SplitTurn = false
		prepared.Disposition = CompactionWithinBudget
		return prepared, nil
	}
	prepared.FirstKeptEntryID = proj.Entries[cut].ID()
	prepared.Disposition = CompactionEligible
	return prepared, nil
}

// recoveryOmissionSuffix mirrors Pi's special cut adjustment for a closed
// context-invisible suffix of abandoned assistant attempts and their omission edits.
func recoveryOmissionSuffix(proj *session.Projection, cut, end int, exceededBudget bool) bool {
	if !exceededBudget || cut+1 >= end {
		return false
	}
	suffix := make([]int, 0, end-cut-1)
	omittedIDs := map[string]bool{}
	for i := cut + 1; i < end; i++ {
		entry := proj.Entries[i]
		intrinsicallyVisible := entry.Kind != "context_edit" && len(session.EntryToContextMessages(entry)) > 0
		omitted := intrinsicallyVisible && len(proj.Messages[i]) == 0
		if omitted {
			omittedIDs[entry.ID()] = true
		}
		suffix = append(suffix, i)
	}
	hasExternalReplacement := false
	hasOmittedAssistant := false
	for _, i := range suffix {
		entry := proj.Entries[i]
		if entry.Kind == "context_edit" && entry.ContextEdit != nil && len(entry.ContextEdit.Replacement) > 0 && string(entry.ContextEdit.Replacement) != "null" && !omittedIDs[entry.ContextEdit.TargetID] {
			hasExternalReplacement = true
		}
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.Assistant != nil && len(proj.Messages[i]) == 0 {
			hasOmittedAssistant = true
		}
	}
	if hasExternalReplacement || !hasOmittedAssistant {
		return false
	}
	for _, i := range suffix {
		entry := proj.Entries[i]
		intrinsicallyVisible := entry.Kind != "context_edit" && len(session.EntryToContextMessages(entry)) > 0
		omitted := intrinsicallyVisible && len(proj.Messages[i]) == 0
		if entry.Kind == "compaction" || intrinsicallyVisible && !omitted {
			return false
		}
	}
	return true
}

func flattenProjectionMessages(groups [][]session.AgentMessage) []session.AgentMessage {
	var out []session.AgentMessage
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func entryCanBeCompactionCut(messages []session.AgentMessage) bool {
	for _, m := range messages {
		switch m.Role {
		case "user", "assistant", "custom", "branchSummary", "compactionSummary", "bashExecution":
			return true
		}
	}
	return false
}

func entryStartsCompactionTurn(messages []session.AgentMessage) bool {
	for _, m := range messages {
		switch m.Role {
		case "user", "custom", "branchSummary", "compactionSummary", "bashExecution":
			return true
		}
	}
	return false
}

func piEstimateTokens(m session.AgentMessage) int {
	chars := 0
	switch m.Role {
	case "system":
		if m.System != nil {
			chars += piContentChars(m.System.Content)
			for _, section := range m.System.Sections {
				if section != nil {
					chars += piStringChars(*section)
				}
			}
			if m.System.ToolsAdded != nil {
				chars += piJSONChars(m.System.ToolsAdded)
			}
		}
	case "user":
		if m.User != nil {
			chars = piContentChars(m.User.Content)
		}
	case "assistant":
		if m.Assistant != nil {
			for _, block := range m.Assistant.Content {
				switch block.Type {
				case "text":
					if block.Text != nil {
						chars += piStringChars(block.Text.Text)
					}
				case "thinking":
					if block.Thinking != nil {
						chars += piStringChars(block.Thinking.Thinking)
					}
				case "toolCall":
					if block.Call != nil {
						chars += piStringChars(block.Call.Name) + piJSONChars(block.Call.Arguments)
					}
				}
			}
		}
	case "toolResult":
		if m.ToolResult != nil {
			chars = piContentChars(m.ToolResult.Content)
		}
	case "custom":
		if m.Custom != nil {
			chars = piContentChars(m.Custom.Content)
		}
	case "bashExecution":
		if m.BashExecution != nil {
			chars = piStringChars(m.BashExecution.Command) + piStringChars(m.BashExecution.Output)
		}
	case "branchSummary":
		if m.BranchSummary != nil {
			chars = piStringChars(m.BranchSummary.Summary)
		}
	case "compactionSummary":
		if m.CompactionSummary != nil {
			chars = piStringChars(m.CompactionSummary.Summary)
		}
	}
	return (chars + 3) / 4
}

func piContentChars(value any) int {
	switch content := value.(type) {
	case string:
		return piStringChars(content)
	case []session.UserContentBlock:
		chars := 0
		for _, block := range content {
			if block.Text != nil {
				chars += piStringChars(block.Text.Text)
			}
			if block.Image != nil {
				chars += 4800
			}
		}
		return chars
	case []any:
		chars := 0
		for _, item := range content {
			if block, ok := item.(map[string]any); ok {
				if text, ok := block["text"].(string); ok {
					chars += piStringChars(text)
				}
				if block["type"] == "image" {
					chars += 4800
				}
			}
		}
		return chars
	default:
		return 0
	}
}

func piStringChars(s string) int { return len(utf16.Encode([]rune(s))) }

func piJSONChars(value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return piStringChars(string(data))
}

// Boundary selects a safe compaction boundary: tool calls stay with results,
// prefer user-message boundaries, single long spans fail safely.
func Boundary(path []*session.Entry, keepRecent int) (firstKeptID string, err error) {
	if len(path) == 0 {
		return "", fmt.Errorf("no entries to compact")
	}
	// Walk back keepRecent user-visible messages, then extend to a safe point.
	// Safe = not between a tool call and its results.
	n := len(path)
	start := n - keepRecent
	if start < 0 {
		start = 0
	}
	// Prefer a user message at start.
	for start > 0 {
		e := path[start]
		if e.Kind == "message" && e.Message.Message.User != nil {
			break
		}
		start--
		if start == 0 {
			break
		}
	}
	// Ensure we don't split tool call/result: if start lands on a toolResult
	// whose call is before start, move start back to the call.
	for start > 0 {
		e := path[start]
		if e.Kind == "message" && e.Message.Message.ToolResult != nil {
			callID := e.Message.Message.ToolResult.ToolCallID
			// Find the call.
			for i := start - 1; i >= 0; i-- {
				pe := path[i]
				if pe.Kind == "message" && pe.Message.Message.Assistant != nil {
					for _, b := range pe.Message.Message.Assistant.Content {
						if b.Type == "toolCall" && b.Call != nil && b.Call.ID == callID {
							start = i
							goto recheck
						}
					}
				}
			}
		}
		break
	recheck:
	}
	if start >= n {
		return "", fmt.Errorf("no useful compaction boundary fits")
	}
	return path[start].ID(), nil
}

// Summarizer produces a bounded summary (injected for tests; real impl uses
// a bounded provider call with cancellation that never mutates context).
type Summarizer func(ctx context.Context, older []session.AgentMessage, focus string) (summary string, usage session.Usage, err error)

// Compact appends a compatible compaction entry: summary + retained boundary +
// tokens + usage + system checkpoint. Original entries are preserved; context
// rebuilds through the same projector. One operation at a time (caller holds
// the loop lock); summary failure corrupts nothing.
func Compact(ctx context.Context, store *session.Store, summary string, firstKeptID string, tokensBefore float64, usage *session.Usage) (string, error) {
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("summary must be non-empty")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	proj, err := store.Projection()
	if err != nil {
		return "", err
	}
	sys := session.ReplaySystem(proj.Flat).Checkpoint
	id, err := store.AppendCompactionWithUsage(summary, firstKeptID, tokensBefore, sys, usage)
	if err != nil {
		return "", err
	}
	return id, nil
}

// ShouldCompact reports threshold at settled request boundaries.
// Overflow-safe (F14): it compares estimated > contextWindow - reserve
// instead of adding, matching Pi's threshold without signed wrapping.
// A reserve above the window exceeds the threshold even for empty context.
func ShouldCompact(estimated, contextWindow, reserve int, enabled bool) bool {
	if !enabled || contextWindow <= 0 {
		return false
	}
	if reserve < 0 {
		reserve = 0
	}
	return estimated > contextWindow-reserve
}

// Recoverable limits compact-and-retry to a single attempt per run.
type Recovery struct {
	Attempts int
}

func (r *Recovery) CanRetry() bool { return r.Attempts < 1 }
func (r *Recovery) NoteAttempt()   { r.Attempts++ }
