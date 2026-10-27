package agent

import (
	"context"
	"encoding/json"
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
