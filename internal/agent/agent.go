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
	if _, err := provider.BuildAnthropicRequest(request); err != nil {
		return err
	}
	if err := l.syncConfiguration(proj, cfg); err != nil {
		return err
	}
	if _, err := l.store.AppendMessage(userMsg(text)); err != nil {
		return fmt.Errorf("storage failure, stopping: %w", err)
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
	if proj.State.Provider != "anthropic" || proj.State.ModelID != cfg.Model.ID {
		if _, err := l.store.AppendModelChange("anthropic", cfg.Model.ID); err != nil {
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
		resp, err := l.prov.Send(ctx, req, func(s string) { l.sink(event.Event{Type: event.TextDelta, Text: s}) })
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("provider: %w", err)
		}
		if resp == nil {
			return fmt.Errorf("provider returned no completed message")
		}
		if _, err := l.store.AppendMessage(toSessionAssistant(resp, cfg)); err != nil {
			return fmt.Errorf("storage failure, stopping: %w", err)
		}
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
					blocks = append(blocks, provider.Block{Type: "text", Text: b.Text.Text})
				case "thinking":
					sig := ""
					if b.Thinking.ThinkingSignature != nil {
						sig = *b.Thinking.ThinkingSignature
					}
					blocks = append(blocks, provider.Block{Type: "thinking", Thinking: b.Thinking.Thinking, ThinkingSignature: sig, Redacted: b.Thinking.Redacted != nil && *b.Thinking.Redacted})
				case "toolCall":
					blocks = append(blocks, provider.Block{Type: "toolCall", ID: b.Call.ID, Name: b.Call.Name, Arguments: b.Call.Arguments})
				default:
					return provider.Request{}, fmt.Errorf("unsupported assistant block %q", b.Type)
				}
			}
			msgs = append(msgs, provider.Message{Role: "assistant", Content: blocks})
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
			blocks = append(blocks, session.ContentBlock{Type: "text", Text: &session.TextContent{Type: "text", Text: b.Text}})
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
			blocks = append(blocks, session.ContentBlock{Type: "toolCall", Call: &session.ToolCall{Type: "toolCall", ID: b.ID, Name: b.Name, Arguments: args}})
		}
	}
	stop := resp.StopReason
	if stop == "" {
		stop = "stop"
		if len(resp.ToolCalls) > 0 {
			stop = "toolUse"
		}
	}
	a := &session.AssistantMessage{Role: "assistant", Content: blocks, API: "anthropic-messages", Provider: "anthropic", Model: cfg.Model.ID,
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
