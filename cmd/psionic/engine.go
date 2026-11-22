package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/skills"
	"github.com/hypernewbie/psi/internal/tools"
)

const (
	conversationQueue   = 8
	maxConcurrentRuns   = 4
	eventRetention      = 24 * time.Hour
	maxHistoryInputSize = 40 << 10
)

type historyMessage struct {
	ID       string
	Author   string
	AuthorID string
	Text     string
}

type sourceTurn struct {
	message   inboundMessage
	text      string
	replyTo   string
	noAck     bool
	history   []historyMessage
	reference *historyMessage
}

type transport interface {
	conversationIO
	sendMessage(ctx context.Context, channelID, replyTo, text string) (string, error)
}

type referenceReader interface {
	message(ctx context.Context, channelID, messageID string) (historyMessage, error)
}

type providerFactory func(models *config.ModelsFile, agentDir, modelRef, compactionRef, sessionID string, maxRetries int) (*providerSet, error)

type runtime struct {
	settings       *fileSettings
	agent          selectedAgent
	policy         *accessPolicy
	models         *config.ModelsFile
	agentDir       string
	state          *stateStore
	defaults       config.Settings
	defaultReplies string
	groupReplies   string
	transport      transport
	logger         *log.Logger
	runTimeout     time.Duration
	historyMu      sync.RWMutex
	discordHistory int
	skills         []skills.Skill
	clients        providerFactory

	mu       sync.Mutex
	botMu    sync.RWMutex
	botID    string
	chats    map[string]*conversation
	runs     chan struct{}
	shutdown chan struct{}
	wg       sync.WaitGroup
}

// newRuntime prepares the shared configuration without creating a session,
// provider client, or network connection.
func newRuntime(settings *fileSettings, selected selectedAgent, models *config.ModelsFile, agentDir string, state *stateStore, defaults config.Settings, tr transport, logger *log.Logger) *runtime {
	defaultReplies, groupReplies := replyModes(settings)
	runTimeout := time.Duration(0)
	if worker := settings.Channels.Discord.InboundWorker; worker != nil && worker.RunTimeoutMs != nil && *worker.RunTimeoutMs > 0 {
		runTimeout = time.Duration(*worker.RunTimeoutMs) * time.Millisecond
	}
	available, diagnostics := discoverWorkspaceSkills(selected.Workspace)
	if len(diagnostics) > 0 {
		logger.Printf("skill diagnostics: %d", len(diagnostics))
	}
	return &runtime{
		settings: settings, agent: selected, policy: newAccessPolicy(settings),
		models: models, agentDir: agentDir, state: state, defaults: defaults,
		defaultReplies: defaultReplies, groupReplies: groupReplies,
		transport: tr, logger: logger, runTimeout: runTimeout, skills: available,
		clients: newProviderSet, chats: map[string]*conversation{},
		runs: make(chan struct{}, maxConcurrentRuns), shutdown: make(chan struct{}),
	}
}

func (r *runtime) close() {
	close(r.shutdown)
	r.mu.Lock()
	chats := make([]*conversation, 0, len(r.chats))
	for _, chat := range r.chats {
		chat.cancel()
		chats = append(chats, chat)
	}
	r.chats = map[string]*conversation{}
	r.mu.Unlock()
	r.wg.Wait()
	for _, chat := range chats {
		chat.close()
	}
}

// routeKey keeps each direct message, guild channel, and thread separate.
// Bot and agent identity scope a state directory that is reused accidentally.
func (r *runtime) routeKey(msg inboundMessage) string {
	botID := r.botUserID()
	prefix := ""
	if botID != "" || r.agent.ID != "" {
		prefix = botID + ":" + r.agent.ID + ":"
	}
	if msg.IsDM {
		switch r.dmScope() {
		case "main":
			return prefix + "dm:main"
		case "per-peer":
			return prefix + "dm:" + msg.AuthorID
		default:
			return prefix + "dm:" + msg.ChannelID + ":" + msg.AuthorID
		}
	}
	return prefix + "guild:" + msg.GuildID + ":" + msg.ChannelID
}

