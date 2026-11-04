package app

// Headless JSONL mode for `psi --mode rpc`, a minimal port of Pi's RPC mode.
// Phi spawns one child with piped stdio and speaks the same line protocol:
// commands in on stdin, responses and events out on stdout.
//
// Only the subset Phi's chat surface uses is implemented: prompt, abort,
// get_state, get_available_models, set_model, get_available_thinking_levels,
// set_thinking_level, compact, new_session, get_session_stats, get_commands,
// and set_session_name. Every other command answers pi's standard unknown
// error, so Phi degrades exactly as it would against a Pi lacking it.
// Diagnostics go to stderr; stdout carries protocol only.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/providerregistry"
	"psi/internal/session"
	"psi/internal/skills"
	"psi/internal/tools"
)

// rpcScannerBuffer is the longest accepted stdin line, mirroring Phi's own
// 10 MiB JSONL bound.
const rpcScannerBuffer = 10 * 1024 * 1024

// rpcCommand is one stdin line. Only implemented fields are decoded; the
// rest of pi's command surface falls through to the unknown-command error.
type rpcCommand struct {
	ID                 *string `json:"id"`
	Type               string  `json:"type"`
	Message            string  `json:"message"`
	StreamingBehavior  string  `json:"streamingBehavior"`
	Provider           string  `json:"provider"`
	ModelID            string  `json:"modelId"`
	Level              string  `json:"level"`
	CustomInstructions string  `json:"customInstructions"`
	Name               string  `json:"name"`
}

// rpcResponse is one stdout answer. The id echoes the command's when present;
// Phi drops id-less responses, so every answer carries it through.
type rpcResponse struct {
	ID      *string `json:"id,omitempty"`
	Type    string  `json:"type"`
	Command string  `json:"command"`
	Success bool    `json:"success"`
	Data    any     `json:"data,omitempty"`
	Error   string  `json:"error,omitempty"`
}

func rpcOK(id *string, command string, data any) rpcResponse {
	return rpcResponse{ID: id, Type: "response", Command: command, Success: true, Data: data}
}

func rpcErr(id *string, command, message string) rpcResponse {
	return rpcResponse{ID: id, Type: "response", Command: command, Success: false, Error: message}
}

// rpcModel is the model object Phi reads: provider and id for identity, name
// for display with an ID fallback when the catalog has no separate name.
type rpcModel struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name"`
}

func rpcModelName(models *config.ModelsFile, providerID, id string) string {
	if models != nil {
		if meta, err := models.SelectModel(providerID, id); err == nil && meta.Name != "" {
			return meta.Name
		}
	}
	return id
}

// rpcRunner owns one headless session: the store, the provider, a single
// persistent agent loop, and the locked stdout encoder. Commands run
// sequentially on the reader goroutine; only prompt submissions and agent
// events run beside it.
type rpcRunner struct {
	r    *runtime
	loop *agent.Loop

	mu         sync.Mutex
	out        *json.Encoder
	cancel     context.CancelFunc
	running    bool
	compacting bool
	emitted    int
	streamOpen bool
	toolIndex  int
	// compactionReason carries the last CompactionStart state to the explicit
	// compact command, which publishes compaction_end with the real outcome.
	compactionReason string
}

func (s *rpcRunner) emit(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.out.Encode(v)
}

// runConfig copies the live configuration for one agent run. Dynamic content
// is refreshed per request, the same way the terminal loop refreshes it.
func (s *rpcRunner) runConfig() agent.Config {
	r := s.r
	cfg := r.cfg
	cfg.DynamicConfig = func() (string, []provider.ToolDef) {
		definitions := tools.Definitions(r.settings.DefaultTools)
		if r.syncboardAvailable() {
			definitions = append(definitions, syncboardToolDefinition())
		}
		return r.prompt, definitions
	}
	cfg.InternalTools = map[string]agent.InternalToolHandler{
		syncboardToolName: r.syncboardInternalHandler,
	}
	return cfg
}

// isRunning reports whether a model turn owns the loop right now.
func (s *rpcRunner) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *rpcRunner) setRunning(running bool, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = running
	s.cancel = cancel
}

// markRunning records the model turn without touching the stored cancel:
// RunStart arrives after submission and must not clobber the abort handle.
func (s *rpcRunner) markRunning() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = true
}

func (s *rpcRunner) pendingCount() int {
	return len(s.loop.PendingSteering()) + len(s.loop.PendingFollowUp())
}

