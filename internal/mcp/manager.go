package mcp

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/tools"
)

const (
	serverStateDisabled     = "disabled"
	serverStateConnecting   = "connecting"
	serverStateReady        = "ready"
	serverStateReconnecting = "reconnecting"
	serverStateFailed       = "failed"
	serverStateClosing      = "closing"

	maxSetupWorkers    = 4
	maxCallsPerServer  = 4
	maxCallsPerManager = 8
	maxReconnects      = 2
	maxDiscoveryTries  = 3 // initial attempt plus two retries
	maxConfigWarnings  = 20
)

// Input is the immutable policy for one reload generation.
type Input struct {
	UserPath             string
	ProjectPath          string
	ProjectTrusted       bool
	AllowProjectCommands bool
	Cwd                  string
	ReadAvailable        bool
}

// Options configures one manager.
type Options struct {
	Input
	Version string
	// roots optionally supplies test-only trusted roots; production uses system roots.
	roots *x509.CertPool
	// now and sleep are test seams for retry policy. Production leaves them nil.
	now   func() time.Time
	sleep func(context.Context, time.Duration) bool
}

// ServerStatus is the safe status shown by /mcp.
type ServerStatus struct {
	Name           string
	Transport      string
	State          string
	Discovered     int
	Exposed        int
	ErrorClass     string
	RefreshWarning string
	// NotificationsUnavailable reports a standalone listener that the server
	// rejected or answered with a non-SSE response.
	NotificationsUnavailable bool
}

// Snapshot is an immutable per-request view of declarations and routes.
type Snapshot struct {
	Revision    uint64
	Servers     []ServerStatus
	Tools       []Tool
	ReloadError string
	Warnings    []string
}

// Find returns the declared route for a model tool name.
func (s *Snapshot) Find(name string) (Tool, bool) {
	if s == nil {
		return Tool{}, false
	}
	for _, tool := range s.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

// Definitions converts the snapshot's declarations for a provider request.
func (s *Snapshot) Definitions() []provider.ToolDef {
	if s == nil || len(s.Tools) == 0 {
		return nil
	}
	defs := make([]provider.ToolDef, 0, len(s.Tools))
	for _, tool := range s.Tools {
		defs = append(defs, provider.ToolDef{Name: tool.Name, Description: tool.Description, Schema: tool.ToolDefSchema()})
	}
	return defs
}

type serverState struct {
	cfg                 Server
	status              string
	errClass            string
	epoch               uint64
	conn                *connection
	catalog             []Tool
	setupCancel         context.CancelFunc
	setupActive         bool
	resolved            Server
	resolvedValid       bool
	reconnects          int
	refreshWarning      string
	listenerUnavailable bool
}

type connection struct {
	manager  *Manager
	server   string
	epoch    uint64
	session  *sdk.ClientSession
	ctx      context.Context
	cancel   context.CancelFunc
	permits  chan struct{}
	listCh   chan struct{}
	lastList time.Time
	catalog  []Tool

	mu                  sync.Mutex
	tokens              map[string]*callState
	errClass            atomic.Value
	closed              atomic.Bool
	closing             atomic.Bool
	retired             atomic.Bool
	sessionEnded        atomic.Bool
	transient           atomic.Bool
	listenerUnavailable atomic.Bool
	setupDisarm         func() bool
	retryMu             sync.Mutex
	retryDelay          time.Duration
	retryStop           bool
	listenerStarted     chan struct{}
	listenerOnce        sync.Once
}

type callState struct {
	token        string
	timeout      time.Duration
	timer        *time.Timer
	mu           sync.Mutex
	retired      bool
	hasProgress  bool
	lastProgress float64
	idle         atomic.Bool
}

func (c *callState) advanceProgress(progress float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// MCP progress must advance for the same token. Repeated and out-of-order
	// notifications do not keep a stalled call alive.
	if c.retired || (c.hasProgress && progress <= c.lastProgress) {
		return
	}
	c.hasProgress = true
	c.lastProgress = progress
	if c.timer != nil {
		c.timer.Reset(c.timeout)
	}
}

func (c *callState) retire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.retired = true
	if c.timer != nil {
		c.timer.Stop()
	}
}

type reloadRequest struct {
	generation uint64
	input      Input
}

type reloadResolution struct {
	resolved Server
	err      error
}

// Manager owns SDK sessions, subprocesses, and background work. Every public
// read returns an immutable snapshot; nothing waits for a server.
type Manager struct {
	opts Options

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	servers    map[string]*serverState
	order      []string
	generation uint64
	lastEpoch  uint64
	reloadErr  string
	warnings   []string
	input      Input
	started    bool
	stopped    bool
	stopOnce   sync.Once

	snapshot       atomic.Pointer[Snapshot]
	snapshotChange chan struct{}
	reloads        chan reloadRequest
	setupSlot      chan struct{}
	callSlot       chan struct{}
	callSeq        atomic.Uint64
	pending        int
	idle           *sync.Cond
}

