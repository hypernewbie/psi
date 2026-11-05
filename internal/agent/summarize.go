package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	return l.compactAndContinue(ctx, cfg, focus, "")
}

// CompactAndResume is the /ac compact behavior when it interrupts active work:
// compact the settled session, then submit one continuation message.
func (l *Loop) CompactAndResume(ctx context.Context, cfg Config, focus string) error {
	return l.compactAndContinue(ctx, cfg, focus, "Continue the current task from the compacted context. Do not repeat completed work.")
}

// ContinuePending drains a message that arrived after a run's final queue poll
// but before the application processed its settled event.
func (l *Loop) ContinuePending(ctx context.Context, cfg Config) error {
	if err := l.begin(); err != nil {
		return err
	}
	defer l.end()
	delivered := l.deliverSteering()
	if !delivered {
		var err error
		delivered, err = l.deliverFollowUp()
		if err != nil {
			return err
		}
	}
	if !delivered {
		return nil
	}
	return l.run(ctx, cfg)
}

func (l *Loop) compactAndContinue(ctx context.Context, cfg Config, focus, resumePrompt string) error {
	if err := l.begin(); err != nil {
		return err
	}
	defer l.end()
	if err := l.compactWithReason(ctx, cfg, focus, event.CompactionManual); err != nil {
		return err
	}
	queued := l.deliverSteering()
	if resumePrompt != "" {
		if _, err := l.store.AppendMessage(userMsg(resumePrompt)); err != nil {
			return fmt.Errorf("storage failure, stopping: %w", err)
		}
		l.sink(event.Event{Type: event.SteeringDelivered, Text: resumePrompt})
		return l.run(ctx, cfg)
	}
	if !queued {
		var err error
		queued, err = l.deliverFollowUp()
		if err != nil {
			return err
		}
	}
	if queued {
		return l.run(ctx, cfg)
	}
	return nil
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

func (l *Loop) compactWithReason(ctx context.Context, cfg Config, focus, reason string) (runErr error) {
	prepared, err := l.CheckCompaction(cfg)
	if err != nil {
		return err
	}
	if cfg.ACPolicy != nil {
		cfg.ACPolicy.SetCompacting(true)
	}
	l.sink(event.Event{Type: event.CompactionStart, State: reason})
	defer func() { l.sink(event.Event{Type: event.CompactionEnd, State: reason, Err: runErr}) }()

	summaryProvider, summaryModel := l.prov, cfg.Model
	cleanup := func() {}
	if cfg.CompactionProvider != nil {
		resolvedProvider, resolvedModel, closeProvider, resolveErr := cfg.CompactionProvider()
		if resolveErr != nil {
			if cfg.ACPolicy != nil {
				cfg.ACPolicy.RecordCompaction(false, compactionSourceName(reason), prepared.TokensBefore)
			}
			return resolveErr
		}
		if resolvedProvider != nil {
			summaryProvider = resolvedProvider
		}
		if resolvedModel.ID != "" {
			summaryModel = resolvedModel
		}
		if closeProvider != nil {
			cleanup = closeProvider
		}
	}
	defer cleanup()
	err = l.compactPrepared(ctx, cfg, focus, prepared, summaryProvider, summaryModel)
	if cfg.ACPolicy != nil {
		cfg.ACPolicy.RecordCompaction(err == nil, compactionSourceName(reason), prepared.TokensBefore)
	}
	return err
}

func compactionSourceName(reason string) string {
	switch reason {
	case event.CompactionManual:
		return "command"
	case event.CompactionAdaptive, event.CompactionThreshold:
		return "automatic"
	case event.CompactionEndTurn:
		return "end-of-turn"
	case event.CompactionOverflow:
		return "overflow"
	default:
		return reason
	}
}

func (l *Loop) compactPrepared(ctx context.Context, cfg Config, focus string, prepared CompactionPreparation, summaryProvider provider.Provider, summaryModel provider.ModelMeta) error {
	if prepared.Disposition != CompactionEligible {
		return &CompactionNoWorkError{Disposition: prepared.Disposition}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	summaryCfg := cfg
	summaryCfg.Model = summaryModel
	historyText := prepared.PreviousSummary
	var historyUsage provider.Usage
	hasHistoryUsage := false
	if len(prepared.Messages) > 0 {
		transcript := summaryTranscript(session.ConvertToLLM(prepared.Messages))
		if strings.TrimSpace(transcript) == "" {
			return fmt.Errorf("no conversation text to summarize; original context is unchanged")
		}
		resp, err := l.requestSummary(ctx, summaryProvider, summaryCfg, focus, transcript, prepared.PreviousSummary, false)
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
				if persistErr := l.persistCompactionUsage(summaryCfg, historyUsage, "history summary completed but turn prefix was empty"); persistErr != nil {
					return persistErr
				}
			}
			return fmt.Errorf("no turn-prefix conversation to summarize; original context is unchanged")
		}
		resp, err := l.requestSummary(ctx, summaryProvider, summaryCfg, focus, transcript, "", true)
		if err != nil {
			if hasHistoryUsage {
				if persistErr := l.persistCompactionUsage(summaryCfg, historyUsage, "history summary completed; split-turn summary did not complete"); persistErr != nil {
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
			if persistErr := l.persistCompactionUsage(summaryCfg, historyUsage, "summary completed but compaction was cancelled before commit"); persistErr != nil {
				return persistErr
			}
		}
		return err
	}
	usage := sessionUsage(historyUsage)
	_, err := Compact(ctx, l.store, historyText, prepared.FirstKeptEntryID, float64(prepared.TokensBefore), &usage)
	return err
}

func (l *Loop) requestSummary(ctx context.Context, summaryProvider provider.Provider, cfg Config, focus, transcript, previous string, turnPrefix bool) (*provider.Response, error) {
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
	maxOutput := summaryOutputTokenLimit(cfg.Model, prompt)
	resp, err := summaryProvider.Send(ctx, provider.Request{
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
	if resp == nil {
		return nil, fmt.Errorf("summary provider returned no response; original context is unchanged")
	}
	if resp.StopReason == "length" {
		if err := l.persistCompactionUsage(cfg, resp.Usage, "incomplete compaction summary; original context unchanged"); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("summary hit the output token limit and is incomplete; original context is unchanged")
	}
	if resp.StopReason == "error" {
		if err := l.persistCompactionUsage(cfg, resp.Usage, "compaction summary stopped with an error"); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("summary provider reported an error; original context is unchanged")
	}

	text := resp.Text
	hasToolCall := len(resp.ToolCalls) > 0
	if len(resp.Content) > 0 {
		var textContent strings.Builder
		for _, block := range resp.Content {
			switch block.Type {
			case "text":
				textContent.WriteString(block.Text)
			case "toolCall":
				hasToolCall = true
			}
		}
		text = textContent.String()
	}
	if hasToolCall {
		if err := l.persistCompactionUsage(cfg, resp.Usage, "compaction summary attempted a tool call"); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("summary attempted to call a tool; original context is unchanged")
	}
	if strings.TrimSpace(text) == "" {
		if err := l.persistCompactionUsage(cfg, resp.Usage, "compaction summary returned no text"); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("summary returned no text; original context is unchanged")
	}
	if resp.StopReason != "stop" && resp.StopReason != "" {
		if err := l.persistCompactionUsage(cfg, resp.Usage, "compaction summary stopped before completion"); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("summary stopped before completion (%s); original context is unchanged", resp.StopReason)
	}
	if text != resp.Text {
		copy := *resp
		copy.Text = text
		resp = &copy
	}
	return resp, nil
}

// summaryOutputTokenLimit mirrors Pi's default maxTokens policy: use the model's
// output limit, then reduce it only when the prompt plus Pi's 4096-token context
// safety margin would exceed the model context window.
func summaryOutputTokenLimit(model provider.ModelMeta, prompt string) int {
	limit := model.MaxTokens
	if model.ContextWindow <= 0 {
		return limit
	}
	inputChars := piStringChars(summarizationSystemPrompt) + piStringChars(prompt)
	inputTokens := int(math.Ceil(float64(inputChars) / 3.5))
	available := max(1, model.ContextWindow-inputTokens-4096)
	if limit <= 0 || available < limit {
		return available
	}
	return limit
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
	const maxChars = 2000
	totalChars := piStringChars(s)
	if totalChars <= maxChars {
		return s
	}
	var prefix strings.Builder
	usedChars := 0
	for _, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if usedChars+width > maxChars {
			break
		}
		prefix.WriteRune(r)
		usedChars += width
	}
	return fmt.Sprintf("%s\n\n[... %d more characters truncated]", prefix.String(), totalChars-usedChars)
}
