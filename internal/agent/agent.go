// Package agent owns one model/tool run and its durable session history.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

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

func (l *Loop) run(ctx context.Context, cfg Config) error {
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 25
	}
	l.sink(event.Event{Type: event.RunStart})
	defer l.sink(event.Event{Type: event.RunEnd})
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
		estimated, _ := (Estimator{}).Estimate(proj, 0, len(cfg.Tools))
		if ShouldCompact(estimated, cfg.Model.ContextWindow, cfg.ReserveTokens, cfg.AutoCompact) {
			if err := l.compact(ctx, cfg, ""); err != nil {
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
		resp, err := l.prov.Send(ctx, req, func(s string) {
			streamed.WriteString(s)
			l.sink(event.Event{Type: event.TextDelta, Text: s, Outcome: event.OutcomeTransient})
		})
		if err != nil {
			if ctx.Err() != nil {
				// P2-056, P2-058: persist supported aborted form for visible partial text when possible.
				// Never label partial args as executable; mark unstarted as unstarted via no tool execution.
				if streamed.Len() > 0 {
					partial := session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
						Role: "assistant", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: streamed.String()}}},
						API: cfg.Model.API, Provider: cfg.Model.ProviderID(), Model: cfg.Model.ID,
						StopReason: "aborted", Timestamp: time.Now().UnixMilli()}}
					if _, aerr := l.store.AppendMessage(partial); aerr != nil {
						return fmt.Errorf("storage failure, stopping: %w (unsaved partial text %d bytes)", aerr, streamed.Len())
					}
				}
				return ctx.Err()
			}
			return fmt.Errorf("provider: %w", err)
		}
		if resp == nil {
			return fmt.Errorf("provider returned no completed message")
		}
		// Reconcile final authoritative text with streamed prefix (P2-030).
		// If final extends prefix, terminal already shows prefix; caller appends suffix via events?
		// Our sink already emitted deltas; store persists complete message once.
		// If final contradicts prefix, report protocol failure rather than rewriting scrollback.
		if streamed.Len() > 0 && resp.Text != "" && !strings.HasPrefix(resp.Text, streamed.String()) && !strings.HasPrefix(streamed.String(), resp.Text) {
			// Contradiction: neither is a prefix of the other. Persist authoritative, report failure.
			if _, err := l.store.AppendMessage(toSessionAssistant(resp, cfg)); err != nil {
				return fmt.Errorf("storage failure, stopping: %w", err)
			}
			return fmt.Errorf("protocol failure: streamed text contradicts final message (persisted authoritative)")
		}
		if _, err := l.store.AppendMessage(toSessionAssistant(resp, cfg)); err != nil {
			return fmt.Errorf("storage failure, stopping: %w (unsaved authoritative response)", err)
		}
		// Settlement barrier: persistence precedes RunEnd (deferred) and next prompt (P2-014).
		if resp.StopReason == "error" || resp.StopReason == "aborted" {
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
					blocks = append(blocks, provider.Block{Type: "toolCall", ID: b.Call.ID, Name: b.Call.Name, Arguments: b.Call.Arguments, Namespace: namespace})
				default:
					return provider.Request{}, fmt.Errorf("unsupported assistant block %q", b.Type)
				}
			}
			msgs = append(msgs, provider.Message{Role: "assistant", Content: blocks, Provider: m.Assistant.Provider, API: m.Assistant.API, Model: m.Assistant.Model})
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
			var namespace *string
			if b.Namespace != "" {
				ns := b.Namespace
				namespace = &ns
			}
			blocks = append(blocks, session.ContentBlock{Type: "toolCall", Call: &session.ToolCall{Type: "toolCall", ID: b.ID, Name: b.Name, Arguments: args, Namespace: namespace}})
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
		Usage: session.Usage{Input: float64(resp.Usage.Input), Output: float64(resp.Usage.Output), CacheRead: float64(resp.Usage.CacheRead), CacheWrite: float64(resp.Usage.CacheWrite), TotalTokens: float64(resp.Usage.TotalTokens)}, StopReason: stop, Timestamp: time.Now().UnixMilli()}
	if resp.ResponseID != "" {
		id := resp.ResponseID
		a.ResponseID = &id
	}
	if resp.Model != "" && resp.Model != cfg.Model.ID {
		model := resp.Model
		a.ResponseModel = &model
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
	for _, tc := range calls {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.sink(event.Event{Type: event.ToolStart, ToolName: tc.Name, ToolCallID: tc.ID, State: "running"})
		res := tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: "Tool is not enabled in this session"}}}
		if enabled[tc.Name] {
			res = l.reg.DispatchCtx(ctx, tools.Invocation{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		state := "done"
		if res.IsError {
			state = "failed"
		}
		if ctx.Err() != nil {
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
		msg := session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{Role: "toolResult", ToolCallID: tc.ID, ToolName: tc.Name, Content: content, Details: details, IsError: res.IsError, Timestamp: time.Now().UnixMilli()}}
		if _, err := l.store.AppendMessage(msg); err != nil {
			return fmt.Errorf("storage failure, stopping: %w", err)
		}
		l.sink(event.Event{Type: event.ToolEnd, ToolName: tc.Name, ToolCallID: tc.ID, State: state})
	}
	return nil
}