// NewManager creates a manager. Call Start to schedule configuration loading.
func NewManager(opts Options) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		opts:           opts,
		ctx:            ctx,
		cancel:         cancel,
		servers:        map[string]*serverState{},
		input:          opts.Input,
		snapshotChange: make(chan struct{}, 1),
		reloads:        make(chan reloadRequest, 1),
		setupSlot:      make(chan struct{}, maxSetupWorkers),
		callSlot:       make(chan struct{}, maxCallsPerManager),
	}
	m.snapshot.Store(&Snapshot{})
	m.idle = sync.NewCond(&m.mu)
	if m.opts.now == nil {
		m.opts.now = time.Now
	}
	if m.opts.sleep == nil {
		m.opts.sleep = sleepContext
	}
	return m
}

// Start begins the background reload worker and schedules initial setup.
func (m *Manager) Start() {
	m.mu.Lock()
	if m.started || m.stopped {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.enqueueReloadLocked()
	m.pending++
	go m.reloadLoop()
	m.mu.Unlock()
}

func (m *Manager) reloadLoop() {
	defer m.workerDone()
	for {
		select {
		case req := <-m.reloads:
			m.applyReload(req)
		case <-m.ctx.Done():
			return
		}
	}
}

func (m *Manager) enqueueReloadLocked() {
	m.generation++
	// Producers hold m.mu. The worker only drains the one-slot queue, so
	// nothing can refill it between dropping the old request and sending.
	select {
	case <-m.reloads:
	default:
	}
	m.reloads <- reloadRequest{generation: m.generation, input: m.input}
}

// Reload schedules a fresh config read. It never waits for I/O.
func (m *Manager) Reload(input Input) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	m.input = input
	m.enqueueReloadLocked()
}