func (r *runtime) setBotID(id string) {
	r.botMu.Lock()
	r.botID = id
	r.botMu.Unlock()
}

func (r *runtime) setDiscordHistory(limit int) {
	r.historyMu.Lock()
	r.discordHistory = limit
	r.historyMu.Unlock()
}

func (r *runtime) historyLimit() int {
	r.historyMu.RLock()
	defer r.historyMu.RUnlock()
	return r.discordHistory
}

func (r *runtime) botUserID() string {
	r.botMu.RLock()
	defer r.botMu.RUnlock()
	return r.botID
}

func (r *runtime) isOwnerDM(msg inboundMessage) bool {
	return msg.IsDM && r.policy.isOwner(msg.AuthorID)
}

// toolPolicy applies route-level guild and channel filters over the selected
// agent's policy. A channel never widens a guild or agent restriction.
func (r *runtime) routeTools(msg inboundMessage) toolPolicy {
	if msg.IsDM {
		return r.agent.Tools
	}
	route := toolPolicy{}
	guild, ok := r.policy.guild(msg.GuildID)
	if ok {
		mergeTools(&route, guild.Tools)
		channelKey := msg.ChannelID
		if msg.ParentID != "" {
			channelKey = msg.ParentID
		}
		channel, _ := guild.Channel(channelKey)
		if channel != nil {
			route = narrowPolicy(route, channel.Tools)
		}
	}
	return narrowTools(route, r.agent.Tools)
}

// narrowPolicy applies a channel filter over the guild policy. The channel can
// remove a tool from the guild, but cannot add one the guild did not allow.
func narrowPolicy(base toolPolicy, channel *toolPolicy) toolPolicy {
	if channel == nil {
		return base
	}
	for _, tool := range implementedTools {
		if !channel.allows(tool) {
			base.Deny = append(base.Deny, tool)
		}
	}
	return base
}

// narrowTools applies route filters on top of the agent policy. A route may
// remove a tool but never add one that the selected agent disallows.
func narrowTools(route, agent toolPolicy) toolPolicy {
	for _, tool := range implementedTools {
		if !agent.allows(tool) {
			route.Deny = append(route.Deny, tool)
		}
	}
	return route
}
func (r *runtime) conversation(key string) (*conversation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.shutdown:
		return nil, errors.New("runtime is shutting down")
	default:
	}
	if chat, ok := r.chats[key]; ok {
		return chat, nil
	}
	chat := &conversation{key: key, rt: r, turns: make(chan sourceTurn, conversationQueue)}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	r.chats[key] = chat
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		chat.worker()
	}()
	return chat, nil
}

// conversation owns one serialized conversation and its Psi session.
type conversation struct {
	key    string
	rt     *runtime
	ctx    context.Context
	turns  chan sourceTurn
	cancel context.CancelFunc

	mu            sync.Mutex
	store         *session.Store
	clients       *providerSet
	loop          *agent.Loop
	modelOverride string
	started       time.Time
	last          time.Time
	private       bool
	scope         string
	running       bool
	runStop       context.CancelFunc
	closed        bool
}

// modelRef returns the effective model reference for this conversation: the
// per-route /model override when set, otherwise the process default.
func (c *conversation) modelRef() string {
	if c.modelOverride != "" {
		return c.modelOverride
	}
	return c.rt.agent.Model
}

func (c *conversation) enqueue(turn sourceTurn) bool {
	select {
	case c.turns <- turn:
		return true
	default:
		return false
	}
}

// stop cancels the active run only. Queued turns remain for the worker.
func (c *conversation) busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running || c.runStop != nil
}

func (c *conversation) stop() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runStop == nil {
		return false
	}
	c.runStop()
	return true
}

func (c *conversation) worker() {
	for {
		select {
		case <-c.rt.shutdown:
			return
		default:
		}
		select {
		case <-c.rt.shutdown:
			return
		case turn, ok := <-c.turns:
			if !ok || c.ctx.Err() != nil {
				return
			}
			select {
			case <-c.rt.shutdown:
				return
			default:
			}
			c.handle(turn)
		}
	}
}

