package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/skills"
	"github.com/hypernewbie/psi/internal/terminal"
	"github.com/hypernewbie/psi/internal/webaccess"
)

// activateSession loads the console-free core of startup: tool defaults, the
// model catalog, instructions, skills, the assembled prompt, overrides, and
// the opened session. Pickers stay with the caller: console and lifecycle are
// only touched by the interactive resume selector.
func activateSessionWithStore(o Options, cwd, agentDir string, st *config.Settings, consent bool, console *terminal.Console, lifecycle *terminalLifecycle, prepared *session.Store, web *webaccess.Client) (models *config.ModelsFile, sk []skills.Skill, diagnostics []skills.Diagnostic, prompt string, sections []config.PromptSection, ov config.StartupOverrides, store *session.Store, err error) {
	if o.NoTools {
		st.DefaultTools = []string{}
	} else {
		if o.Tools != "" {
			st.DefaultTools, err = config.ResolveTools(strings.Split(o.Tools, ","), st.DefaultTools)
			if err != nil {
				return nil, nil, nil, "", nil, config.StartupOverrides{}, nil, err
			}
		}
		if o.ExcludeTools != "" {
			var excluded []string
			for _, name := range strings.Split(o.ExcludeTools, ",") {
				name = strings.TrimSpace(name)
				if name != "" {
					excluded = append(excluded, "-"+name)
				}
			}
			if len(excluded) > 0 {
				st.DefaultTools, err = config.ResolveTools(excluded, st.DefaultTools)
				if err != nil {
					return nil, nil, nil, "", nil, config.StartupOverrides{}, nil, err
				}
			}
		}
	}
	models, err = config.LoadModels(filepath.Join(agentDir, "models.json"))
	if err != nil {
		return nil, nil, nil, "", nil, config.StartupOverrides{}, nil, err
	}
	// A provider from models.json resolves after the file loads.
	o, err = resolveConfiguredModelOption(o, models)
	if err != nil {
		return nil, nil, nil, "", nil, config.StartupOverrides{}, nil, err
	}
	var instructions []config.InstructionFile
	if !o.NoContextFiles {
		instructions, err = config.DiscoverInstructions(cwd, agentDir, consent)
		if err != nil {
			return nil, nil, nil, "", nil, config.StartupOverrides{}, nil, err
		}
	}
	if !o.NoSkills {
		sk, diagnostics = skills.Discover(cwd, agentDir, st.Skills, consent)
	}
	var appendSystemPrompts []string
	for _, source := range o.AppendSystemPrompts {
		appendSystemPrompts = append(appendSystemPrompts, resolveCLIPrompt(source))
	}
	sections = config.BuildPromptSections(cwd, instructions, []string{skills.FormatForPrompt(sk)}, promptToolNames(st.DefaultTools, web), resolveCLIPrompt(o.SystemPrompt), o.SystemPrompt != "", appendSystemPrompts)
	prompt = config.RenderSections(sections)
	ov = config.ResolveStartup(o.Provider, o.Model, o.Thinking, o.SessionDir, o.NoSession, "", "", "", *st, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
	if prepared != nil {
		store = prepared
	} else {
		store, err = openSession(o, cwd, agentDir, *st, ov, console, lifecycle)
	}
	if err != nil {
		return nil, nil, nil, "", nil, config.StartupOverrides{}, nil, err
	}
	return models, sk, diagnostics, prompt, sections, ov, store, nil
}

// runRPC serves one headless JSONL session on stdin/stdout. There is no TTY,
// no theme, and no picker: consent and workspace decisions use headless
// defaults instead of interactive prompts.
func runRPC(o Options, version string) error {
	if o.Resume {
		return fmt.Errorf("psi --resume needs a terminal; use --session, --session-id, or --continue in RPC mode")
	}
	if len(o.Messages) > 0 {
		return fmt.Errorf("positional messages are not supported in RPC mode; send a prompt command")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	agentDir := session.AgentDir()
	userPath := filepath.Join(agentDir, "settings.json")
	userSettings, err := config.LoadSettings(userPath, "", false)
	if err != nil {
		return err
	}
	var preparedStore *session.Store
	if o.SessionPath != "" {
		if info, statErr := os.Stat(o.SessionPath); statErr == nil && info.Mode().IsRegular() {
			preparedStore, err = session.OpenFile(o.SessionPath, "", !o.NoSession)
			if err != nil {
				return err
			}
			info, statErr = os.Stat(preparedStore.Cwd())
			if statErr != nil || !info.IsDir() {
				return fmt.Errorf("Stored session working directory does not exist: %s\nSession file: %s\nCurrent working directory: %s", preparedStore.Cwd(), preparedStore.File(), cwd)
			}
			cwd = preparedStore.Cwd()
		}
	}
	projectPath := filepath.Join(cwd, ".pi", "settings.json")
	// Headless trust follows pi: an explicit override wins, then the stored
	// decision, then the global defaultProjectTrust setting. There is no UI,
	// so an undecided folder stays untrusted.
	trust, err := resolveProjectTrust(o, cwd, agentDir, userSettings.Settings.DefaultProjectTrust, nil, nil)
	if err != nil {
		return err
	}
	consent := trust.allowsResources()
	loaded := userSettings
	if consent {
		if loaded, err = config.LoadProjectSettings(userSettings, projectPath); err != nil {
			return err
		}
	}
	st := loaded.Settings
	web, webDiagnostics := loadWebAccess()
	defer web.CloseIdle()
	for _, line := range webDiagnosticsLines(webDiagnostics) {
		fmt.Fprintln(os.Stderr, line)
	}
	models, sk, _, prompt, sections, _, store, err := activateSessionWithStore(o, cwd, agentDir, &st, consent, nil, nil, preparedStore, web)
	if err != nil {
		return err
	}
	// The runtime keeps the resolved options, so a restored session uses the
	// same provider as the startup selection.
	o, err = resolveConfiguredModelOption(o, models)
	if err != nil {
		return err
	}
	if err := store.TryLock(); err != nil {
		return err
	}
	r := &runtime{
		store: store, version: version, options: o, settings: st, web: web,
		models: models, skills: sk, prompt: prompt, sections: sections, agentDir: agentDir,
		projectTrust: trust,
	}
	cleanup := func() {
		if r.web != nil {
			r.web.CloseIdle()
		}
		if r.prov != nil {
			r.prov.CloseIdle()
		}
		r.store.Unlock()
	}
	var cleanupOnce sync.Once
	cleanupAll := func() {
		cleanupOnce.Do(func() {
			r.stopMCP()
			r.waitMCP(30 * time.Second)
			cleanup()
		})
	}
	if err := r.restore(); err != nil {
		cleanupAll()
		return err
	}
	if o.Name != "" {
		if _, err := r.store.AppendSessionInfo(o.Name); err != nil {
			cleanupAll()
			return fmt.Errorf("set session name: %w", err)
		}
	}
	transport, err := openRPCTransport()
	if err != nil {
		cleanup()
		return err
	}
	s := &rpcRunner{r: r, transport: transport, shutdown: make(chan struct{})}
	s.encoder = newRPCEncoder(transport.out, s.failTransport)
	// External clients hydrate resumed sessions separately. Never replay
	// their old messages as new live events after the next prompt.
	if proj, err := store.Projection(); err == nil {
		s.emitted = len(proj.Flat)
	} else {
		_ = transport.Close()
		cleanupAll()
		return err
	}
	r.startMCP()
	// The RPC child runs without bridge environment variables, so the
	// syncboard tool stays hidden by the same availability predicate the
	// terminal loop uses.
	s.loop = r.newLoop(s.sink)

	// One shutdown path serves stdin EOF and termination signals: stop
	// intake, cancel the owned workers, wait for their terminal frames,
	// then release the store once. No timeout can unlock the store while
	// a worker still owns it.
	var shutdownOnce sync.Once
	var signalled atomic.Bool
	shutdown := func() {
		shutdownOnce.Do(func() {
			close(s.shutdown)
			s.cancelActive()
			s.cancelShells()
			// Wake the scanner without closing the original stdin object.
			transport.InterruptInput()
			s.waitWorkers()
			// Release the scanner goroutine from its stdin read. The
			// shutdown channel already stopped intake; this only ends
			// the blocked read so no goroutine outlives the session.
			_ = os.Stdin.Close()
			// Restore endpoint flags and release wrappers only after every
			// worker has settled its terminal frame.
			_ = transport.Close()
			cleanupAll()
		})
	}
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	signalDone := make(chan struct{})
	signalFinished := make(chan struct{})
	go func() {
		defer close(signalFinished)
		select {
		case <-sigs:
			signalled.Store(true)
			shutdown()
			// A blocked stdin read is not guaranteed to wake on Close across
			// platforms. The workers and store are already settled, so exit
			// from the signal path instead of waiting for the scanner.
			os.Exit(143)
		case <-signalDone:
		}
	}()

	loopErr := s.runLoop(transport.in)
	close(signalDone)
	shutdown()
	<-signalFinished
	if signalled.Load() {
		os.Exit(143)
	}
	if err := s.transportError(); err != nil {
		return err
	}
	if loopErr != nil {
		return loopErr
	}
	return nil
}

// cancelActive aborts the in-flight run and the compact reservation, if
// any. Reader shutdown and abort share it; it is safe to call twice.
func (s *rpcRunner) cancelActive() {
	s.mu.Lock()
	var cancels []context.CancelFunc
	// Cancel the reservation first: settling the run can wake its worker.
	if s.reservation != nil {
		cancels = append(cancels, s.reservation.cancel)
	}
	if s.run != nil {
		cancels = append(cancels, s.run.cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// waitWorkers blocks until the owned prompt run and compact reservation
// settle. Admissions after shutdown refuse, so the snapshot is complete.
func (s *rpcRunner) waitWorkers() {
	s.mu.Lock()
	var waits []<-chan struct{}
	if s.run != nil {
		waits = append(waits, s.run.done)
	}
	if s.reservation != nil {
		waits = append(waits, s.reservation.done)
	}
	for _, worker := range s.shells {
		waits = append(waits, worker.done)
	}
	s.mu.Unlock()
	for _, wait := range waits {
		<-wait
	}
}
