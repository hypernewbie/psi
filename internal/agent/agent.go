// Package agent implements the run-at-a-time model/tool loop (Phase 8).
//
// One goroutine owns mutation of the active session (via Store). Provider and
// process goroutines report results to that owner. Context cancellation
// propagates from the application to HTTP requests and child processes.
// A persistence error stops further model and tool actions.
package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

// Config for a run.
type Config struct {
	Model     provider.ModelMeta
	Thinking  string
	Tools     []provider.ToolDef
	MaxTurns  int // bounded run guard against endless tool cycles
	MaxTokens int
	System    string
}

// Loop owns one active session run at a time.
type Loop struct {
	mu      sync.Mutex
	running bool
	store   *session.Store
	reg     *tools.Registry
	prov    provider.Provider
	sink    func(event.Event)
}

// New creates a loop bound to a store, registry, provider, and event sink.
func New(store *session.Store, reg *tools.Registry, prov provider.Provider, sink func(event.Event)) *Loop {
	if sink == nil {
		sink = func(event.Event) {}
	}
	return &Loop{store: store, reg: reg, prov: prov, sink: sink}
}

// Running reports whether a run is active.
func (l *Loop) Running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

// Submit validates slash/skill commands, appends one user message, then runs
// until the model finishes, cancellation, or unrecoverable error.
func (l *Loop) Submit(ctx context.Context, text string, cfg Config) error {
	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		return fmt.Errorf("a run is already active; cancellation only (no steering queue)")
	}
	l.running = true
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.running = false
		l.mu.Unlock()
	}()
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return fmt.Errorf("empty submission rejected")
	}
	// Skill invocation is expanded by the app layer before Submit; slash
	// commands other than normal text are rejected here as unknown (app maps
	// /help etc. before calling Submit).
	if strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "/skill:") {
		return fmt.Errorf("unknown command %q", trimmed)
	}
	// Detect unresolved historical calls before submission (conservative).
	proj, err := l.store.Projection()
	if err != nil {
		return err
	}
	llm := session.ConvertToLLM(proj.Flat)
	if missing := session.UnresolvedToolCalls(llm); len(missing) > 0 {
		// Never rerun uncertain side effects. Record compatible interruption
		// results only for the known-safe subset (none in first release):
		// stop and explain instead of continuing.
		return fmt.Errorf("unresolved tool calls from interrupted session (%d); resolve or start a new branch before continuing", len(missing))
	}
	// Append accepted user message before the provider request (durable).
	if _, err := l.store.AppendMessage(userMsg(trimmed)); err != nil {
		return fmt.Errorf("storage failure, stopping: %w", err)
	}
	return l.run(ctx, cfg)
}

func userMsg(text string) session.AgentMessage {
	return session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: text, Timestamp: time.Now().UnixMilli()}}
}

// run continues until a final answer, cancellation, error, or turn bound.
func (l *Loop) run(ctx context.Context, cfg Config) error {
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 25
	}
	l.sink(event.Event{Type: event.RunStart})
	defer l.sink(event.Event{Type: event.RunEnd})
	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Rebuild model-visible context from the projector every turn.
		proj, err := l.store.Projection()
		if err != nil {
			return err
		}
		llm := session.ConvertToLLM(proj.Flat)
		if err := session.CheckContinuable(llm, proj.Entries); err != nil {
			return err
		}
		req, err := l.buildRequest(llm, proj, cfg)
		if err != nil {
			return err
		}
		// Stream one model response; deltas go to the sink, final is authoritative.
		var deltas []string
		resp, err := l.prov.Send(ctx, req, func(s string) {
			deltas = append(deltas, s)
			l.sink(event.Event{Type: event.TextDelta, Text: s})
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("provider: %w", err)
		}
		amsg := toSessionAssistant(resp, cfg)
		aid, err := l.store.AppendMessage(amsg)
		if err != nil {
			return fmt.Errorf("storage failure, stopping: %w", err)
		}
		_ = aid
		_ = deltas
		if len(resp.ToolCalls) == 0 {
			return nil // normal final response
		}
		// Run the complete tool batch in assistant order.
		if err := l.runBatch(ctx, resp.ToolCalls); err != nil {
			return err
		}
	}
	return fmt.Errorf("run guard: exceeded %d turns (possible endless tool cycle)", maxTurns)
}