func (m *Manager) applyReload(req reloadRequest) {
	cfg, err := LoadConfig(req.input.UserPath, req.input.ProjectPath, req.input.ProjectTrusted)
	var resolutions map[string]reloadResolution
	if err == nil {
		resolutions = m.prepareReloadResolutions(cfg, req.input)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.generation != m.generation || m.stopped {
		return
	}
	if err != nil {
		m.reloadErr = ErrorClass(err)
		m.applyPolicyLossLocked(req.input)
	} else {
		m.reloadErr = ""
		m.warnings = append(m.warnings[:0], cfg.Warnings...)
		m.acceptConfigLocked(cfg, req.input, resolutions)
	}
	m.publishLocked()
}

// prepareReloadResolutions re-resolves only unchanged connections that use
// env/header interpolation. Commands stay in setup so an explicit reload
// still runs them exactly once. This work is outside the manager lock because
// a command can take its full ten-second budget.
func (m *Manager) prepareReloadResolutions(cfg *Config, input Input) map[string]reloadResolution {
	m.mu.Lock()
	previous := make(map[string]Server, len(cfg.Servers))
	for _, server := range cfg.Servers {
		if state := m.servers[server.Name]; state != nil {
			previous[server.Name] = state.cfg
		}
	}
	m.mu.Unlock()
	resolutions := make(map[string]reloadResolution)
	for _, server := range cfg.Servers {
		old, ok := previous[server.Name]
		if !ok || !old.Enabled || !server.Enabled || !sameConnectionConfig(old, server) {
			continue
		}
		if serverUsesCommand(server) || !serverNeedsInterpolation(server) {
			continue
		}
		ctx, cancel := context.WithTimeout(m.ctx, server.Timeout)
		resolved, err := Resolve(ctx, server, input.AllowProjectCommands)
		cancel()
		resolutions[server.Name] = reloadResolution{resolved: resolved, err: err}
	}
	return resolutions
}

func serverNeedsInterpolation(server Server) bool {
	for _, value := range server.Env {
		if variablePattern.MatchString(value) {
			return true
		}
	}
	for _, value := range server.Headers {
		if variablePattern.MatchString(value) {
			return true
		}
	}
	return false
}

func resolvedValuesEqual(a, b Server) bool {
	return maps.Equal(a.Env, b.Env) && maps.Equal(a.Headers, b.Headers)
}

// applyPolicyLossLocked withdraws connections that the current policy can no
// longer permit, even when the config file itself is malformed.
func (m *Manager) applyPolicyLossLocked(input Input) {
	for _, state := range m.servers {
		loss := false
		if !input.ProjectTrusted && state.cfg.Source == "project" {
			loss = true
		}
		if !input.AllowProjectCommands && state.cfg.ProjectCommand {
			loss = true
		}
		if !loss {
			continue
		}
		m.withdrawLocked(state, "permission")
	}
}

func (m *Manager) acceptConfigLocked(cfg *Config, input Input, resolutions map[string]reloadResolution) {
	newServers := make(map[string]*serverState, len(cfg.Servers))
	newOrder := make([]string, 0, len(cfg.Servers))
	for _, server := range cfg.Servers {
		newOrder = append(newOrder, server.Name)
		old := m.servers[server.Name]
		if old == nil {
			state := &serverState{cfg: server, status: serverStateConnecting, epoch: m.nextEpochLocked()}
			newServers[server.Name] = state
			continue
		}
		if !server.Enabled {
			m.withdrawLocked(old, "")
			old.cfg = server
			old.resolved = Server{}
			old.resolvedValid = false
			old.reconnects = 0
			old.status = serverStateDisabled
			old.errClass = ""
			old.refreshWarning = ""
			old.listenerUnavailable = false
			newServers[server.Name] = old
			continue
		}
		hasCommand := serverUsesCommand(server)
		changed := !sameConnectionConfig(old.cfg, server) || hasCommand || old.status == serverStateFailed ||
			(!input.ProjectTrusted && old.cfg.Source == "project") ||
			(!input.AllowProjectCommands && old.cfg.ProjectCommand)
		if changed {
			m.withdrawLocked(old, "")
			old.cfg = server
			old.catalog = nil
			old.reconnects = 0
			old.resolvedValid = false
			old.status = serverStateConnecting
			old.errClass = ""
			old.refreshWarning = ""
			old.listenerUnavailable = false
			newServers[server.Name] = old
			continue
		}
		if resolution, ok := resolutions[server.Name]; ok {
			if resolution.err != nil {
				m.withdrawLocked(old, "")
				old.cfg = server
				old.catalog = nil
				old.resolvedValid = false
				old.resolved = Server{}
				old.status = serverStateFailed
				old.errClass = ErrorClass(resolution.err)
				old.refreshWarning = ""
				old.listenerUnavailable = false
				newServers[server.Name] = old
				continue
			}
			if !old.resolvedValid || !resolvedValuesEqual(old.resolved, resolution.resolved) {
				m.withdrawLocked(old, "")
				old.cfg = server
				old.catalog = nil
				old.reconnects = 0
				old.resolved = resolution.resolved
				old.resolvedValid = true
				old.status = serverStateConnecting
				old.errClass = ""
				old.refreshWarning = ""
				old.listenerUnavailable = false
				newServers[server.Name] = old
				continue
			}
			old.resolved = resolution.resolved
		}
		// Unchanged connection: keep its epoch and notification callbacks,
		// apply the newest exposure policy, and start a fresh reload allowance.
		old.cfg = server
		old.reconnects = 0
		old.catalog = recomputeExposure(old.catalog, server)
		if old.conn != nil {
			old.conn.catalog = old.catalog
		}
		if old.status == serverStateDisabled {
			old.status = serverStateConnecting
		}
		newServers[server.Name] = old
	}
	for name, state := range m.servers {
		if _, keep := newServers[name]; keep {
			continue
		}
		m.withdrawLocked(state, "")
	}
	m.servers = newServers
	m.order = newOrder
	for _, name := range m.order {
		state := m.servers[name]
		if state.conn == nil && state.cfg.Enabled && state.status == serverStateConnecting && !state.setupActive {
			m.scheduleSetupLocked(state)
		}
	}
}

func serverUsesCommand(server Server) bool {
	for _, value := range server.Env {
		if isCommand(value) {
			return true
		}
	}
	for _, value := range server.Headers {
		if isCommand(value) {
			return true
		}
	}
	return false
}

func recomputeExposure(catalog []Tool, server Server) []Tool {
	if catalog == nil {
		return nil
	}
	out := make([]Tool, len(catalog))
	for i, tool := range catalog {
		out[i] = tool
		out[i].Exposure = effectiveExposure(server, tool.RawName)
	}
	return out
}

// Exposure and call timeouts can change without replacing a connection.
// Keep connection-defining fields separate: env and headers are not interchangeable.
func sameConnectionConfig(a, b Server) bool {
	return a.Transport == b.Transport && a.Command == b.Command && slices.Equal(a.Args, b.Args) &&
		a.Cwd == b.Cwd && a.URL == b.URL && maps.Equal(a.Env, b.Env) && maps.Equal(a.Headers, b.Headers) &&
		a.Source == b.Source && a.ProjectCommand == b.ProjectCommand
}

func (m *Manager) scheduleSetupLocked(state *serverState) {
	if m.stopped || state.setupActive {
		return
	}
	state.setupActive = true
	epoch := state.epoch
	ctx, cancel := context.WithCancel(m.ctx)
	state.setupCancel = cancel
	m.pending++
	go func() {
		defer m.workerDone()
		defer cancel()
		defer func() {
			m.mu.Lock()
			if m.servers[state.cfg.Name] == state && state.epoch == epoch {
				state.setupActive = false
			}
			m.mu.Unlock()
		}()
		select {
		case m.setupSlot <- struct{}{}:
			defer func() { <-m.setupSlot }()
		case <-ctx.Done():
			return
		}
		m.runSetup(state, epoch, ctx)
	}()
}

func (m *Manager) runSetup(state *serverState, epoch uint64, parent context.Context) {
	m.mu.Lock()
	input := m.input
	server := state.cfg
	resolved, resolvedValid := state.resolved, state.resolvedValid
	m.mu.Unlock()
	if !policyAllowsInput(input, server) {
		m.setupFailed(state, epoch, configErr("permission", "MCP server is no longer permitted"))
		return
	}
	ctx, cancel := context.WithTimeout(parent, server.Timeout)
	defer cancel()
	if !resolvedValid {
		var err error
		resolved, err = Resolve(ctx, server, input.AllowProjectCommands)
		if err != nil {
			m.setupFailed(state, epoch, err)
			return
		}
		m.mu.Lock()
		if m.servers[state.cfg.Name] == state && state.epoch == epoch {
			state.resolved = resolved
			state.resolvedValid = true
		}
		m.mu.Unlock()
	}
	var session *sdk.ClientSession
	var conn *connection
	var err error
	for attempt := 0; attempt < maxDiscoveryTries; attempt++ {
		session, conn, err = m.connect(ctx, state, epoch, resolved, input)
		if err == nil {
			break
		}
		if ctx.Err() != nil || !retryableSetup(err) {
			break
		}
		delay, ok := m.discoveryRetryDelay(conn, attempt)
		if !ok || !m.opts.sleep(ctx, delay) {
			break
		}
	}
	if err != nil {
		m.setupFailed(state, epoch, err)
		return
	}
	m.publishConnection(state, epoch, conn)
	_ = session
}

func (m *Manager) discoveryRetryDelay(conn *connection, attempt int) (time.Duration, bool) {
	delay := discoveryBackoff(attempt + 1)
	if conn == nil {
		return delay, true
	}
	advice, ok := conn.retryAdvice()
	if !ok {
		return 0, false
	}
	if advice > delay {
		delay = advice
	}
	return delay, true
}

func discoveryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return 250 * time.Millisecond
	}
	return time.Second
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (m *Manager) connect(ctx context.Context, state *serverState, epoch uint64, server Server, input Input) (*sdk.ClientSession, *connection, error) {
	connCtx, connCancel := context.WithCancel(context.Background())
	conn := &connection{
		manager:         m,
		server:          server.Name,
		epoch:           epoch,
		ctx:             connCtx,
		cancel:          connCancel,
		permits:         make(chan struct{}, maxCallsPerServer),
		listCh:          make(chan struct{}, 1),
		tokens:          map[string]*callState{},
		listenerStarted: make(chan struct{}),
	}
	conn.errClass.Store("")
	if !policyAllowsInput(input, server) {
		connCancel()
		return nil, conn, configErr("permission", "MCP server is no longer permitted")
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "psi", Version: m.opts.Version}, &sdk.ClientOptions{
		Capabilities:   &sdk.ClientCapabilities{},
		MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
		ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) {
			conn.signalListChanged()
		},
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			if req != nil && req.Params != nil {
				conn.noteProgress(req.Params.ProgressToken, req.Params.Progress)
			}
		},
	})
	client.AddSendingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if obs := requestObservationFrom(ctx); obs != nil && obs.method == method {
				ctx = withOutboundObservation(ctx, obs)
			}
			return next(ctx, method, req)
		}
	})
	var transport sdk.Transport
	if server.Transport == TransportHTTP {
		transport = m.newHTTPTransport(server, conn)
	} else {
		transport = newStdioTransport(server, input.Cwd)
	}
	conn.clearRetryAdvice()
	var disarm func() bool
	if server.Transport == TransportHTTP {
		// The SDK detaches its standalone GET from the setup context. Keep
		// the setup budget armed until publication disarms it.
		disarm = context.AfterFunc(ctx, connCancel)
	}
	stopDisarm := func() {
		if disarm != nil {
			disarm()
			disarm = nil
		}
	}
	session, err := client.Connect(ctx, transport, &sdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		stopDisarm()
		if class := conn.latchedClass(); class != "" {
			err = configErr(class, "MCP connection failed: %s", class)
		} else if conn.transient.Load() {
			err = &transientError{err: err}
		}
		connCancel()
		return nil, conn, err
	}
	if class := conn.terminalClass(); class != "" {
		stopDisarm()
		_ = session.Close()
		connCancel()
		return nil, conn, configErr(class, "MCP connection failed: %s", class)
	}
	init := session.InitializeResult()
	if init != nil && protocolTooNew(init.ProtocolVersion) {
		stopDisarm()
		_ = session.Close()
		connCancel()
		return nil, conn, configErr("unsupported", "server selected an unsupported protocol version")
	}
	conn.clearRetryAdvice()
	raw, err := listAllTools(ctx, session)
	if err != nil {
		stopDisarm()
		if class := conn.latchedClass(); class != "" {
			err = configErr(class, "MCP discovery failed: %s", class)
		} else if conn.transient.Load() {
			err = &transientError{err: err}
		}
		_ = session.Close()
		connCancel()
		return nil, conn, err
	}
	if server.Transport == TransportHTTP {
		select {
		case <-conn.listenerStarted:
		case <-ctx.Done():
			stopDisarm()
			_ = session.Close()
			connCancel()
			return nil, conn, ctx.Err()
		}
	}
	if class := conn.terminalClass(); class != "" {
		stopDisarm()
		_ = session.Close()
		connCancel()
		return nil, conn, configErr(class, "MCP discovery failed: %s", class)
	}
	catalog, err := buildCatalog(server, epoch, raw)
	if err != nil {
		stopDisarm()
		_ = session.Close()
		connCancel()
		return nil, conn, err
	}
	conn.session = session
	conn.catalog = catalog
	conn.setupDisarm = disarm
	m.mu.Lock()
	if m.stopped || m.servers[state.cfg.Name] != state || state.epoch != epoch {
		m.mu.Unlock()
		_ = session.Close()
		connCancel()
		return nil, conn, context.Canceled
	}
	conn.lastList = m.opts.now()
	m.mu.Unlock()
	return session, conn, nil
}