func (c *conversation) close() {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.clients != nil {
		c.clients.close()
		c.clients = nil
	}
	if c.store != nil {
		c.store.Unlock()
	}
	c.loop, c.store = nil, nil
}

// prepare opens or refreshes the Psi session before a run.
func (c *conversation) prepare(msg inboundMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("conversation is closed")
	}
	if c.rt.replyMode(msg) == "message_tool" && !c.rt.routeTools(msg).allows("message") {
		return errors.New("visible replies require the message tool")
	}
	now := time.Now()
	private := c.rt.isOwnerDM(msg)
	scope := scopeKey(c.rt.routeTools(msg))
	policy := resolveResetPolicy(c.rt.settings.Session, resetType(msg), c.rt.resetOverride(msg.ChannelID))
	if c.store != nil {
		fresh, _ := sessionFresh(c.started, c.last, now, policy)
		if fresh && c.private == private && c.scope == scope {
			return nil
		}
		c.retireLocked()
		if err := c.rt.state.deleteRoute(c.key); err != nil {
			return err
		}
	}
	state, found, err := c.rt.state.route(c.key)
	if err != nil {
		return err
	}
	// The /model choice is a route preference: it survives session resets and
	// scope changes, which discard conversation content but not this setting.
	modelChoice := state.Model
	started, last := now, now
	if found && state.SessionID != "" && (state.Private != private || state.Workspace != c.rt.agent.Workspace || state.Scope != scope) {
		if err := c.rt.state.deleteRoute(c.key); err != nil {
			return err
		}
		state, found = routeState{}, false
	}
	if found && state.SessionID != "" {
		started = time.UnixMilli(state.StartedAt)
		last = time.UnixMilli(state.LastInteraction)
		if state.StartedAt == 0 {
			started = now
		}
		if state.LastInteraction == 0 {
			last = started
		}
		if fresh, _ := sessionFresh(started, last, now, policy); !fresh {
			if err := c.rt.state.deleteRoute(c.key); err != nil {
				return err
			}
			state, found = routeState{}, false
		}
	}
	dir := c.rt.state.routeDir(c.key)
	if err := ensureRouteDir(dir); err != nil {
		return err
	}
	var store *session.Store
	if found && state.SessionID != "" {
		path, err := c.rt.state.checkSessionPath(dir, state.SessionID)
		if err != nil {
			return fmt.Errorf("stored conversation is unavailable: %w", err)
		}
		store, err = session.OpenFile(path, c.rt.agent.Workspace, true)
		if err != nil {
			return fmt.Errorf("open conversation: %w", err)
		}
		if err := store.TryLock(); err != nil {
			store.Unlock()
			return fmt.Errorf("lock conversation: %w", err)
		}
		c.started, c.last = started, last
	} else {
		store, err = session.New(session.OpenOptions{
			Cwd: c.rt.agent.Workspace, SessionDir: dir, AgentDir: c.rt.agentDir,
			Persist: true,
		})
		if err != nil {
			return fmt.Errorf("new conversation: %w", err)
		}
		if err := store.TryLock(); err != nil {
			store.Unlock()
			return fmt.Errorf("lock conversation: %w", err)
		}
		c.started = now
		c.last = now
	}
	build := c.rt.clients
	if build == nil {
		build = newProviderSet
	}
	c.modelOverride = modelChoice
	clients, err := build(c.rt.models, c.rt.agentDir, c.modelRef(), c.rt.agent.Compaction, store.Header().ID, c.rt.defaults.MaxRetries)
	if err != nil {
		store.Unlock()
		return err
	}
	c.store, c.clients, c.private, c.scope = store, clients, private, scope
	c.loop = agent.New(store, tools.NewRegistry(c.rt.agent.Workspace), clients.main, nil)
	if err := c.rt.state.saveRoute(c.key, routeState{
		SessionID: store.Header().ID, StartedAt: c.started.UnixMilli(), LastInteraction: c.last.UnixMilli(),
		Private: private, Workspace: c.rt.agent.Workspace, Scope: scope, Model: modelChoice,
	}); err != nil {
		c.retireLocked()
		return err
	}
	return nil
}

