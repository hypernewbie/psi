// Package agent owns one model/tool run and its durable session history.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

type Config struct {
	Model            provider.ModelMeta
	Thinking         string
	Tools            []provider.ToolDef
	MaxTurns         int
	MaxTokens        int
	System           string
	AutoCompact      bool
	ReserveTokens    int
	KeepRecentTokens int
}

type Loop struct {
	mu      sync.Mutex
	running bool
	store   *session.Store
	reg     *tools.Registry
	prov    provider.Provider
	sink    func(event.Event)
}

func New(store *session.Store, reg *tools.Registry, prov provider.Provider, sink func(event.Event)) *Loop {
	if sink == nil {
		sink = func(event.Event) {}
	}
	return &Loop{store: store, reg: reg, prov: prov, sink: sink}
}

func (l *Loop) Running() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.running }

func (l *Loop) begin() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running {
		return fmt.Errorf("a run is already active; cancellation only")
	}
	l.running = true
	return nil
}
func (l *Loop) end() { l.mu.Lock(); l.running = false; l.mu.Unlock() }

func (l *Loop) Submit(ctx context.Context, text string, cfg Config) error {
	if err := l.begin(); err != nil {
		return err
	}
	defer l.end()
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty submission rejected")
	}
	proj, err := l.store.Projection()
	if err != nil {
		return err
	}
	// Validate before conversion, which intentionally handles only known roles,
	// and before accepting a user message into an unsafe imported session.
	if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
		return err
	}
	if missing := session.UnresolvedToolCalls(session.ConvertToLLM(proj.Flat)); len(missing) > 0 {
		return fmt.Errorf("unresolved tool calls from interrupted session (%d); start a new session or recover a copy before continuing", len(missing))
	}
	// Validate provider input before any durable mutation or billable call.
	if cfg.Thinking == "" {
		cfg.Thinking = "off"
	}
	request, err := l.buildRequest(session.ConvertToLLM(proj.Flat), proj, cfg)
	if err != nil {
		return err
	}
	if _, err := provider.BuildRequest(request); err != nil {
		return err
	}
	if err := l.syncConfiguration(proj, cfg); err != nil {
		return err
	}
	uid, err := l.store.AppendMessage(userMsg(text))
	if err != nil {
		return fmt.Errorf("storage failure, stopping: %w", err)
	}
	_ = uid
	return l.run(ctx, cfg)
}

// SubmitWithProvenance stores the expanded user input plus visible invocation provenance (P2-043, P2-054).
// Ordering: user message, then psi.input custom entry (target -> invocation), then provider request.
// Provenance is state-only (type "custom"), so model context is unchanged (pi ignores "custom").
func (l *Loop) SubmitWithProvenance(ctx context.Context, text, invocation, source string, cfg Config) error {
	if err := l.begin(); err != nil {
		return err
	}
	defer l.end()
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty submission rejected")
	}
	proj, err := l.store.Projection()
	if err != nil {
		return err
	}
	if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
		return err
	}
	if missing := session.UnresolvedToolCalls(session.ConvertToLLM(proj.Flat)); len(missing) > 0 {
		return fmt.Errorf("unresolved tool calls from interrupted session (%d); start a new session or recover a copy before continuing", len(missing))
	}
	if cfg.Thinking == "" {
		cfg.Thinking = "off"
	}
	request, err := l.buildRequest(session.ConvertToLLM(proj.Flat), proj, cfg)
	if err != nil {
		return err
	}
	if _, err := provider.BuildRequest(request); err != nil {
		return err
	}
	if err := l.syncConfiguration(proj, cfg); err != nil {
		return err
	}
	uid, err := l.store.AppendMessage(userMsg(text))
	if err != nil {
		return fmt.Errorf("storage failure, stopping: %w", err)
	}
	if invocation != "" {
		if _, err := l.store.AppendProvenance(uid, invocation, source); err != nil {
			return fmt.Errorf("storage failure, stopping: %w", err)
		}
	}
	return l.run(ctx, cfg)
}

func userMsg(text string) session.AgentMessage {
	return session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: text, Timestamp: time.Now().UnixMilli()}}
}

