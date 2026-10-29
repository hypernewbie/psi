package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
)

const turnPrefixSummaryPrompt = `The messages above are earlier context from an ongoing conversation. Later messages are stored separately and do not need to be reconstructed.

Create a concise checkpoint of the user's request and the progress shown above. This checkpoint will be placed before the later messages so the conversation can continue with the necessary context.

## Original Request
[What did the user ask for?]

## Progress So Far
- [Key decisions and work completed in these messages]

## Context Needed to Continue
- [Information from these messages needed to understand the later work]

Only summarize information explicitly present above. Do not infer or recreate later messages.`

const initialSummaryPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummaryPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on what was accomplished]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const summarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

// Compact generates a real summary before appending any context-changing entry.
// Provider errors, empty summaries, and cancellation leave the context intact.
func (l *Loop) Compact(ctx context.Context, cfg Config, focus string) error {
	if err := l.begin(); err != nil {
		return err
	}
	defer l.end()
	return l.compactWithReason(ctx, cfg, focus, event.CompactionManual)
}

// CheckCompaction performs the same read-only preparation used by Compact.
// The application uses it before showing activity or acquiring credentials.
func (l *Loop) CheckCompaction(cfg Config) (CompactionPreparation, error) {
	if err := l.store.WriteError(); err != nil {
		return CompactionPreparation{}, err
	}
	path, err := l.store.ActivePath()
	if err != nil {
		return CompactionPreparation{}, err
	}
	proj, err := l.store.Projection()
	if err != nil {
		return CompactionPreparation{}, err
	}
	if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
		return CompactionPreparation{}, err
	}
	if len(session.UnresolvedToolCalls(session.ConvertToLLM(proj.Flat))) > 0 {
		return CompactionPreparation{}, fmt.Errorf("cannot compact unresolved tool calls")
	}
	prepared, err := PrepareCompaction(path, proj, cfg.KeepRecentTokens)
	if err != nil {
		return CompactionPreparation{}, err
	}
	if prepared.Disposition != CompactionEligible {
		return prepared, &CompactionNoWorkError{Disposition: prepared.Disposition}
	}
	prepared.TokensBefore = EstimatedContext(proj, cfg)
	return prepared, nil
}

func (l *Loop) compactWithReason(ctx context.Context, cfg Config, focus, reason string) error {
	prepared, err := l.CheckCompaction(cfg)
	if err != nil {
		return err
	}
	l.sink(event.Event{Type: event.CompactionStart, State: reason})
	defer l.sink(event.Event{Type: event.CompactionEnd, State: reason})
	return l.compactPrepared(ctx, cfg, focus, prepared)
}