// sink translates the agent event stream to Phi's JSONL events. It runs on
// agent goroutines, so every write takes the encoder lock.
func (s *rpcRunner) sink(ev event.Event) {
	switch ev.Type {
	case event.RunStart:
		s.markRunning()
		s.emit(map[string]any{"type": "agent_start"})
	case event.TextDelta:
		if ev.Text == "" {
			return
		}
		s.mu.Lock()
		if !s.streamOpen {
			s.streamOpen = true
			s.toolIndex = 0
		}
		s.mu.Unlock()
		s.emit(map[string]any{
			"type":  "message_update",
			"usage": map[string]any{},
			"assistantMessageEvent": map[string]any{
				"type": "text_delta", "contentIndex": 0, "delta": ev.Text,
			},
		})
	case event.ToolStart:
		s.mu.Lock()
		if !s.streamOpen {
			s.streamOpen = true
			s.toolIndex = 0
		}
		index := s.toolIndex
		s.toolIndex++
		s.mu.Unlock()
		s.emit(map[string]any{
			"type":  "message_update",
			"usage": map[string]any{},
			"assistantMessageEvent": map[string]any{
				"type": "toolcall_start", "contentIndex": index,
				"id": ev.ToolCallID, "toolName": ev.ToolName,
			},
		})
	case event.ToolEnd:
		s.emit(map[string]any{
			"type":  "message_update",
			"usage": map[string]any{},
			"assistantMessageEvent": map[string]any{
				"type": "toolcall_end", "contentIndex": 0,
				"id": ev.ToolCallID, "toolName": ev.ToolName,
			},
		})
	case event.Retry:
		s.emit(map[string]any{
			"type": "auto_retry_start", "attempt": ev.RetryAttempt,
			"maxAttempts": ev.RetryMaximum, "delayMs": ev.RetryDelayMs,
			"errorMessage": ev.RetryClass,
		})
	case event.CompactionStart:
		reason := rpcCompactionReason(ev.State)
		s.mu.Lock()
		s.compactionReason = reason
		s.mu.Unlock()
		s.emit(map[string]any{"type": "compaction_start", "reason": reason})
	case event.RunEnd:
		s.mu.Lock()
		s.streamOpen = false
		s.toolIndex = 0
		s.running = false
		s.cancel = nil
		s.mu.Unlock()
		s.sweepDurable()
		aborted := ev.Outcome == event.OutcomeInterrupted
		s.emit(map[string]any{"type": "agent_settled", "aborted": aborted})
	}
}

func rpcCompactionReason(state string) string {
	switch state {
	case event.CompactionManual, event.CompactionThreshold, event.CompactionOverflow:
		return state
	default:
		return event.CompactionManual
	}
}

// sweepDurable publishes every newly stored assistant and tool message as a
// start/end pair. User messages are published optimistically at submission,
// so the sweep covers only model-side records and each one exactly once.
func (s *rpcRunner) sweepDurable() {
	proj, err := s.r.store.Projection()
	if err != nil {
		return
	}
	for s.emitted < len(proj.Flat) {
		msg := proj.Flat[s.emitted]
		s.emitted++
		if msg.Role != "assistant" && msg.Role != "toolResult" {
			continue
		}
		raw, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		var body any
		if err := json.Unmarshal(raw, &body); err != nil {
			continue
		}
		s.emit(map[string]any{"type": "message_start", "message": body})
		s.emit(map[string]any{"type": "message_end", "message": body})
	}
}

// userMessage publishes one caller-side message immediately, the way Pi's
// agent emits message_start before the run and message_end at persistence.
func (s *rpcRunner) userMessage(text string) {
	body := map[string]any{"role": "user", "content": text, "timestamp": time.Now().UnixMilli()}
	s.emit(map[string]any{"type": "message_start", "message": body})
	s.emit(map[string]any{"type": "message_end", "message": body})
}

// expandSkill expands a /skill:name invocation the way Pi's prompt path does.
// Anything else, including unknown skills, passes through untouched.
func (s *rpcRunner) expandSkill(text string) string {
	if !strings.HasPrefix(text, "/skill:") {
		return text
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/skill:"))
	name, args := rest, ""
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		name, args = rest[:i], strings.TrimSpace(rest[i+1:])
	}
	if name == "" {
		return text
	}
	expanded, err := skills.Invoke(s.r.skills, name, args)
	if err != nil {
		return text
	}
	return expanded
}