func protocolTooNew(version string) bool {
	return version >= "2026-07-28"
}

func listAllTools(ctx context.Context, session *sdk.ClientSession) ([]*sdk.Tool, error) {
	var tools []*sdk.Tool
	var total int
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < maxCatalogPages; page++ {
		result, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, configErr("catalog", "empty tools/list result")
		}
		for _, tool := range result.Tools {
			if tool == nil {
				continue
			}
			data, err := json.Marshal(tool)
			if err != nil {
				return nil, configErr("catalog", "tool declaration could not be encoded")
			}
			total += len(data)
			if total > maxCatalogBytes {
				return nil, errLimit("tool catalog")
			}
			tools = append(tools, tool)
		}
		if result.NextCursor == "" {
			return tools, nil
		}
		if seen[result.NextCursor] {
			return nil, configErr("catalog", "tools/list repeated a cursor")
		}
		seen[result.NextCursor] = true
		cursor = result.NextCursor
	}
	return nil, errLimit("tool catalog pages")
}

func policyAllowsInput(input Input, server Server) bool {
	return (input.ProjectTrusted || server.Source != "project") &&
		(input.AllowProjectCommands || !server.ProjectCommand)
}

func (m *Manager) policyAllows(server Server) bool {
	return policyAllowsInput(m.input, server)
}