func (c *conversation) retireLocked() {
	if c.clients != nil {
		c.clients.close()
	}
	if c.store != nil {
		c.store.Unlock()
	}
	c.store, c.clients, c.loop = nil, nil, nil
}

// resetOverride prefers a channel-specific entry, then the Discord provider
// entry. An explicit override replaces per-type policy for this conversation.
func (r *runtime) resetOverride(channelID string) *resetSettings {
	if r.settings.Session == nil || r.settings.Session.ByChannel == nil {
		return nil
	}
	if override, ok := r.settings.Session.ByChannel[channelID]; ok {
		return override
	}
	return r.settings.Session.ByChannel["discord"]
}

// buildTurnConfig composes one turn's prompt sections, tools, and agent
// config from the route's scoping rules. The returned active turn carries the
// staged message state for message_tool replies.
func (c *conversation) buildTurnConfig(msg inboundMessage) (agent.Config, *activeTurn, error) {
	includeMemory := c.rt.isOwnerDM(msg)
	files, err := loadWorkspaceFiles(c.rt.agent.Workspace, includeMemory)
	if err != nil {
		return agent.Config{}, nil, err
	}
	replyMode := c.rt.replyMode(msg)
	effectiveTools := c.rt.routeTools(msg)
	active := &activeTurn{channelID: msg.ChannelID}
	availableSkills := c.rt.skills
	if !effectiveTools.allows("skill") {
		availableSkills = nil
	}
	toolsForTurn, err := buildTools(c.rt.agent.Workspace, includeMemory, replyMode == "message_tool", effectiveTools, availableSkills, active, c.rt.transport)
	if err != nil {
		return agent.Config{}, nil, err
	}
	names := make([]string, 0, len(toolsForTurn.definitions))
	for _, def := range toolsForTurn.definitions {
		names = append(names, def.Name)
	}
	sections := buildPromptSections(c.rt.agent, files, names, availableSkills, replyMode)
	return c.rt.agentConfig(c.clients, toolsForTurn, sections), active, nil
}

func resetType(msg inboundMessage) string {
	if msg.IsDM {
		return "direct"
	}
	if msg.IsThread {
		return "thread"
	}
	return "group"
}