// handle runs one command. Prompt is the one async exception: its response
// is the preflight disposition, never completion.
func (s *rpcRunner) handle(cmd rpcCommand) *rpcResponse {
	switch cmd.Type {
	case "prompt":
		return s.prompt(cmd)
	case "abort":
		s.mu.Lock()
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		out := rpcOK(cmd.ID, "abort", nil)
		return &out
	case "get_state":
		return s.getState(cmd)
	case "get_available_models":
		out := rpcOK(cmd.ID, "get_available_models", map[string]any{"models": rpcAvailableModels(s.r.models)})
		return &out
	case "set_model":
		return s.setModel(cmd)
	case "get_available_thinking_levels":
		out := rpcOK(cmd.ID, "get_available_thinking_levels", map[string]any{"levels": provider.SupportedThinking(s.r.cfg.Model)})
		return &out
	case "set_thinking_level":
		return s.setThinking(cmd)
	case "compact":
		return s.compact(cmd)
	case "new_session":
		return s.newSession(cmd)
	case "get_session_stats":
		return s.sessionStats(cmd)
	case "get_commands":
		return s.commands(cmd)
	case "set_session_name":
		return s.setName(cmd)
	case "extension_ui_response":
		// No extension UI channel exists headlessly; the response has no waiter.
		return nil
	default:
		out := rpcErr(cmd.ID, cmd.Type, "Unknown command: "+cmd.Type)
		return &out
	}
}

// prompt validates synchronously, then submits on a goroutine so the command
// loop stays responsive to abort.
func (s *rpcRunner) prompt(cmd rpcCommand) *rpcResponse {
	if s.isCompacting() {
		out := rpcErr(cmd.ID, "prompt", "Cannot submit a prompt while compaction is in progress. Wait for compaction to finish and retry.")
		return &out
	}
	text := s.expandSkill(cmd.Message)
	if strings.TrimSpace(text) == "" {
		out := rpcErr(cmd.ID, "prompt", "empty submission rejected")
		return &out
	}
	if s.r.authErr != nil {
		out := rpcErr(cmd.ID, "prompt", s.r.authErr.Error())
		return &out
	}
	if s.loop.Running() {
		switch cmd.StreamingBehavior {
		case "followUp":
			s.loop.FollowUp(text)
		case "steer":
			s.loop.Steer(text)
		case "":
			out := rpcErr(cmd.ID, "prompt", "Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.")
			return &out
		default:
			s.loop.Steer(text)
		}
		s.userMessage(text)
		out := rpcOK(cmd.ID, "prompt", map[string]any{"disposition": "queued"})
		return &out
	}
	s.userMessage(text)
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.setRunning(true, cancel)
		defer s.setRunning(false, nil)
		_ = s.loop.Submit(ctx, text, s.runConfig())
	}()
	out := rpcOK(cmd.ID, "prompt", map[string]any{"disposition": "started"})
	return &out
}

func (s *rpcRunner) isCompacting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compacting
}

// getState answers with the live runtime shape Phi's wire parser reads.
func (s *rpcRunner) getState(cmd rpcCommand) *rpcResponse {
	s.mu.Lock()
	running, compacting := s.running, s.compacting
	s.mu.Unlock()
	r := s.r
	pending := s.pendingCount()
	var count int
	if proj, err := r.store.Projection(); err == nil {
		count = len(proj.Flat)
	}
	data := map[string]any{
		"model": map[string]any{
			"provider": r.ov.Provider, "id": r.ov.Model,
			"name": rpcModelName(r.models, r.ov.Provider, r.ov.Model),
		},
		"thinkingLevel":         r.ov.Thinking,
		"isStreaming":           running,
		"isCompacting":          compacting,
		"steeringMode":          "all",
		"followUpMode":          "one-at-a-time",
		"sessionFile":           r.store.File(),
		"sessionId":             r.store.Header().ID,
		"sessionName":           r.store.SessionName(),
		"autoCompactionEnabled": r.settings.Compaction.Enabled,
		"messageCount":          count,
		"pendingMessageCount":   pending,
	}
	out := rpcOK(cmd.ID, "get_state", data)
	return &out
}

// rpcAvailableModels enumerates the same selectable catalog the bridge
// offers, with display names for Phi's pickers.
func rpcAvailableModels(models *config.ModelsFile) []rpcModel {
	var out []rpcModel
	if models == nil {
		return nil
	}
	for _, providerID := range providerregistry.IDs() {
		for _, option := range models.ModelOptions(providerID) {
			if !option.Available {
				continue
			}
			name := option.Model.Name
			if name == "" {
				name = option.Model.ID
			}
			out = append(out, rpcModel{Provider: providerID, ID: option.Model.ID, Name: name})
		}
	}
	return out
}