// buildRequest translates projected LLM messages + active tools.
func (l *Loop) buildRequest(llm []session.AgentMessage, proj *session.Projection, cfg Config) (provider.Request, error) {
	var msgs []provider.Message
	for _, m := range llm {
		switch m.Role {
		case "system":
			// System messages fold into Request.System (leading prompt + replayed state).
			continue
		case "user":
			msgs = append(msgs, provider.Message{Role: "user", Content: userBlocks(m)})
		case "assistant":
			var blocks []provider.Block
			for _, b := range m.Assistant.Content {
				switch b.Type {
				case "text":
					blocks = append(blocks, provider.Block{Type: "text", Text: b.Text.Text})
				case "thinking":
					th := ""
					sig := ""
					if b.Thinking != nil {
						th = b.Thinking.Thinking
						if b.Thinking.ThinkingSignature != nil {
							sig = *b.Thinking.ThinkingSignature
						}
					}
					blocks = append(blocks, provider.Block{Type: "thinking", Thinking: th, ThinkingSignature: sig})
				case "toolCall":
					args := map[string]any{}
					if b.Call != nil {
						args = b.Call.Arguments
						blocks = append(blocks, provider.Block{Type: "toolCall", ID: b.Call.ID, Name: b.Call.Name, Arguments: args})
					}
				}
			}
			msgs = append(msgs, provider.Message{Role: "assistant", Content: blocks})
		case "toolResult":
			var blocks []provider.Block
			for _, b := range m.ToolResult.Content {
				if b.Type == "image" && b.Image != nil {
					blocks = append(blocks, provider.Block{Type: "image", Data: b.Image.Data, MimeType: b.Image.MimeType})
				} else if b.Text != nil {
					blocks = append(blocks, provider.Block{Type: "text", Text: b.Text.Text})
				}
			}
			msgs = append(msgs, provider.Message{Role: "toolResult", ToolCallID: m.ToolResult.ToolCallID, Content: blocks})
		default:
			return provider.Request{}, fmt.Errorf("unsupported role %q in request", m.Role)
		}
	}
	// Effective system = replayed prompt/tool state + configured base.
	eff := session.ReplaySystem(llm)
	sys := cfg.System
	if eff.Content != "" {
		if sys != "" {
			sys += "\n" + eff.Content
		} else {
			sys = eff.Content
		}
	}
	_ = proj
	return provider.Request{Model: cfg.Model, System: sys, Messages: msgs, Tools: cfg.Tools, Thinking: cfg.Thinking, MaxTokens: cfg.MaxTokens}, nil
}

func userBlocks(m session.AgentMessage) []provider.Block {
	var out []provider.Block
	if m.User == nil {
		return out
	}
	switch c := m.User.Content.(type) {
	case string:
		out = append(out, provider.Block{Type: "text", Text: c})
	case []session.UserContentBlock:
		for _, b := range c {
			if b.Type == "image" && b.Image != nil {
				out = append(out, provider.Block{Type: "image", Data: b.Image.Data, MimeType: b.Image.MimeType})
			} else if b.Text != nil {
				out = append(out, provider.Block{Type: "text", Text: b.Text.Text})
			}
		}
	case []any:
		for _, e := range c {
			if mm, ok := e.(map[string]any); ok && mm["type"] == "text" {
				if t, ok := mm["text"].(string); ok {
					out = append(out, provider.Block{Type: "text", Text: t})
				}
			}
		}
	}
	return out
}

func toSessionAssistant(resp *provider.Response, cfg Config) session.AgentMessage {
	var blocks []session.ContentBlock
	if resp.Thinking != "" {
		sig := ""
		blocks = append(blocks, session.ContentBlock{Type: "thinking", Thinking: &session.ThinkingContent{Type: "thinking", Thinking: resp.Thinking, ThinkingSignature: &sig}})
	}
	if resp.Text != "" {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: &session.TextContent{Type: "text", Text: resp.Text}})
	}
	for _, tc := range resp.ToolCalls {
		args := tc.Arguments
		if args == nil {
			args = map[string]any{}
		}
		blocks = append(blocks, session.ContentBlock{Type: "toolCall", Call: &session.ToolCall{Type: "toolCall", ID: tc.ID, Name: tc.Name, Arguments: args}})
	}
	stop := resp.StopReason
	if stop == "" {
		stop = "stop"
		if len(resp.ToolCalls) > 0 {
			stop = "toolUse"
		}
	}
	return session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", Content: blocks, API: "anthropic-messages",
		Provider: "anthropic", Model: cfg.Model.ID,
		Usage:      session.Usage{Input: float64(resp.Usage.Input), Output: float64(resp.Usage.Output), CacheRead: float64(resp.Usage.CacheRead), CacheWrite: float64(resp.Usage.CacheWrite), TotalTokens: float64(resp.Usage.TotalTokens)},
		StopReason: stop, Timestamp: time.Now().UnixMilli(),
	}}
}

// runBatch executes completed calls in order, persisting each result before
// the next model request. Cancellation stops remaining calls.
func (l *Loop) runBatch(ctx context.Context, calls []provider.Block) error {
	for _, tc := range calls {
		if ctx.Err() != nil {
			// Record interruption compatibly? First release: stop without
			// appending a fabricated result; the unresolved call is detected
			// on next resume (conservative recovery).
			return ctx.Err()
		}
		l.sink(event.Event{Type: event.ToolStart, ToolName: tc.Name, ToolCallID: tc.ID, State: "running"})
		res := l.reg.Dispatch(tools.Invocation{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		state := "done"
		if res.IsError {
			state = "failed"
		}
		// Convert to a compatible tool-result message with original call ID.
		var content []session.UserContentBlock
		for _, b := range res.Content {
			if b.Type == "image" {
				content = append(content, session.UserContentBlock{Type: "image", Image: &session.ImageContent{Type: "image", Data: b.Data, MimeType: b.MimeType}})
			} else {
				content = append(content, session.UserContentBlock{Type: "text", Text: &session.TextContent{Type: "text", Text: b.Text}})
			}
		}
		tmsg := session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{
			Role: "toolResult", ToolCallID: tc.ID, ToolName: tc.Name, Content: content, IsError: res.IsError, Timestamp: time.Now().UnixMilli(),
		}}
		if _, err := l.store.AppendMessage(tmsg); err != nil {
			l.sink(event.Event{Type: event.ToolEnd, ToolName: tc.Name, ToolCallID: tc.ID, State: "failed", Err: err})
			return fmt.Errorf("storage failure, stopping: %w", err)
		}
		l.sink(event.Event{Type: event.ToolEnd, ToolName: tc.Name, ToolCallID: tc.ID, State: state})
	}
	return nil
}