func (m *Manager) publishConnection(state *serverState, epoch uint64, conn *connection) {
	m.mu.Lock()
	if m.stopped || m.servers[state.cfg.Name] != state || state.epoch != epoch || !state.cfg.Enabled {
		conn.closeAsyncLocked()
		m.mu.Unlock()
		return
	}
	if !m.policyAllows(state.cfg) {
		// A newer reload can restore trust before the revoking config loads.
		// Retire this setup now so that reload can start a replacement.
		m.withdrawLocked(state, "permission")
		conn.closeAsyncLocked()
		m.publishLocked()
		m.mu.Unlock()
		return
	}
	if state.conn != nil && state.conn != conn {
		state.conn.closeAsyncLocked()
	}
	if conn.setupDisarm != nil {
		conn.setupDisarm()
		conn.setupDisarm = nil
	}
	state.conn = conn
	state.status = serverStateReady
	state.errClass = ""
	state.refreshWarning = ""
	// Setup may predate an exposure-only reload that preserves this epoch.
	conn.catalog = recomputeExposure(conn.catalog, state.cfg)
	state.catalog = conn.catalog
	state.listenerUnavailable = conn.listenerUnavailable.Load()
	m.pending += 2
	m.publishLocked()
	m.mu.Unlock()
	go func() {
		defer m.workerDone()
		conn.runListChanges()
	}()
	go func() {
		defer m.workerDone()
		conn.watch()
	}()
}

func (m *Manager) setupFailed(state *serverState, epoch uint64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped || m.servers[state.cfg.Name] != state || state.epoch != epoch {
		return
	}
	state.conn = nil
	state.catalog = nil
	state.refreshWarning = ""
	state.listenerUnavailable = false
	state.status = serverStateFailed
	state.errClass = ErrorClass(err)
	if state.errClass == "" {
		state.errClass = "error"
	}
	m.publishLocked()
}

// Allocate epochs across the manager so a removed server's routes stay stale
// even if another server is later added under the same name.
func (m *Manager) nextEpochLocked() uint64 {
	m.lastEpoch++
	return m.lastEpoch
}

func (m *Manager) withdrawLocked(state *serverState, reason string) {
	state.epoch = m.nextEpochLocked()
	if state.setupCancel != nil {
		state.setupCancel()
		state.setupCancel = nil
	}
	state.setupActive = false
	if state.conn != nil {
		conn := state.conn
		state.conn = nil
		conn.closing.Store(true)
		conn.catalog = nil
		if conn.setupDisarm != nil {
			conn.setupDisarm()
			conn.setupDisarm = nil
		}
		conn.cancel()
		conn.closeAsyncLocked()
	}
	state.catalog = nil
	state.refreshWarning = ""
	state.listenerUnavailable = false
	if state.cfg.Enabled {
		if reason == "permission" {
			state.status = serverStateFailed
			state.errClass = "permission"
			state.resolved = Server{}
			state.resolvedValid = false
		}
	}
}

func (m *Manager) connectionLost(conn *connection, err error) {
	m.mu.Lock()
	state := m.servers[conn.server]
	if m.stopped || state == nil || state.conn != conn || conn.closing.Load() || conn.retired.Load() {
		m.mu.Unlock()
		return
	}
	conn.retired.Store(true)
	state.conn = nil
	state.catalog = nil
	state.refreshWarning = ""
	state.listenerUnavailable = false
	class := ErrorClass(err)
	if class == "" {
		class = "connection"
	}
	if state.reconnects >= maxReconnects {
		state.status = serverStateFailed
		state.errClass = class
		m.publishLocked()
		m.mu.Unlock()
		conn.closeAsync()
		return
	}
	state.reconnects++
	state.epoch = m.nextEpochLocked()
	state.setupActive = false
	state.status = serverStateReconnecting
	state.errClass = class
	m.scheduleSetupLocked(state)
	m.publishLocked()
	m.mu.Unlock()
	conn.closeAsync()
}