// setModel switches the provider configuration synchronously: the command
// loop is already serialized, so no preparation worker is needed.
func (s *rpcRunner) setModel(cmd rpcCommand) *rpcResponse {
	r := s.r
	providerID, modelID := strings.TrimSpace(cmd.Provider), strings.TrimSpace(cmd.ModelID)
	if providerID == "" || modelID == "" {
		out := rpcErr(cmd.ID, "set_model", "Model not found: "+cmd.Provider+"/"+cmd.ModelID)
		return &out
	}
	if _, err := r.models.SelectModel(providerID, modelID); err != nil {
		out := rpcErr(cmd.ID, "set_model", "Model not found: "+providerID+"/"+modelID)
		return &out
	}
	if providerID == r.ov.Provider && modelID == r.ov.Model {
		out := rpcOK(cmd.ID, "set_model", rpcModel{Provider: providerID, ID: modelID, Name: rpcModelName(r.models, providerID, modelID)})
		return &out
	}
	next := r.ov
	next.Provider, next.Model = providerID, modelID
	cand := *r
	cand.prov = nil
	if err := cand.configure(next); err != nil {
		out := rpcErr(cmd.ID, "set_model", err.Error())
		return &out
	}
	if cand.authErr != nil {
		cand.prov.CloseIdle()
		out := rpcErr(cmd.ID, "set_model", cand.authErr.Error())
		return &out
	}
	if _, err := r.store.AppendModelChange(cand.ov.Provider, cand.ov.Model); err != nil {
		cand.prov.CloseIdle()
		out := rpcErr(cmd.ID, "set_model", err.Error())
		return &out
	}
	if r.prov != nil {
		r.prov.CloseIdle()
	}
	r.ov, r.cfg, r.prov, r.authErr, r.reg = cand.ov, cand.cfg, cand.prov, cand.authErr, cand.reg
	out := rpcOK(cmd.ID, "set_model", rpcModel{Provider: providerID, ID: modelID, Name: rpcModelName(r.models, providerID, modelID)})
	return &out
}

// setThinking changes the level for the live model. Thinking is per-request
// state, so no provider rebuild is needed.
func (s *rpcRunner) setThinking(cmd rpcCommand) *rpcResponse {
	r := s.r
	level := strings.TrimSpace(cmd.Level)
	if level == "" {
		out := rpcErr(cmd.ID, "set_thinking_level", "unsupported thinking level")
		return &out
	}
	supported := false
	for _, candidate := range provider.SupportedThinking(r.cfg.Model) {
		if candidate == level {
			supported = true
			break
		}
	}
	if !supported {
		out := rpcErr(cmd.ID, "set_thinking_level", "unsupported thinking level: "+level)
		return &out
	}
	if level == r.ov.Thinking {
		out := rpcOK(cmd.ID, "set_thinking_level", nil)
		return &out
	}
	if _, err := r.store.AppendThinkingLevelChange(level); err != nil {
		out := rpcErr(cmd.ID, "set_thinking_level", err.Error())
		return &out
	}
	r.ov.Thinking = level
	r.cfg.Thinking = level
	out := rpcOK(cmd.ID, "set_thinking_level", nil)
	return &out
}

// compact runs one native compaction synchronously: Phi's control gate holds
// the UI until the command responds.
func (s *rpcRunner) compact(cmd rpcCommand) *rpcResponse {
	if s.loop.Running() || s.isRunning() {
		out := rpcErr(cmd.ID, "compact", "Cannot compact while a run is active")
		return &out
	}
	s.mu.Lock()
	s.compacting = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.compacting = false
		s.mu.Unlock()
	}()
	compactErr := s.loop.Compact(context.Background(), s.runConfig(), cmd.CustomInstructions)
	s.mu.Lock()
	reason := s.compactionReason
	if reason == "" {
		reason = event.CompactionManual
	}
	s.mu.Unlock()
	if compactErr != nil {
		aborted := compactErr == context.Canceled
		s.emit(map[string]any{
			"type": "compaction_end", "reason": reason,
			"aborted": aborted, "errorMessage": compactErr.Error(),
		})
		out := rpcErr(cmd.ID, "compact", compactErr.Error())
		return &out
	}
	s.emit(map[string]any{
		"type": "compaction_end", "reason": reason,
		"aborted": false, "result": map[string]any{},
	})
	out := rpcOK(cmd.ID, "compact", map[string]any{})
	return &out
}