func (l *Loop) syncConfiguration(proj *session.Projection, cfg Config) error {
	var added []session.Tool
	for _, def := range cfg.Tools {
		schema, err := json.Marshal(def.Schema)
		if err != nil {
			return err
		}
		added = append(added, session.Tool{Name: def.Name, Description: def.Description, Parameters: schema})
	}
	sort.Slice(added, func(i, j int) bool { return added[i].Name < added[j].Name })
	eff := session.ReplaySystem(proj.Flat)
	sections := map[string]*string{}
	for key := range eff.Checkpoint.Sections {
		sections[key] = nil
	}
	sections["psi"] = &cfg.System
	if !reflect.DeepEqual(eff.Tools, added) || !reflect.DeepEqual(eff.Checkpoint.Sections, map[string]*string{"psi": &cfg.System}) {
		var removed []session.ToolReference
		for _, tool := range eff.Tools {
			name := tool.Name
			removed = append(removed, session.ToolReference{Name: &name})
		}
		if _, err := l.store.AppendMessage(session.AgentMessage{Role: "system", System: &session.SystemMessage{
			Role: "system", Content: "", Sections: sections, ToolsAdded: added, ToolsRemoved: removed, Timestamp: time.Now().UnixMilli(),
		}}); err != nil {
			return err
		}
	}
	if proj.State.Provider != cfg.Model.ProviderID() || proj.State.ModelID != cfg.Model.ID {
		if _, err := l.store.AppendModelChange(cfg.Model.ProviderID(), cfg.Model.ID); err != nil {
			return err
		}
	}
	if proj.State.ThinkingLevel != cfg.Thinking {
		if _, err := l.store.AppendThinkingLevelChange(cfg.Thinking); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) run(ctx context.Context, cfg Config) (runErr error) {
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 25
	}
	outcome := event.OutcomeDurable
	l.sink(event.Event{Type: event.RunStart})
	defer func() { l.sink(event.Event{Type: event.RunEnd, Outcome: outcome, Err: runErr}) }()
	recovery := &Recovery{}
	for turn := 0; turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		proj, err := l.store.Projection()
		if err != nil {
			return err
		}
		if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
			return err
		}
		estimated := EstimatedContext(proj, cfg)
		if ShouldCompact(estimated, cfg.Model.ContextWindow, cfg.ReserveTokens, cfg.AutoCompact) {
			if err := l.compactWithReason(ctx, cfg, "", event.CompactionThreshold); err != nil && !IsCompactionNoWork(err) {
				return fmt.Errorf("automatic compaction: %w", err)
			}
			proj, err = l.store.Projection()
			if err != nil {
				return err
			}
		}
		req, err := l.buildRequest(session.ConvertToLLM(proj.Flat), proj, cfg)
		if err != nil {
			return err
		}
		// Completion barrier (P2-014, P2-055): track streamed prefix for reconciliation.
		// Transient deltas vs durable completion are distinct; final is authoritative.
		var streamed strings.Builder
		l.sink(event.Event{Type: event.AssistantStart})
		resp, err := l.prov.Send(ctx, req, func(s string) {
			streamed.WriteString(s)
			l.emitText(s, event.OutcomeTransient)
		})
		if err != nil {
			outcome = event.OutcomeInterrupted
			var pe *provider.ProviderError
			if ctx.Err() == nil && streamed.Len() == 0 && cfg.AutoCompact && errors.As(err, &pe) && pe.Kind == provider.ErrContextLimit && recovery.CanRetry() {
				recovery.NoteAttempt()
				if cerr := l.compactWithReason(ctx, cfg, "", event.CompactionOverflow); cerr != nil {
					if IsCompactionNoWork(cerr) {
						return fmt.Errorf("provider: %w", err)
					}
					return fmt.Errorf("context-limit recovery: %w", cerr)
				}
				continue // retry the projected request, never the completed tool batch
			}
			stop := "error"
			if ctx.Err() != nil {
				stop = "aborted"
			}
			if perr := l.persistStreamUsage(cfg, err); perr != nil {
				outcome = event.OutcomeUnsaved
				return perr
			}
			if aerr := l.savePartial(streamed.String(), stop, cfg); aerr != nil {
				outcome = event.OutcomeUnsaved
				return aerr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("provider: %w", err)
		}
		if resp == nil {
			outcome = event.OutcomeInterrupted
			if err := l.savePartial(streamed.String(), "error", cfg); err != nil {
				outcome = event.OutcomeUnsaved
				return err
			}
			return fmt.Errorf("provider returned no completed message")
		}
		// Content is authoritative when supplied; Text and ToolCalls are convenience caches.
		if len(resp.Content) > 0 {
			var text strings.Builder
			resp.ToolCalls = nil
			for _, block := range resp.Content {
				if block.Type == "text" {
					text.WriteString(block.Text)
				}
				if block.Type == "toolCall" {
					resp.ToolCalls = append(resp.ToolCalls, block)
				}
			}
			resp.Text = text.String()
		}
		if !strings.HasPrefix(resp.Text, streamed.String()) {
			outcome = event.OutcomeInterrupted
			if err := l.savePartial(streamed.String(), "error", cfg); err != nil {
				outcome = event.OutcomeUnsaved
				return err
			}
			return fmt.Errorf("protocol failure: final text contradicts streamed text; partial response saved, tools not executed")
		}
		if _, err := l.store.AppendMessage(toSessionAssistant(resp, cfg)); err != nil {
			outcome = event.OutcomeUnsaved
			return fmt.Errorf("storage failure, stopping: %w (unsaved authoritative response)", err)
		}
		if suffix := resp.Text[streamed.Len():]; suffix != "" {
			l.emitText(suffix, event.OutcomeDurable)
		}
		// Settlement barrier: persistence precedes RunEnd and the next prompt.
		if resp.StopReason == "error" || resp.StopReason == "aborted" {
			outcome = event.OutcomeInterrupted
			return fmt.Errorf("provider stopped: %s", resp.StopReason)
		}
		// A length-limited response can contain unfinished tool requests. Never
		// execute them as if the provider completed a tool-use turn.
		if resp.StopReason == "length" {
			return fmt.Errorf("model reached its output limit; response saved, tool calls were not executed")
		}
		if len(resp.ToolCalls) == 0 {
			return nil
		}
		if err := l.runBatch(ctx, resp.ToolCalls, cfg.Tools); err != nil {
			return err
		}
	}
	return fmt.Errorf("run guard: exceeded %d turns (possible endless tool cycle)", maxTurns)
}