func (c *connection) watch() {
	err := c.session.Wait()
	// Wait settles every pending SDK call. Preserve those results instead of
	// racing their consumers with cancellation during connection retirement.
	c.sessionEnded.Store(true)
	if c.closing.Load() || c.closed.Load() {
		return
	}
	c.manager.connectionLost(c, err)
}

func (c *connection) runListChanges() {
	for {
		select {
		case <-c.listCh:
			delay := time.Second - c.manager.opts.now().Sub(c.lastList)
			if delay > 0 {
				if !c.manager.opts.sleep(c.ctx, delay) {
					return
				}
			}
			_ = c.refreshCatalog()
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *connection) signalListChanged() {
	select {
	case c.listCh <- struct{}{}:
	default:
	}
}

func (c *connection) refreshCatalog() error {
	// Failed refreshes also consume the notification rate-limit interval.
	c.lastList = c.manager.opts.now()
	ctx, cancel := context.WithTimeout(c.ctx, c.manager.serverTimeout(c.server))
	defer cancel()
	raw, err := listWithRetry(ctx, c.session, c)
	if err != nil {
		c.manager.markRefreshWarning(c, err)
		return err
	}
	c.manager.mu.Lock()
	state := c.manager.servers[c.server]
	if c.manager.stopped || state == nil || state.conn != c || c.retired.Load() || c.closing.Load() {
		c.manager.mu.Unlock()
		return context.Canceled
	}
	catalog, err := buildCatalog(state.cfg, c.epoch, raw)
	if err != nil {
		c.manager.mu.Unlock()
		c.manager.markRefreshWarning(c, err)
		return err
	}
	state.catalog = catalog
	c.catalog = catalog
	state.refreshWarning = ""
	c.manager.publishLocked()
	c.manager.mu.Unlock()
	return nil
}

func listWithRetry(ctx context.Context, session *sdk.ClientSession, conn *connection) ([]*sdk.Tool, error) {
	var err error
	for attempt := 0; attempt < maxDiscoveryTries; attempt++ {
		if conn != nil {
			conn.clearRetryAdvice()
			conn.transient.Store(false)
		}
		var tools []*sdk.Tool
		tools, err = listAllTools(ctx, session)
		if err == nil {
			return tools, nil
		}
		if conn != nil && conn.transient.Load() {
			err = &transientError{err: err}
		}
		if ctx.Err() != nil || !retryableSetup(err) {
			break
		}
		delay, ok := conn.manager.discoveryRetryDelay(conn, attempt)
		if !ok || !conn.manager.opts.sleep(ctx, delay) {
			break
		}
	}
	return nil, err
}

func (m *Manager) markRefreshWarning(conn *connection, err error) {
	class := conn.latchedClass()
	m.mu.Lock()
	state := m.servers[conn.server]
	if state == nil || state.conn != conn {
		m.mu.Unlock()
		return
	}
	state.refreshWarning = ErrorClass(err)
	if state.refreshWarning == "" {
		state.refreshWarning = "refresh"
	}
	m.publishLocked()
	m.mu.Unlock()
	if class == "session-lost" {
		go m.connectionLost(conn, &ConfigError{Class: class, Message: "session lost"})
	}
}

func (m *Manager) serverTimeout(name string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state := m.servers[name]; state != nil {
		return state.cfg.Timeout
	}
	return 60 * time.Second
}

func (m *Manager) publishLocked() {
	snap := &Snapshot{Revision: m.generation, ReloadError: m.reloadErr}
	// Per-server naming cannot detect collisions across server/tool boundaries.
	// Withhold every ambiguous route rather than choosing a server by order.
	nameCounts := make(map[string]int)
	for _, state := range m.servers {
		for _, tool := range state.catalog {
			if declared(tool) {
				nameCounts[tool.Name]++
			}
		}
	}
	if len(m.warnings) > 0 {
		warnings := m.warnings
		if len(warnings) > maxConfigWarnings {
			warnings = warnings[:maxConfigWarnings]
		}
		snap.Warnings = append([]string(nil), warnings...)
		if len(m.warnings) > maxConfigWarnings {
			snap.Warnings = append(snap.Warnings, fmt.Sprintf("%d more config warnings", len(m.warnings)-maxConfigWarnings))
		}
	}
	names := append([]string(nil), m.order...)
	for _, name := range names {
		state := m.servers[name]
		if state == nil {
			continue
		}
		status := ServerStatus{
			Name:                     state.cfg.Name,
			Transport:                string(state.cfg.Transport),
			State:                    state.status,
			Discovered:               len(state.catalog),
			ErrorClass:               state.errClass,
			RefreshWarning:           state.refreshWarning,
			NotificationsUnavailable: state.listenerUnavailable,
		}
		if !state.cfg.Enabled {
			status.State = serverStateDisabled
		}
		for _, tool := range state.catalog {
			if declared(tool) {
				if nameCounts[tool.Name] > 1 {
					status.ErrorClass = "tool-name-collision"
					continue
				}
				status.Exposed++
				snap.Tools = append(snap.Tools, tool)
			}
		}
		snap.Servers = append(snap.Servers, status)
	}
	m.snapshot.Store(snap)
	// Coalesce notifications. Waiters read the snapshot before blocking and
	// again after each notification, so one token can represent many updates.
	select {
	case m.snapshotChange <- struct{}{}:
	default:
	}
}

// Snapshot returns the latest immutable view.
func (m *Manager) Snapshot() *Snapshot { return m.snapshot.Load() }

// Stop unpublishes declarations and cancels all manager work without waiting.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.stopped = true
		for _, state := range m.servers {
			if state.setupCancel != nil {
				state.setupCancel()
			}
			if state.conn != nil {
				conn := state.conn
				state.conn = nil
				conn.closing.Store(true)
				conn.cancel()
				conn.closeAsyncLocked()
			}
			state.catalog = nil
			state.status = serverStateClosing
		}
		m.snapshot.Store(&Snapshot{Revision: m.generation})
		m.mu.Unlock()
		m.cancel()
	})
}