// newSession replaces the store, exactly like /new. A busy or queued session
// is refused: Phi pre-checks idleness and never wants a torn transcript.
func (s *rpcRunner) newSession(cmd rpcCommand) *rpcResponse {
	r := s.r
	if s.loop.Running() || s.isRunning() || s.pendingCount() > 0 || s.isCompacting() {
		out := rpcErr(cmd.ID, "new_session", "Cannot start a new session while a run is active")
		return &out
	}
	next, err := session.New(session.OpenOptions{
		Cwd: r.store.Cwd(), SessionDir: r.store.Dir(),
		AgentDir: r.agentDir, Persist: r.store.Persistent(),
	})
	if err != nil {
		out := rpcErr(cmd.ID, "new_session", err.Error())
		return &out
	}
	if err := next.TryLock(); err != nil {
		out := rpcErr(cmd.ID, "new_session", err.Error())
		return &out
	}
	r.store.Unlock()
	r.store = next
	s.loop = agent.New(next, r.reg, r.prov, s.sink)
	s.mu.Lock()
	s.emitted = 0
	s.mu.Unlock()
	out := rpcOK(cmd.ID, "new_session", map[string]any{"cancelled": false})
	return &out
}

// sessionStats answers with stored token accounting and a live context
// estimate, the two shapes Phi's chat surface renders.
func (s *rpcRunner) sessionStats(cmd rpcCommand) *rpcResponse {
	r := s.r
	acc, err := r.store.Accounting()
	if err != nil {
		out := rpcErr(cmd.ID, "get_session_stats", err.Error())
		return &out
	}
	proj, err := r.store.Projection()
	if err != nil {
		out := rpcErr(cmd.ID, "get_session_stats", err.Error())
		return &out
	}
	data := map[string]any{
		"tokens": map[string]any{
			"input": acc.Usage.Input, "output": acc.Usage.Output,
			"cacheRead": acc.Usage.CacheRead, "cacheWrite": acc.Usage.CacheWrite,
		},
		"contextUsage": map[string]any{
			"tokens":        agent.EstimatedContext(proj, r.cfg),
			"contextWindow": r.cfg.Model.ContextWindow,
		},
	}
	out := rpcOK(cmd.ID, "get_session_stats", data)
	return &out
}

// commands answers with the live session skills, the only invokable source
// headlessly. The skill: prefix is what Phi's chat sends back through prompt.
func (s *rpcRunner) commands(cmd rpcCommand) *rpcResponse {
	var entries []map[string]any
	for _, skill := range s.r.skills {
		entries = append(entries, map[string]any{
			"name": "skill:" + skill.Name, "description": skill.Description, "source": "skill",
		})
	}
	if entries == nil {
		entries = []map[string]any{}
	}
	out := rpcOK(cmd.ID, "get_commands", map[string]any{"commands": entries})
	return &out
}

// setName persists the display name and publishes the change event Phi's
// title service listens for.
func (s *rpcRunner) setName(cmd rpcCommand) *rpcResponse {
	name := strings.TrimSpace(cmd.Name)
	if name == "" {
		out := rpcErr(cmd.ID, "set_session_name", "Session name cannot be empty")
		return &out
	}
	if _, err := s.r.store.AppendSessionInfo(name); err != nil {
		out := rpcErr(cmd.ID, "set_session_name", err.Error())
		return &out
	}
	s.emit(map[string]any{"type": "session_info_changed", "name": name})
	out := rpcOK(cmd.ID, "set_session_name", nil)
	return &out
}

// runRPCLoop reads commands until stdin ends. Commands run sequentially;
// malformed input answers pi's parse error and never kills the session.
func (s *rpcRunner) runLoop(stdin io.Reader) {
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 64*1024), rpcScannerBuffer)
	split := func(data []byte, atEOF bool) (int, []byte, error) {
		for i, b := range data {
			if b == '\n' {
				line := data[:i]
				if len(line) > 0 && line[len(line)-1] == '\r' {
					line = line[:len(line)-1]
				}
				return i + 1, line, nil
			}
		}
		if atEOF && len(data) > 0 {
			line := data
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return len(data), line, nil
		}
		return 0, nil, nil
	}
	sc.Split(split)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue
		}
		var cmd rpcCommand
		if err := json.Unmarshal(line, &cmd); err != nil {
			s.emit(rpcErr(nil, "parse", "Failed to parse command: "+err.Error()))
			continue
		}
		if resp := s.handle(cmd); resp != nil {
			s.emit(resp)
		}
	}
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
