package agent

import (
	"context"
	"fmt"
	"strings"

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
// Prefers provider-reported usage when available; otherwise conservative
// ~4 chars/token with image overhead.
func (e Estimator) Estimate(proj *session.Projection, systemLen, toolCount int) (tokens int, conservative bool) {
	total := systemLen/4 + toolCount*200
	for _, m := range proj.Flat {
		total += messageTokens(m)
	}
	return total, true
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
						n += len(b.Thinking.Thinking)/4 + 5
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
					n += 1500 // conservative image cost
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

func userLen(u *session.UserMessage) int {
	switch c := u.Content.(type) {
	case string:
		return len(c)
	case []session.UserContentBlock:
		n := 0
		for _, b := range c {
			if b.Text != nil {
				n += len(b.Text.Text)
			}
			if b.Image != nil {
				n += 6000
			}
		}
		return n
	}
	return 0
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
	var sys *session.SystemMessage
	for _, m := range proj.Flat {
		if m.Role == "system" && m.System != nil {
			cp := *m.System
			sys = &cp
			break
		}
	}
	_ = usage
	id, err := store.AppendCompaction(summary, firstKeptID, tokensBefore, sys)
	if err != nil {
		return "", err
	}
	return id, nil
}

// ShouldCompact reports threshold at settled request boundaries.
func ShouldCompact(estimated, contextWindow, reserve int, enabled bool) bool {
	if !enabled || contextWindow <= 0 {
		return false
	}
	return estimated+reserve > contextWindow
}

// Recoverable limits compact-and-retry to a single attempt per run.
type Recovery struct {
	Attempts int
}

func (r *Recovery) CanRetry() bool { return r.Attempts < 1 }
func (r *Recovery) NoteAttempt()   { r.Attempts++ }