// Wait observes all owned workers and process cleanup.
func (m *Manager) Wait() {
	m.mu.Lock()
	for m.pending > 0 {
		m.idle.Wait()
	}
	m.mu.Unlock()
}

func (m *Manager) workerDone() {
	m.mu.Lock()
	m.pending--
	if m.pending == 0 {
		m.idle.Broadcast()
	}
	m.mu.Unlock()
}

// Call runs one declared tool through its captured connection identity.
func (m *Manager) Call(ctx context.Context, tool Tool, inv tools.Invocation) tools.Result {
	m.mu.Lock()
	state := m.servers[tool.Server]
	if m.stopped || state == nil || state.conn == nil || state.epoch != tool.Epoch || state.status != serverStateReady {
		m.mu.Unlock()
		return callError("stale", tool)
	}
	if !m.policyAllows(state.cfg) {
		m.mu.Unlock()
		return callError("permission", tool)
	}
	conn := state.conn
	live, ok := findCatalogTool(conn.catalog, tool.Name)
	_, published := m.snapshot.Load().Find(tool.Name)
	if !published || !ok || !declared(live) || live.RawName != tool.RawName {
		m.mu.Unlock()
		return callError("stale", tool)
	}
	timeout := state.cfg.Timeout
	readAvailable := m.input.ReadAvailable
	m.mu.Unlock()
	return conn.call(ctx, live, inv, timeout, readAvailable)
}

func findCatalogTool(catalog []Tool, name string) (Tool, bool) {
	for _, tool := range catalog {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

func callError(class string, tool Tool) tools.Result {
	return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: fmt.Sprintf("MCP tool %s/%s is unavailable (%s)", tool.Server, tool.RawName, class)}}}
}

func (c *connection) call(parent context.Context, tool Tool, inv tools.Invocation, timeout time.Duration, readAvailable bool) tools.Result {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	hard := timeout * 10
	callCtx, cancel := context.WithTimeout(parent, hard)
	defer cancel()
	stopConn := context.AfterFunc(c.ctx, func() {
		if !c.sessionEnded.Load() {
			cancel()
		}
	})
	defer stopConn()

	state := &callState{token: fmt.Sprintf("psi-%d-%d", c.epoch, c.manager.callSeq.Add(1)), timeout: timeout}
	state.timer = time.AfterFunc(timeout, func() {
		state.idle.Store(true)
		cancel()
	})
	defer state.retire()

	obs := newRequestObservation("tools/call")
	callCtx = withRequestObservation(callCtx, obs)
	defer obs.finish()

	if err := c.acquire(callCtx); err != nil {
		return c.admissionFailure(tool, parent)
	}
	defer c.release()
	if state.idle.Load() || callCtx.Err() != nil {
		return c.admissionFailure(tool, parent)
	}
	// Recheck the live route after admission so a queued call cannot reach a
	// replacement connection or a newly hidden tool.
	if !c.manager.routeLive(tool, c) {
		return callError("stale", tool)
	}
	c.registerToken(state)
	defer c.retireToken(state)
	params := &sdk.CallToolParams{Name: tool.RawName, Arguments: inv.Arguments}
	params.SetProgressToken(state.token)
	result, err := c.session.CallTool(callCtx, params)
	if err != nil {
		return c.callFailure(tool, err, state, callCtx, parent)
	}
	return Convert(result, ConvertOptions{Server: tool.Server, Tool: tool.RawName, ReadAvailable: readAvailable})
}

func (m *Manager) routeLive(tool Tool, conn *connection) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.servers[tool.Server]
	if m.stopped || state == nil || state.conn != conn || state.epoch != tool.Epoch ||
		state.status != serverStateReady || conn.retired.Load() || conn.closing.Load() || !m.policyAllows(state.cfg) {
		return false
	}
	live, ok := findCatalogTool(conn.catalog, tool.Name)
	_, published := m.snapshot.Load().Find(tool.Name)
	return published && ok && declared(live) && live.RawName == tool.RawName
}

