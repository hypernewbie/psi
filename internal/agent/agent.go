// Package agent owns one model/tool run and its durable session history.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"psi/internal/ac"
	"psi/internal/config"
	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

type InternalToolHandler func(context.Context, tools.Invocation) tools.Result

type Config struct {
	Model            provider.ModelMeta
	Thinking         string
	Tools            []provider.ToolDef
	MaxTokens        int
	System           string
	Sections         []config.PromptSection
	AutoCompact      bool
	ReserveTokens    int
	KeepRecentTokens int
	// DynamicConfig returns the system prompt, its sections, and the tool set
	// for each model request.
	DynamicConfig func() (string, []config.PromptSection, []provider.ToolDef)
	InternalTools map[string]InternalToolHandler
	AfterToolCall func(context.Context, tools.Invocation, tools.Result) error
	ContinueOnEnd func() (messageType, content string, ok bool)
	ACPolicy      *ac.Controller
	// CompactionProvider optionally supplies a separate model/provider for the
	// summary call. The normal provider remains active for resumed user work.
	CompactionProvider func() (provider.Provider, provider.ModelMeta, func(), error)
	// RetryAttempts bounds agent-level retries of transient summarization
	// errors, mirroring Pi's settings.retry.maxRetries. The provider layer
	// retries pre-stream HTTP failures separately; this policy covers summary
	// requests that die mid-stream.
	RetryAttempts        int
	RetryBaseDelayMs     int
	RetryMaxAgentDelayMs int
	SummaryRetryReason   string
}

// Queue modes match Pi's steeringMode and followUpMode settings: one message
// per turn by default, or every queued message at once.
const (
	queueModeOneAtATime = "one-at-a-time"
	queueModeAll        = "all"
)

type Loop struct {
	mu           sync.Mutex
	running      bool
	store        *session.Store
	reg          *tools.Registry
	prov         provider.Provider
	sink         func(event.Event)
	steerMu      sync.Mutex
	steering     []UserInput
	followUps    []string
	steeringMode string
	followUpMode string
	// pendingBytes counts decoded image bytes still owned by queued or
	// temporarily delivering steering input.
	pendingBytes uint64
}

// UserInput is one user message. Content is a string or
// []session.UserContentBlock. Display is the safe transcript rendering and
// never carries image data. ImageBytes is the verified decoded image total.
type UserInput struct {
	Content    any
	Display    string
	Invocation string
	Source     string
	ImageBytes uint64
}

// Structured reports whether the content carries blocks rather than text.
func (in UserInput) Structured() bool {
	_, ok := in.Content.([]session.UserContentBlock)
	return ok
}

// Text returns the plain text content. Structured input has none.
func (in UserInput) Text() string {
	if text, ok := in.Content.(string); ok {
		return text
	}
	return ""
}