// The app's 64-item event queue retains at most 512 KiB of text payload.
func (l *Loop) emitText(text, outcome string) {
	for len(text) > 0 {
		n := min(len(text), 8*1024)
		if n < len(text) {
			for n > 0 && !utf8.RuneStart(text[n]) {
				n--
			}
			if n == 0 {
				n = min(len(text), 8*1024)
			}
		}
		l.sink(event.Event{Type: event.TextDelta, Text: strings.Clone(text[:n]), Outcome: outcome})
		text = text[n:]
	}
}

func (l *Loop) savePartial(text, stop string, cfg Config) error {
	if text == "" {
		return nil
	}
	partial := toSessionAssistant(&provider.Response{Text: text, StopReason: stop}, cfg)
	if _, err := l.store.AppendMessage(partial); err != nil {
		return fmt.Errorf("storage failure, stopping: %w (unsaved partial text %d bytes)", err, len(text))
	}
	return nil
}

// persistStreamUsage records provider-reported counts from a failed stream
// exactly once (F12). Zero usage means unknown and is never persisted, so
// known usage stays distinct from unknown final usage.
func (l *Loop) persistStreamUsage(cfg Config, err error) error {
	var se *provider.StreamInterrupt
	if !errors.As(err, &se) || !se.KnownUsage() {
		return nil
	}
	u := sessionUsage(se.Partial.Usage)
	if _, aerr := l.store.AppendUsage("stream-interrupt", cfg.Model.ProviderID(), cfg.Model.ID, u, "partial stream usage; final counts unknown"); aerr != nil {
		return fmt.Errorf("storage failure, stopping: %w", aerr)
	}
	return nil
}