// Admission can end because of the caller, a timeout, or connection withdrawal.
// None of these paths has sent a request to the server.
func (c *connection) admissionFailure(tool Tool, parent context.Context) tools.Result {
	if errors.Is(parent.Err(), context.Canceled) {
		return cancelledResult(tool)
	}
	if parent.Err() == nil && c.ctx.Err() != nil {
		return callError("stale", tool)
	}
	return timeoutResult(tool, "admission")
}

func cancelledResult(tool Tool) tools.Result {
	return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: fmt.Sprintf("MCP tool %s/%s was cancelled", tool.Server, tool.RawName)}}}
}

func timeoutResult(tool Tool, phase string) tools.Result {
	return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: fmt.Sprintf("MCP tool %s/%s timed out (%s)", tool.Server, tool.RawName, phase)}}}
}

func (c *connection) callFailure(tool Tool, err error, state *callState, ctx, parent context.Context) tools.Result {
	class := c.latchedClass()
	if obs := requestObservationFrom(ctx); obs != nil {
		class, _ = obs.snapshot()
	}
	if class == "session-lost" {
		go c.manager.connectionLost(c, &ConfigError{Class: class, Message: "session lost"})
		return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: fmt.Sprintf("MCP tool %s/%s failed (session-lost); the outcome may be unknown", tool.Server, tool.RawName)}}}
	}
	if class != "" {
		text := fmt.Sprintf("MCP tool %s/%s failed (%s)", tool.Server, tool.RawName, class)
		switch class {
		case "server", "timeout", "rate-limit", "limit":
			text += "; the outcome may be unknown"
		}
		return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: text}}}
	}
	if state.idle.Load() {
		return timeoutResult(tool, "waiting for progress")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return timeoutResult(tool, "hard deadline")
	}
	// Connection loss also cancels ctx. Only cancellation of the calling
	// turn means the user or run cancelled this tool rather than losing it.
	if errors.Is(parent.Err(), context.Canceled) {
		return cancelledResult(tool)
	}
	return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: fmt.Sprintf("MCP tool %s/%s failed: %s; the outcome may be unknown", tool.Server, tool.RawName, safeErrorText(err))}}}
}

func safeErrorText(err error) string {
	if err == nil {
		return "unknown error"
	}
	var le *limitError
	if errors.As(err, &le) {
		return le.Error()
	}
	var ce *ConfigError
	if errors.As(err, &ce) {
		return ce.Error()
	}
	if errors.Is(err, sdk.ErrConnectionClosed) {
		return "connection closed"
	}
	return "request failed"
}

func (c *connection) acquire(ctx context.Context) error {
	select {
	case c.permits <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return context.Canceled
	}
	select {
	case c.manager.callSlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		<-c.permits
		return ctx.Err()
	case <-c.ctx.Done():
		<-c.permits
		return context.Canceled
	}
}

func (c *connection) release() {
	select {
	case <-c.manager.callSlot:
	default:
	}
	select {
	case <-c.permits:
	default:
	}
}

func (c *connection) registerToken(state *callState) {
	c.mu.Lock()
	c.tokens[state.token] = state
	c.mu.Unlock()
}

func (c *connection) retireToken(state *callState) {
	c.mu.Lock()
	delete(c.tokens, state.token)
	c.mu.Unlock()
	state.retire()
}

func (c *connection) noteProgress(token any, progress float64) {
	if math.IsNaN(progress) || math.IsInf(progress, 0) {
		return
	}
	key, ok := token.(string)
	if !ok {
		return
	}
	c.mu.Lock()
	state := c.tokens[key]
	c.mu.Unlock()
	if state != nil {
		state.advanceProgress(progress)
	}
}

func (c *connection) closeAsync() {
	c.manager.mu.Lock()
	c.closeAsyncLocked()
	c.manager.mu.Unlock()
}

func (c *connection) closeAsyncLocked() {
	if c.closed.Swap(true) {
		return
	}
	if c.setupDisarm != nil {
		c.setupDisarm()
		c.setupDisarm = nil
	}
	c.cancel()
	c.manager.pending++
	go func() {
		defer c.manager.workerDone()
		_ = c.session.Close()
	}()
}

type transientError struct{ err error }

func (e *transientError) Error() string {
	return "transient MCP transport error"
}

func (e *transientError) Unwrap() error { return e.err }

func retryableSetup(err error) bool {
	if err == nil {
		return false
	}
	var transient *transientError
	if errors.As(err, &transient) {
		return true
	}
	var ce *ConfigError
	if errors.As(err, &ce) {
		return ce.Class == "timeout"
	}
	var le *limitError
	if errors.As(err, &le) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errors.Is(err, context.DeadlineExceeded)
	}
	if errors.Is(err, sdk.ErrConnectionClosed) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Retryable
	}
	return false
}

// String makes a snapshot safe to print in a diagnostic.
func (s *Snapshot) String() string {
	var b bytes.Buffer
	for _, server := range s.Servers {
		fmt.Fprintf(&b, "%s %s %s %d/%d\n", server.Name, server.Transport, server.State, server.Exposed, server.Discovered)
	}
	return b.String()
}