// Empty reports whether the input carries no visible text and no usable block.
func (in UserInput) Empty() bool {
	switch content := in.Content.(type) {
	case string:
		return strings.TrimSpace(content) == ""
	case []session.UserContentBlock:
		for _, block := range content {
			if block.Text != nil && block.Text.Text != "" {
				return false
			}
			if block.Image != nil && block.Image.Data != "" && block.Image.MimeType != "" {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func New(store *session.Store, reg *tools.Registry, prov provider.Provider, sink func(event.Event)) *Loop {
	if sink == nil {
		sink = func(event.Event) {}
	}
	return &Loop{
		store: store, reg: reg, prov: prov, sink: sink,
		steeringMode: queueModeOneAtATime,
		followUpMode: queueModeOneAtATime,
	}
}

// Steer queues a user message for delivery at the next turn boundary, like
// Pi's steering queue: the message joins the running conversation after the
// current turn's tool calls, before the next model request, and keeps the run
// alive. It never interrupts the in-flight request.
func (l *Loop) Steer(text string) {
	l.SteerWithProvenance(text, "", "")
}

// SteerWithProvenance queues a user message plus the invocation that produced
// it, so a dispatched skill keeps its provenance through late delivery.
func (l *Loop) SteerWithProvenance(text, invocation, source string) {
	l.SteerContent(UserInput{Content: text, Display: text, Invocation: invocation, Source: source})
}

// SteerContent queues one structured or text message. Images stay structured
// until delivery, so a steered attachment reaches the next model request.
func (l *Loop) SteerContent(input UserInput) {
	if input.Empty() {
		return
	}
	if input.Display == "" {
		input.Display = input.Text()
	}
	l.steerMu.Lock()
	l.steering = append(l.steering, input)
	l.pendingBytes += input.ImageBytes
	l.steerMu.Unlock()
}

// PendingSteering returns queued, undelivered steering displays.
func (l *Loop) PendingSteering() []string {
	l.steerMu.Lock()
	defer l.steerMu.Unlock()
	out := make([]string, len(l.steering))
	for i, msg := range l.steering {
		out[i] = msg.Display
	}
	return out
}

// FollowUp queues a message until the current run has no more tools or steering.
func (l *Loop) FollowUp(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	l.steerMu.Lock()
	l.followUps = append(l.followUps, text)
	l.steerMu.Unlock()
}

// PendingFollowUp returns queued messages that have not been delivered yet.
func (l *Loop) PendingFollowUp() []string {
	l.steerMu.Lock()
	defer l.steerMu.Unlock()
	return append([]string(nil), l.followUps...)
}

// SetQueueModes mirrors Pi's steeringMode and followUpMode settings. Unknown
// values fall back to Pi's default: one message per turn.
func (l *Loop) SetQueueModes(steering, followUp string) {
	l.steerMu.Lock()
	l.steeringMode = normalizeQueueMode(steering)
	l.followUpMode = normalizeQueueMode(followUp)
	l.steerMu.Unlock()
}

func normalizeQueueMode(mode string) string {
	if mode == queueModeAll {
		return queueModeAll
	}
	return queueModeOneAtATime
}

// QueueModes reports the effective delivery mode of each queue.
func (l *Loop) QueueModes() (steering, followUp string) {
	l.steerMu.Lock()
	defer l.steerMu.Unlock()
	return l.steeringMode, l.followUpMode
}

// takeQueued removes the messages one turn may deliver, honoring the queue mode.
func takeQueued[T any](mode string, queue *[]T) []T {
	if len(*queue) == 0 {
		return nil
	}
	n := 1
	if mode == queueModeAll {
		n = len(*queue)
	}
	taken := append([]T(nil), (*queue)[:n]...)
	*queue = (*queue)[n:]
	return taken
}

// takeSteering removes the batch one turn may deliver. Bytes stay counted
// until each append is durable.
func (l *Loop) takeSteering() []UserInput {
	l.steerMu.Lock()
	defer l.steerMu.Unlock()
	return takeQueued(l.steeringMode, &l.steering)
}

func (l *Loop) takeFollowUp() []string {
	l.steerMu.Lock()
	defer l.steerMu.Unlock()
	return takeQueued(l.followUpMode, &l.followUps)
}

// PendingContentBytes is the decoded image total still owned by queued or
// delivering steering input.
func (l *Loop) PendingContentBytes() uint64 {
	l.steerMu.Lock()
	defer l.steerMu.Unlock()
	return l.pendingBytes
}

// TakeSteering removes the queued input for structured recovery. Display text
// alone cannot reconstruct an image, so callers that recover must take it.
func (l *Loop) TakeSteering() []UserInput {
	l.steerMu.Lock()
	defer l.steerMu.Unlock()
	out := l.steering
	l.steering = nil
	for _, msg := range out {
		l.pendingBytes -= msg.ImageBytes
	}
	return out
}

// ClearSteering takes and clears the queued steering display text.
func (l *Loop) ClearSteering() []string {
	input := l.TakeSteering()
	out := make([]string, len(input))
	for i, msg := range input {
		out[i] = msg.Display
	}
	return out
}

// ClearFollowUp takes and clears the queued follow-up messages.
func (l *Loop) ClearFollowUp() []string {
	l.steerMu.Lock()
	defer l.steerMu.Unlock()
	out := l.followUps
	l.followUps = nil
	return out
}

// releasePending drops bytes that a successful append made durable.
func (l *Loop) releasePending(bytes uint64) {
	if bytes == 0 {
		return
	}
	l.steerMu.Lock()
	if bytes >= l.pendingBytes {
		l.pendingBytes = 0
	} else {
		l.pendingBytes -= bytes
	}
	l.steerMu.Unlock()
}

// deliverQueuedSteering appends queued messages in order and restores the
// remainder on failure, so a retry cannot duplicate or reorder input.
func (l *Loop) deliverQueuedSteering() (bool, error) {
	msgs := l.takeSteering()
	for i, msg := range msgs {
		uid, err := l.store.AppendMessage(userMsg(msg.Content))
		if err != nil {
			l.steerMu.Lock()
			l.steering = append(msgs[i:], l.steering...)
			l.steerMu.Unlock()
			return false, fmt.Errorf("storage failure, stopping: %w", err)
		}
		// The user message is durable from here: release its bytes and report
		// it even if the provenance append fails.
		l.releasePending(msg.ImageBytes)
		if msg.Invocation != "" {
			if _, err := l.store.AppendProvenance(uid, msg.Invocation, msg.Source); err != nil {
				l.steerMu.Lock()
				l.steering = append(msgs[i+1:], l.steering...)
				l.steerMu.Unlock()
				return false, fmt.Errorf("storage failure, stopping: %w", err)
			}
		}
		l.sink(event.Event{Type: event.SteeringDelivered, Text: msg.Display})
	}
	return len(msgs) > 0, nil
}

// deliverFollowUp persists queued messages at the point when the current run
// would otherwise finish, honoring the follow-up queue mode.
func (l *Loop) deliverFollowUp() (bool, error) {
	msgs := l.takeFollowUp()
	for i, text := range msgs {
		if _, err := l.store.AppendMessage(userMsg(text)); err != nil {
			l.steerMu.Lock()
			l.followUps = append(msgs[i:], l.followUps...)
			l.steerMu.Unlock()
			return false, fmt.Errorf("storage failure, stopping: %w", err)
		}
		l.sink(event.Event{Type: event.FollowUpDelivered, Text: text})
	}
	return len(msgs) > 0, nil
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
	return l.submit(ctx, UserInput{Content: text, Display: text}, cfg)
}

// SubmitContent submits structured text/image content through the same path as
// text submissions, so session shape and validation stay identical.
func (l *Loop) SubmitContent(ctx context.Context, input UserInput, cfg Config) error {
	return l.submit(ctx, input, cfg)
}

// SubmitWithProvenance stores the expanded user input plus visible invocation provenance (P2-043, P2-054).
// Ordering: user message, then psi.input custom entry (target -> invocation), then provider request.
// Provenance is state-only (type "custom"), so model context is unchanged (pi ignores "custom").
func (l *Loop) SubmitWithProvenance(ctx context.Context, text, invocation, source string, cfg Config) error {
	input := UserInput{Content: text, Display: text, Invocation: invocation, Source: source}
	return l.submit(ctx, input, cfg)
}

// submit owns the one admission path every user message shares.
func (l *Loop) submit(ctx context.Context, input UserInput, cfg Config) error {
	if err := l.begin(); err != nil {
		return err
	}
	defer l.end()
	if err := ctx.Err(); err != nil {
		return err
	}
	if input.Empty() {
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
	// Validate provider input before any durable mutation or billable call.
	if cfg.Thinking == "" {
		cfg.Thinking = "off"
	}
	refreshDynamicConfig(&cfg)
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
	uid, err := l.store.AppendMessage(userMsg(input.Content))
	if err != nil {
		return fmt.Errorf("storage failure, stopping: %w", err)
	}
	if input.Invocation != "" {
		if _, err := l.store.AppendProvenance(uid, input.Invocation, input.Source); err != nil {
			return fmt.Errorf("storage failure, stopping: %w", err)
		}
	}
	return l.run(ctx, cfg, false)
}

func refreshDynamicConfig(cfg *Config) {
	if cfg.DynamicConfig != nil {
		cfg.System, cfg.Sections, cfg.Tools = cfg.DynamicConfig()
	}
}

func userMsg(content any) session.AgentMessage {
	return session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: content, Timestamp: time.Now().UnixMilli()}}
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
	// Programmatic callers that build no sections keep the legacy single
	// section so their sessions stay byte-compatible.
	if len(cfg.Sections) == 0 {
		sections["psi"] = &cfg.System
	} else {
		for i := range cfg.Sections {
			body := cfg.Sections[i].Body
			sections[cfg.Sections[i].Name] = &body
		}
	}
	if !reflect.DeepEqual(eff.Tools, added) || !reflect.DeepEqual(eff.Checkpoint.Sections, sections) {
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

func (l *Loop) run(ctx context.Context, cfg Config, skipInitialSteeringPoll bool) (runErr error) {
	// Pi's agent loop has no turn cap: it runs until the model stops calling
	// tools, the provider errors, or the user cancels. A legitimate chain of
	// sequential tool calls is unbounded, so psi must not invent a limit.
	outcome := event.OutcomeDurable
	l.sink(event.Event{Type: event.RunStart})
	defer func() { l.sink(event.Event{Type: event.RunEnd, Outcome: outcome, Err: runErr}) }()
	recovery := &Recovery{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Turn boundary: deliver steering queued while the previous turn ran,
		// Turn boundary: deliver steering queued while the previous turn ran,
		// before the next model request (Pi's getSteeringMessages poll). Pi skips
		// this poll when the previous iteration already delivered a batch, so
		// one-at-a-time mode never puts two messages in the same turn.
		deliveredThisTurn := skipInitialSteeringPoll
		if !deliveredThisTurn {
			var err error
			if deliveredThisTurn, err = l.deliverQueuedSteering(); err != nil {
				return err
			}
		}
		skipInitialSteeringPoll = false
		refreshDynamicConfig(&cfg)
		proj, err := l.store.Projection()
		if err != nil {
			return err
		}
		if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
			return err
		}
		if err := l.syncConfiguration(proj, cfg); err != nil {
			return err
		}
		proj, err = l.store.Projection()
		if err != nil {
			return err
		}
		estimated := EstimatedContext(proj, cfg)
		if ShouldCompact(estimated, cfg.Model.ContextWindow, cfg.ReserveTokens, cfg.AutoCompact) {
			if err := l.compactWithReason(ctx, cfg, "", event.CompactionThreshold); err != nil && !IsCompactionNoWork(err) {
				if ctx.Err() != nil || !IsSummaryFailure(err) {
					return fmt.Errorf("automatic compaction: %w", err)
				}
				// Pi reports the failed auto-compaction through compaction_end and
				// keeps the run alive; the next turn retries with the same context.
			}
			// Compaction can take long enough for the user to queue steering.
			// Pi polls again after next-turn preparation, before the next model
			// request; do the same here so steering is not stranded until the
			// post-response boundary (or restored to the editor when the run ends).
			// Poll only when this turn has not already delivered a batch.
			if !deliveredThisTurn {
				if _, err := l.deliverQueuedSteering(); err != nil {
					return err
				}
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
		var streamedThinking strings.Builder
		l.sink(event.Event{Type: event.AssistantStart})
		resp, err := l.prov.Send(ctx, req, func(d provider.Delta) {
			switch d.Kind {
			case provider.DeltaThinking:
				streamedThinking.WriteString(d.Text)
				l.emitChunked(d.Text, event.ThinkingDelta, event.OutcomeTransient)
			default:
				streamed.WriteString(d.Text)
				l.emitChunked(d.Text, event.TextDelta, event.OutcomeTransient)
			}
		})
		if err != nil {
			outcome = event.OutcomeInterrupted
			var pe *provider.ProviderError
			if ctx.Err() == nil && streamed.Len() == 0 && cfg.AutoCompact && errors.As(err, &pe) && pe.Kind == provider.ErrContextLimit && recovery.CanRetry() {
				recovery.NoteAttempt()
				if cerr := l.compactWithReason(ctx, cfg, "", event.CompactionOverflow); cerr != nil {
					if IsCompactionNoWork(cerr) {
						return err
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
			if aerr := l.savePartial(streamed.String(), streamedThinking.String(), stop, cfg); aerr != nil {
				outcome = event.OutcomeUnsaved
				return aerr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if resp == nil {
			outcome = event.OutcomeInterrupted
			if err := l.savePartial(streamed.String(), streamedThinking.String(), "error", cfg); err != nil {
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
			if err := l.savePartial(streamed.String(), streamedThinking.String(), "error", cfg); err != nil {
				outcome = event.OutcomeUnsaved
				return err
			}
			return fmt.Errorf("protocol failure: final text contradicts streamed text; partial response saved, tools not executed")
		}
		if _, err := l.store.AppendMessage(toSessionAssistant(resp, cfg)); err != nil {
			outcome = event.OutcomeUnsaved
			return fmt.Errorf("storage failure, stopping: %w (unsaved authoritative response)", err)
		}
		// A provider may return reasoning only in the final response. Show the
		// part that never arrived as a delta, before the text tail, so the
		// transcript keeps the content order. A mismatch never fails the turn.
		if suffix := unstreamedThinking(resp.Thinking, streamedThinking.String()); suffix != "" {
			l.emitChunked(suffix, event.ThinkingDelta, event.OutcomeDurable)
		}
		if suffix := resp.Text[streamed.Len():]; suffix != "" {
			l.emitChunked(suffix, event.TextDelta, event.OutcomeDurable)
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
			// Deliver steering before follow-ups. Follow-ups wait until the
			// current run has no more tool calls or steering messages.
			deliveredSteering, steerErr := l.deliverQueuedSteering()
			if steerErr != nil {
				return steerErr
			}
			deliveredFollowUp := false
			if !deliveredSteering {
				var deliverErr error
				deliveredFollowUp, deliverErr = l.deliverFollowUp()
				if deliverErr != nil {
					return deliverErr
				}
			}
			// One delivery batch per turn: the next iteration must not deliver
			// another batch before the following model request.
			skipInitialSteeringPoll = deliveredSteering || deliveredFollowUp
			if deliveredFollowUp {
				if err := l.maybeAdaptiveCompact(ctx, cfg, true, true); err != nil {
					return err
				}
				continue
			}
			if cfg.ContinueOnEnd != nil {
				messageType, content, ok := cfg.ContinueOnEnd()
				if ok {
					if _, err := l.store.AppendCustomMessage(messageType, content, false, nil); err != nil {
						return fmt.Errorf("storage failure, stopping: %w", err)
					}
					if err := l.maybeAdaptiveCompact(ctx, cfg, true, deliveredSteering); err != nil {
						return err
					}
					continue
				}
			}
			if !deliveredSteering {
				if err := l.maybeAdaptiveCompact(ctx, cfg, false, false); err != nil {
					return err
				}
				return nil
			}
			// Record growth, but let the queued user message receive the next
			// request before predictive compaction is considered again.
			if err := l.maybeAdaptiveCompact(ctx, cfg, true, true); err != nil {
				return err
			}
		}
		if err := l.runBatch(ctx, resp.ToolCalls, cfg); err != nil {
			return err
		}
		if len(resp.ToolCalls) > 0 {
			if err := l.maybeAdaptiveCompact(ctx, cfg, true, len(l.PendingSteering()) > 0); err != nil {
				return err
			}
		}
	}
}

func (l *Loop) maybeAdaptiveCompact(ctx context.Context, cfg Config, canContinue, hasPendingMessages bool) error {
	if cfg.ACPolicy == nil {
		return nil
	}
	proj, err := l.store.Projection()
	if err != nil {
		return err
	}
	tokens := EstimatedContext(proj, cfg)
	if !cfg.ACPolicy.ObserveTurn(tokens, cfg.Model.ContextWindow, cfg.ReserveTokens, cfg.AutoCompact, canContinue, hasPendingMessages) {
		return nil
	}
	err = l.compactWithReason(ctx, cfg, "", event.CompactionAdaptive)
	if err != nil && IsSummaryFailure(err) && ctx.Err() == nil {
		// Reported through CompactionEnd; the run continues like Pi's
		// auto-compaction instead of failing the turn.
		return nil
	}
	return err
}

// unstreamedThinking returns the tail of the final reasoning that was never
// streamed. It returns "" when the final reasoning contradicts the stream.
func unstreamedThinking(final, streamed string) string {
	if !strings.HasPrefix(final, streamed) {
		return ""
	}
	return final[len(streamed):]
}

// The app's 64-item event queue retains at most 512 KiB of text payload.
func (l *Loop) emitChunked(text string, typ event.Type, outcome string) {
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
		l.sink(event.Event{Type: typ, Text: strings.Clone(text[:n]), Outcome: outcome})
		text = text[n:]
	}
}

// savePartial stores the visible part of an interrupted turn. Thinking that
// arrived before the failure is kept with the text, like Pi's partial message.
// toSessionAssistant orders the thinking block before the text block.
func (l *Loop) savePartial(text, thinking, stop string, cfg Config) error {
	if text == "" && thinking == "" {
		return nil
	}
	partial := &provider.Response{Text: text, Thinking: thinking, StopReason: stop}
	if _, err := l.store.AppendMessage(toSessionAssistant(partial, cfg)); err != nil {
		return fmt.Errorf("storage failure, stopping: %w (unsaved partial output %d bytes)", err, len(text)+len(thinking))
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
	llm = session.TransformMessagesForAPI(llm)
	var msgs []provider.Message
	for _, m := range llm {
		switch m.Role {
		case "system":
			if cfg.Model.API != "openai-responses" || m.System == nil {
				continue
			}
			blocks, err := session.UserBlocks(m.System.Content)
			if err != nil {
				return provider.Request{}, err
			}
			toolDefs, err := responseToolDefinitions(m.System.ToolsAdded)
			if err != nil {
				return provider.Request{}, err
			}
			var removed []string
			for _, tool := range m.System.ToolsRemoved {
				if tool.Name == nil || *tool.Name == "" {
					return provider.Request{}, fmt.Errorf("Responses replay refuses a tool removal without a name")
				}
				removed = append(removed, *tool.Name)
			}
			msgs = append(msgs, provider.Message{Role: "system", Content: userBlocks(blocks), Sections: responseSystemSections(m.System), ToolsAdded: toolDefs, ToolsRemoved: removed})
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
					if b.Call.ArgumentsError != nil {
						return provider.Request{}, fmt.Errorf("provider replay refuses a tool call with invalid arguments")
					}
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

func responseToolDefinitions(tools []session.Tool) ([]provider.ToolDef, error) {
	out := make([]provider.ToolDef, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "" || len(tool.Parameters) == 0 || string(tool.Parameters) == "null" {
			return nil, fmt.Errorf("invalid schema for Responses tool %q", tool.Name)
		}
		var schema map[string]any
		if len(tool.Parameters) > 0 && string(tool.Parameters) != "null" {
			decoder := json.NewDecoder(strings.NewReader(string(tool.Parameters)))
			decoder.UseNumber()
			if err := decoder.Decode(&schema); err != nil || schema == nil {
				return nil, fmt.Errorf("invalid schema for Responses tool %q", tool.Name)
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return nil, fmt.Errorf("invalid schema for Responses tool %q", tool.Name)
			}
		}
		out = append(out, provider.ToolDef{Name: tool.Name, Description: tool.Description, Schema: schema})
	}
	return out, nil
}

func responseSystemSections(system *session.SystemMessage) []provider.SystemSection {
	if system == nil || len(system.Sections) == 0 {
		return nil
	}
	names := session.OrderedSectionNames(system.Sections)
	sections := make([]provider.SystemSection, 0, len(names))
	for _, name := range names {
		sections = append(sections, provider.SystemSection{Name: name, Value: system.Sections[name]})
	}
	return sections
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