func (c *conversation) handle(turn sourceTurn) {
	if command, arg, ok := commandWithArg(turn.text); ok {
		// A recognized command is either handled or rejected. It is never
		// forwarded to the model as ordinary text.
		c.handleCommand(turn, command, arg)
		return
	}
	c.mu.Lock()
	c.running = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()
	if err := c.prepare(turn.message); err != nil {
		c.rt.logger.Printf("conversation %s: unavailable (%T)", shortKey(c.key), err)
		c.rt.sendNotice(turn, "This conversation is temporarily unavailable.")
		return
	}
	msg := turn.message
	turn.history = c.rt.turnHistory(c.ctx, turn)
	if msg.ReferenceID != "" && msg.ReferenceID != msg.MessageID {
		if reader, ok := c.rt.transport.(referenceReader); ok {
			if referenced, err := reader.message(c.ctx, msg.ChannelID, msg.ReferenceID); err == nil {
				turn.reference = &referenced
			}
		}
	}
	cfg, active, err := c.buildTurnConfig(msg)
	if err != nil {
		c.rt.logger.Printf("conversation %s: unavailable (%T)", shortKey(c.key), err)
		c.rt.sendNotice(turn, "The workspace files are temporarily unavailable.")
		return
	}

	projection, err := c.store.Projection()
	if err != nil {
		c.rt.logger.Printf("conversation %s: projection failed (%T)", shortKey(c.key), err)
		c.rt.sendNotice(turn, "The conversation is temporarily unavailable.")
		return
	}
	before := len(projection.Flat)
	select {
	case c.rt.runs <- struct{}{}:
		defer func() { <-c.rt.runs }()
	case <-c.ctx.Done():
		return
	}
	runCtx, cancel := context.WithCancel(c.ctx)
	if c.rt.runTimeout > 0 {
		runCtx, cancel = context.WithTimeout(c.ctx, c.rt.runTimeout)
	}
	c.mu.Lock()
	c.runStop = cancel
	c.mu.Unlock()
	err = c.loop.SubmitContent(runCtx, agent.UserInput{Content: turnContent(turn), Display: turn.text}, cfg)
	c.mu.Lock()
	c.runStop = nil
	c.last = time.Now()
	c.mu.Unlock()
	cancel()
	if err != nil {
		c.persistActivity()
		if errors.Is(err, context.Canceled) {
			return
		}
		c.rt.logger.Printf("conversation %s: run failed (%T)", shortKey(c.key), err)
		c.rt.sendNotice(turn, "The request failed.")
		return
	}
	c.persistActivity()
	if c.rt.replyMode(msg) == "message_tool" {
		if active.stagedOK && strings.TrimSpace(active.staged) != "" {
			if err := c.rt.deliver(turn, active.staged); err != nil {
				c.rt.logger.Printf("conversation %s: delivery failed (%T)", shortKey(c.key), err)
			}
			return
		}
		c.rt.logger.Printf("conversation %s: no visible reply was staged", shortKey(c.key))
		return
	}
	answer := assistantTextAfter(c.store, before)
	if strings.TrimSpace(answer) == "" || strings.TrimSpace(answer) == "NO_REPLY" {
		return
	}
	if err := c.rt.deliver(turn, answer); err != nil {
		c.rt.logger.Printf("conversation %s: delivery failed (%T)", shortKey(c.key), err)
	}
}

func (c *conversation) status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return "no session"
	}
	return "session active"
}

func (c *conversation) persistActivity() {
	c.mu.Lock()
	state := routeState{
		StartedAt: c.started.UnixMilli(), LastInteraction: c.last.UnixMilli(),
		Private: c.private, Workspace: c.rt.agent.Workspace, Scope: c.scope, Model: c.modelOverride,
	}
	if c.store != nil {
		state.SessionID = c.store.Header().ID
	}
	c.mu.Unlock()
	if state.SessionID == "" {
		return
	}
	if err := c.rt.state.saveRoute(c.key, state); err != nil {
		c.rt.logger.Printf("conversation %s: save state failed (%T)", shortKey(c.key), err)
	}
}

func (r *runtime) agentConfig(clients *providerSet, toolSet toolSet, sections []config.PromptSection) agent.Config {
	internal := make(map[string]agent.InternalToolHandler, len(toolSet.handlers))
	for name, handler := range toolSet.handlers {
		internal[name] = agent.InternalToolHandler(handler)
	}
	thinking := config.ResolveThinkingLevel(r.defaults, clients.model.Provider, clients.model.ID, "", false)
	if thinking == "" {
		thinking = "off"
	}
	thinking = provider.ClampThinkingLevel(clients.model, thinking)
	cfg := agent.Config{
		Model: clients.model, Thinking: thinking,
		Tools: toolSet.definitions, System: config.RenderSections(sections), Sections: sections,
		AutoCompact: r.defaults.Compaction.Enabled, ReserveTokens: r.defaults.Compaction.ReserveTokens,
		KeepRecentTokens: r.defaults.Compaction.KeepRecentTokens,
		InternalTools:    internal,
		RetryAttempts:    r.defaults.MaxRetries, RetryBaseDelayMs: r.defaults.RetryBaseDelayMs,
		RetryMaxAgentDelayMs: r.defaults.RetryMaxAgentDelayMs,
	}
	if clients.compactor != nil {
		cfg.CompactionProvider = func() (provider.Provider, provider.ModelMeta, func(), error) {
			return clients.compactor, clients.compact, func() {}, nil
		}
	}
	return cfg
}