func (l *Loop) buildRequest(llm []session.AgentMessage, _ *session.Projection, cfg Config) (provider.Request, error) {
	var msgs []provider.Message
	for _, m := range llm {
		switch m.Role {
		case "system":
			continue
		case "user":
			blocks, err := session.UserBlocks(m.User.Content)
			if err != nil {
				return provider.Request{}, err
			}
			msgs = append(msgs, provider.Message{Role: "user", Content: userBlocks(blocks)})
		case "assistant":
			var blocks []provider.Block
			for _, b := range m.Assistant.Content {
				switch b.Type {
				case "text":
					sig := ""
					if b.Text.TextSignature != nil {
						sig = *b.Text.TextSignature
					}
					blocks = append(blocks, provider.Block{Type: "text", Text: b.Text.Text, TextSignature: sig})
				case "thinking":
					sig := ""
					if b.Thinking.ThinkingSignature != nil {
						sig = *b.Thinking.ThinkingSignature
					}
					blocks = append(blocks, provider.Block{Type: "thinking", Thinking: b.Thinking.Thinking, ThinkingSignature: sig, Redacted: b.Thinking.Redacted != nil && *b.Thinking.Redacted})
				case "toolCall":
					namespace := ""
					if b.Call.Namespace != nil {
						namespace = *b.Call.Namespace
					}
					thoughtSignature := ""
					if b.Call.ThoughtSignature != nil {
						thoughtSignature = *b.Call.ThoughtSignature
					}
					blocks = append(blocks, provider.Block{Type: "toolCall", ID: b.Call.ID, Name: b.Call.Name, Arguments: b.Call.Arguments, Namespace: namespace, ThoughtSignature: thoughtSignature})
				default:
					return provider.Request{}, fmt.Errorf("unsupported assistant block %q", b.Type)
				}
			}
			effort := ""
			if m.Assistant.ProviderThinkingLevel != nil {
				effort = *m.Assistant.ProviderThinkingLevel
			}
			msgs = append(msgs, provider.Message{Role: "assistant", Content: blocks, Provider: m.Assistant.Provider, API: m.Assistant.API, Model: m.Assistant.Model, ThinkingLevel: effort, StopReason: m.Assistant.StopReason})
		case "toolResult":
			msgs = append(msgs, provider.Message{Role: "toolResult", ToolCallID: m.ToolResult.ToolCallID, ToolName: m.ToolResult.ToolName, IsError: m.ToolResult.IsError, Content: userBlocks(m.ToolResult.Content)})
		default:
			return provider.Request{}, fmt.Errorf("unsupported role %q in request", m.Role)
		}
	}
	sys := session.ReplaySystem(llm).Content
	if sys == "" {
		sys = cfg.System
	}
	return provider.Request{Model: cfg.Model, System: sys, Messages: msgs, Tools: cfg.Tools, Thinking: cfg.Thinking, MaxTokens: cfg.MaxTokens}, nil
}

func userBlocks(blocks []session.UserContentBlock) []provider.Block {
	var out []provider.Block
	for _, b := range blocks {
		if b.Image != nil {
			out = append(out, provider.Block{Type: "image", Data: b.Image.Data, MimeType: b.Image.MimeType})
		} else if b.Text != nil {
			out = append(out, provider.Block{Type: "text", Text: b.Text.Text})
		}
	}
	return out
}

func toSessionAssistant(resp *provider.Response, cfg Config) session.AgentMessage {
	content := resp.Content
	if len(content) == 0 {
		if resp.Thinking != "" {
			content = append(content, provider.Block{Type: "thinking", Thinking: resp.Thinking})
		}
		if resp.Text != "" {
			content = append(content, provider.Block{Type: "text", Text: resp.Text})
		}
		content = append(content, resp.ToolCalls...)
	}
	blocks := []session.ContentBlock{}
	for _, b := range content {
		switch b.Type {
		case "text":
			var signature *string
			if b.TextSignature != "" {
				sig := b.TextSignature
				signature = &sig
			}
			blocks = append(blocks, session.ContentBlock{Type: "text", Text: &session.TextContent{Type: "text", Text: b.Text, TextSignature: signature}})
		case "thinking":
			var redacted *bool
			if b.Redacted {
				v := true
				redacted = &v
			}
			sig := b.ThinkingSignature
			blocks = append(blocks, session.ContentBlock{Type: "thinking", Thinking: &session.ThinkingContent{Type: "thinking", Thinking: b.Thinking, ThinkingSignature: &sig, Redacted: redacted}})
		case "toolCall":
			args := b.Arguments
			if args == nil {
				args = map[string]any{}
			}
			var namespace, thoughtSignature *string
			if b.Namespace != "" {
				ns := b.Namespace
				namespace = &ns
			}
			if b.ThoughtSignature != "" {
				sig := b.ThoughtSignature
				thoughtSignature = &sig
			}
			blocks = append(blocks, session.ContentBlock{Type: "toolCall", Call: &session.ToolCall{Type: "toolCall", ID: b.ID, Name: b.Name, Arguments: args, Namespace: namespace, ThoughtSignature: thoughtSignature}})
		}
	}
	stop := resp.StopReason
	if stop == "" {
		stop = "stop"
		if len(resp.ToolCalls) > 0 {
			stop = "toolUse"
		}
	}
	api := cfg.Model.API
	if api == "" {
		api = "anthropic-messages"
	}
	a := &session.AssistantMessage{Role: "assistant", Content: blocks, API: api, Provider: cfg.Model.ProviderID(), Model: cfg.Model.ID,
		Usage: sessionUsage(resp.Usage), StopReason: stop, Timestamp: time.Now().UnixMilli()}
	if resp.ResponseID != "" {
		id := resp.ResponseID
		a.ResponseID = &id
	}
	if resp.RawStopReason != "" {
		reason := resp.RawStopReason
		a.RawStopReason = &reason
	}
	if resp.Model != "" && resp.Model != cfg.Model.ID {
		model := resp.Model
		a.ResponseModel = &model
	}
	if resp.ThinkingLevel != "" {
		effort := resp.ThinkingLevel
		a.ProviderThinkingLevel = &effort
	}
	level := cfg.Thinking
	a.ThinkingLevel = &level
	return session.AgentMessage{Role: "assistant", Assistant: a}
}