func (l *Loop) compactPrepared(ctx context.Context, cfg Config, focus string, prepared CompactionPreparation) error {
	if prepared.Disposition != CompactionEligible {
		return &CompactionNoWorkError{Disposition: prepared.Disposition}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	maxOutput := min(4096, cfg.Model.MaxTokens)
	if maxOutput <= 0 {
		maxOutput = 4096
	}
	historyText := prepared.PreviousSummary
	var historyUsage provider.Usage
	hasHistoryUsage := false
	if len(prepared.Messages) > 0 {
		transcript := summaryTranscript(session.ConvertToLLM(prepared.Messages))
		if strings.TrimSpace(transcript) == "" {
			return fmt.Errorf("no conversation text to summarize; original context is unchanged")
		}
		resp, err := l.requestSummary(ctx, cfg, focus, transcript, prepared.PreviousSummary, false, maxOutput)
		if err != nil {
			return err
		}
		historyText = resp.Text
		historyUsage = resp.Usage
		hasHistoryUsage = true
	}
	if prepared.SplitTurn {
		if len(prepared.TurnPrefix) == 0 {
			return fmt.Errorf("split-turn preparation has no prefix messages; original context is unchanged")
		}
		if historyText == "" {
			historyText = "No prior history."
		}
		transcript := summaryTranscript(session.ConvertToLLM(prepared.TurnPrefix))
		if strings.TrimSpace(transcript) == "" {
			if hasHistoryUsage {
				if persistErr := l.persistCompactionUsage(cfg, historyUsage, "history summary completed but turn prefix was empty"); persistErr != nil {
					return persistErr
				}
			}
			return fmt.Errorf("no turn-prefix conversation to summarize; original context is unchanged")
		}
		resp, err := l.requestSummary(ctx, cfg, focus, transcript, "", true, maxOutput)
		if err != nil {
			if hasHistoryUsage {
				if persistErr := l.persistCompactionUsage(cfg, historyUsage, "history summary completed; split-turn summary did not complete"); persistErr != nil {
					return persistErr
				}
			}
			return err
		}
		historyText += "\n\n---\n\n**Turn Context (split turn):**\n\n" + resp.Text
		if hasHistoryUsage {
			historyUsage = combineProviderUsage(historyUsage, resp.Usage)
		} else {
			historyUsage = resp.Usage
			hasHistoryUsage = true
		}
	}
	if strings.TrimSpace(historyText) == "" {
		return fmt.Errorf("summary is incomplete; original context is unchanged")
	}
	if err := ctx.Err(); err != nil {
		if hasHistoryUsage {
			if persistErr := l.persistCompactionUsage(cfg, historyUsage, "summary completed but compaction was cancelled before commit"); persistErr != nil {
				return persistErr
			}
		}
		return err
	}
	usage := sessionUsage(historyUsage)
	_, err := Compact(ctx, l.store, historyText, prepared.FirstKeptEntryID, float64(prepared.TokensBefore), &usage)
	return err
}

func (l *Loop) requestSummary(ctx context.Context, cfg Config, focus, transcript, previous string, turnPrefix bool, maxOutput int) (*provider.Response, error) {
	var prompt string
	if turnPrefix {
		prompt = "# Conversation\n" + transcript + "\n\n# Instructions\n" + turnPrefixSummaryPrompt
		if focus != "" {
			prompt += "\n\nAdditional focus: " + focus
		}
	} else {
		base := initialSummaryPrompt
		if previous != "" {
			base = updateSummaryPrompt
		}
		if focus != "" {
			base += "\n\nAdditional focus: " + focus
		}
		prompt = "<conversation>\n" + transcript + "\n</conversation>\n\n"
		if previous != "" {
			prompt += "<previous-summary>\n" + previous + "\n</previous-summary>\n\n"
		}
		prompt += base
	}
	if cfg.Model.ContextWindow > 0 && len(prompt)/3+maxOutput+2000 >= cfg.Model.ContextWindow {
		return nil, fmt.Errorf("history is too large for a safe summary request; original context is unchanged")
	}
	resp, err := l.prov.Send(ctx, provider.Request{
		Model: cfg.Model, Thinking: "off", MaxTokens: maxOutput, System: summarizationSystemPrompt,
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: prompt}}}},
	}, nil)
	if err != nil {
		// Compaction calls persist known partial usage but never mutate context on failure.
		if perr := l.persistStreamUsage(cfg, err); perr != nil {
			return nil, perr
		}
		return nil, fmt.Errorf("summary failed; original context is unchanged: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Text) == "" || len(resp.ToolCalls) > 0 || (resp.StopReason != "stop" && resp.StopReason != "") {
		if resp != nil {
			if err := l.persistCompactionUsage(cfg, resp.Usage, "incomplete compaction summary; original context unchanged"); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("summary is incomplete; original context is unchanged")
	}
	return resp, nil
}

func (l *Loop) persistCompactionUsage(cfg Config, usage provider.Usage, note string) error {
	if usage.Input == 0 && usage.Output == 0 && usage.CacheRead == 0 && usage.CacheWrite == 0 && usage.TotalTokens == 0 && (usage.CacheWrite1h == nil || *usage.CacheWrite1h == 0) && (usage.Reasoning == nil || *usage.Reasoning == 0) {
		return nil
	}
	if _, err := l.store.AppendUsage("compaction-summary", cfg.Model.ProviderID(), cfg.Model.ID, sessionUsage(usage), note); err != nil {
		return fmt.Errorf("storage failure, stopping: %w", err)
	}
	return nil
}

func combineProviderUsage(a, b provider.Usage) provider.Usage {
	out := provider.Usage{
		Input: a.Input + b.Input, Output: a.Output + b.Output,
		CacheRead: a.CacheRead + b.CacheRead, CacheWrite: a.CacheWrite + b.CacheWrite,
		TotalTokens: a.TotalTokens + b.TotalTokens,
	}
	if a.CacheWrite1h != nil || b.CacheWrite1h != nil {
		var n int64
		if a.CacheWrite1h != nil {
			n += *a.CacheWrite1h
		}
		if b.CacheWrite1h != nil {
			n += *b.CacheWrite1h
		}
		out.CacheWrite1h = &n
	}
	if a.Reasoning != nil || b.Reasoning != nil {
		var n int64
		if a.Reasoning != nil {
			n += *a.Reasoning
		}
		if b.Reasoning != nil {
			n += *b.Reasoning
		}
		out.Reasoning = &n
	}
	return out
}

// IsCompactionNoWork lets the app distinguish an expected manual no-op from a failure.
func IsCompactionNoWork(err error) bool {
	var noWork *CompactionNoWorkError
	return errors.As(err, &noWork)
}

func summaryTranscript(messages []session.AgentMessage) string {
	var out strings.Builder
	for _, m := range messages {
		switch m.Role {
		case "user":
			if m.User == nil {
				continue
			}
			blocks, _ := session.UserBlocks(m.User.Content)
			out.WriteString("\n[User]\n")
			for _, b := range blocks {
				if b.Text != nil {
					out.WriteString(b.Text.Text + "\n")
				}
			}
		case "assistant":
			if m.Assistant == nil {
				continue
			}
			out.WriteString("\n[Assistant]\n")
			for _, b := range m.Assistant.Content {
				if b.Text != nil {
					out.WriteString(b.Text.Text + "\n")
				}
				if b.Call != nil {
					fmt.Fprintf(&out, "Tool call: %s %v\n", b.Call.Name, b.Call.Arguments)
				}
			}
		case "toolResult":
			if m.ToolResult == nil {
				continue
			}
			out.WriteString("\n[Tool result: " + m.ToolResult.ToolName + "]\n")
			for _, b := range m.ToolResult.Content {
				if b.Text != nil {
					out.WriteString(truncateSummaryText(b.Text.Text) + "\n")
				}
			}
		}
	}
	return out.String()
}

func truncateSummaryText(s string) string {
	r := []rune(s)
	if len(r) <= 2000 {
		return s
	}
	return string(r[:2000]) + " [truncated for summary]"
}