func (r *runtime) deliver(turn sourceTurn, text string) error {
	select {
	case <-r.shutdown:
		return errors.New("runtime is shutting down")
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := r.transport.sendMessage(ctx, turn.message.ChannelID, turn.replyTo, text)
	return err
}

// sendNotice reports a local failure unless the route intentionally keeps the
// final text private. A notice is never sent after an uncertain delivery.
func (r *runtime) sendNotice(turn sourceTurn, text string) {
	if r.replyMode(turn.message) == "message_tool" {
		return
	}
	if err := r.deliver(turn, text); err != nil {
		r.logger.Printf("delivery failed (%T)", err)
	}
}

func discoverWorkspaceSkills(workspace string) ([]skills.Skill, []skills.Diagnostic) {
	dir := filepath.Join(workspace, "skills")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || !inside(workspace, resolved) {
		return nil, nil
	}
	found, diagnostics := skills.DiscoverPaths([]string{dir})
	available := make([]skills.Skill, 0, len(found))
	for _, skill := range found {
		if skill.Disable {
			continue
		}
		file, err := filepath.EvalSymlinks(skill.FilePath)
		if err != nil || !inside(workspace, file) {
			continue
		}
		base, err := filepath.EvalSymlinks(skill.BaseDir)
		if err != nil || !inside(workspace, base) {
			continue
		}
		available = append(available, skill)
	}
	return available, diagnostics
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// replyMode applies the group-specific override without changing direct
// messages.
func (r *runtime) replyMode(msg inboundMessage) string {
	if !msg.IsDM && r.groupReplies != "" {
		return r.groupReplies
	}
	return r.defaultReplies
}

func (r *runtime) dmScope() string {
	if r.settings.Session == nil {
		return "per-channel-peer"
	}
	if r.settings.Session.DMScope == "" {
		return "per-channel-peer"
	}
	return r.settings.Session.DMScope
}

// assistantTextAfter ignores replies that predate this run. A tool-only or
// failed run therefore cannot resend an older answer.
func assistantTextAfter(store *session.Store, boundary int) string {
	projection, err := store.Projection()
	if err != nil {
		return ""
	}
	if boundary > len(projection.Flat) {
		// Compaction rewrote the projection. The pre-run boundary no longer
		// identifies this turn, so do not replay an older answer.
		return ""
	}
	for i := len(projection.Flat) - 1; i >= boundary; i-- {
		message := projection.Flat[i]
		if message.Assistant == nil || message.Assistant.StopReason == "toolUse" {
			continue
		}
		var b strings.Builder
		for _, block := range message.Assistant.Content {
			if block.Text != nil {
				b.WriteString(block.Text.Text)
			}
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

func turnContent(turn sourceTurn) string {
	if len(turn.history) == 0 && turn.reference == nil {
		return turn.text
	}
	var b strings.Builder
	if turn.reference != nil {
		b.WriteString("<reply_context>\n")
		fmt.Fprintf(&b, "[%s] %s: %s\n", turn.reference.ID, turn.reference.Author, turn.reference.Text)
		b.WriteString("</reply_context>\n\n")
	}
	if len(turn.history) > 0 {
		history := boundedHistory(turn.history, maxHistoryInputSize)
		b.WriteString("<conversation_history>\n")
		for _, message := range history {
			fmt.Fprintf(&b, "[%s] %s: %s\n", message.ID, message.Author, message.Text)
		}
		b.WriteString("</conversation_history>\n\n")
	}
	b.WriteString(turn.text)
	return b.String()
}

func boundedHistory(messages []historyMessage, limit int) []historyMessage {
	total := 0
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		total += len(messages[i].Author) + len(messages[i].Text) + len(messages[i].ID) + 4
		if total > limit {
			break
		}
		start = i
	}
	return messages[start:]
}

func scopeKey(policy toolPolicy) string {
	raw, err := json.Marshal(policy)
	if err != nil {
		return "invalid"
	}
	return string(raw)
}

func shortKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}