func (l *Loop) runBatch(ctx context.Context, calls []provider.Block, definitions []provider.ToolDef) error {
	enabled := map[string]bool{}
	for _, d := range definitions {
		enabled[d.Name] = true
	}
	for i, tc := range calls {
		home, _ := os.UserHomeDir()
		summary := tools.SummarizeArguments(tc.Name, tc.Arguments, l.store.Cwd(), home)
		if err := ctx.Err(); err != nil {
			for _, pending := range calls[i:] {
				msg := session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{
					Role: "toolResult", ToolCallID: pending.ID, ToolName: pending.Name, IsError: true,
					Content: []session.UserContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "Tool was not started because the run was cancelled."}}},
					Details: json.RawMessage(`{"psiOutcome":"not_started"}`), Timestamp: time.Now().UnixMilli(),
				}}
				if _, aerr := l.store.AppendMessage(msg); aerr != nil {
					return fmt.Errorf("storage failure, stopping: %w", aerr)
				}
				pendingSummary := tools.SummarizeArguments(pending.Name, pending.Arguments, l.store.Cwd(), home)
				l.sink(event.Event{Type: event.ToolEnd, ToolName: pending.Name, ToolCallID: pending.ID, State: "not started", Summary: pendingSummary})
			}
			return err
		}
		l.sink(event.Event{Type: event.ToolStart, ToolName: tc.Name, ToolCallID: tc.ID, State: "running", Summary: summary})
		res := tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: "Tool is not enabled in this session"}}}
		if enabled[tc.Name] {
			res = l.reg.DispatchCtx(ctx, tools.Invocation{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		state := "done"
		if res.IsError {
			state = "failed"
		}
		if ctx.Err() != nil && res.IsError {
			state = "cancelled"
		}
		var content []session.UserContentBlock
		for _, b := range res.Content {
			if b.Type == "image" {
				content = append(content, session.UserContentBlock{Type: "image", Image: &session.ImageContent{Type: "image", Data: b.Data, MimeType: b.MimeType}})
			} else {
				content = append(content, session.UserContentBlock{Type: "text", Text: &session.TextContent{Type: "text", Text: b.Text}})
			}
		}
		details, err := json.Marshal(res.Details)
		if err != nil {
			return err
		}
		if state == "cancelled" {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(details, &fields); err != nil {
				fields = map[string]json.RawMessage{"toolDetails": details}
			}
			if fields == nil {
				fields = map[string]json.RawMessage{}
			}
			fields["psiOutcome"] = json.RawMessage(`"cancelled"`)
			details, _ = json.Marshal(fields)
		}
		msg := session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{Role: "toolResult", ToolCallID: tc.ID, ToolName: tc.Name, Content: content, Details: details, IsError: res.IsError, Timestamp: time.Now().UnixMilli()}}
		if _, err := l.store.AppendMessage(msg); err != nil {
			return fmt.Errorf("storage failure, stopping: %w", err)
		}
		tools.SetExitCode(&summary, res.Details)
		l.sink(event.Event{Type: event.ToolEnd, ToolName: tc.Name, ToolCallID: tc.ID, State: state, Summary: summary})
	}
	return nil
}
