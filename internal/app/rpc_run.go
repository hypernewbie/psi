package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"psi/internal/config"
	"psi/internal/session"
	"psi/internal/skills"
	"psi/internal/terminal"
	"psi/internal/webaccess"
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
	sections = config.BuildPromptSections(cwd, instructions, []string{skills.FormatForPrompt(sk)}, filterPromptTools(st.DefaultTools, web), resolveCLIPrompt(o.SystemPrompt), o.SystemPrompt != "", appendSystemPrompts)
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
	consent, err := resolveProjectTrust(o, cwd, agentDir, userSettings.Settings.DefaultProjectTrust, nil, nil)
	if err != nil {
		return err
	}
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
		projectTrusted: consent, live: newLiveConfig(),
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
	if err := r.restore(); err != nil {
		cleanup()
		return err
	}
	r.publishLive()
	if o.Name != "" {
		if _, err := r.store.AppendSessionInfo(o.Name); err != nil {
			cleanup()
			return fmt.Errorf("set session name: %w", err)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	s := &rpcRunner{r: r, out: enc, shutdown: make(chan struct{})}
	// External clients hydrate resumed sessions separately. Never replay
	// their old messages as new live events after the next prompt.
	if proj, err := store.Projection(); err == nil {
		s.emitted = len(proj.Flat)
	} else {
		cleanup()
		return err
	}
	// The RPC child runs without bridge environment variables, so the
	// syncboard tool stays hidden by the same availability predicate the
	// terminal loop uses.
	s.loop = r.newLoop(s.sink)

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	if s.serve(os.Stdin, sigs, cleanup) {
		os.Exit(143)
	}
	return nil
}

// serve owns the session through both command dispatch and worker settlement.
// A signal stops intake and cancels work, but only the command-loop owner may
// release the store: a synchronous setter can still be persisting a change.
func (s *rpcRunner) serve(stdin io.ReadCloser, sigs <-chan os.Signal, cleanup func()) bool {
	var stopOnce sync.Once
	stopIntake := func() {
		stopOnce.Do(func() {
			close(s.shutdown)
			s.cancelActive()
		})
	}
	stopSignal, signalDone := make(chan struct{}), make(chan struct{})
	signalled := false
	go func() {
		defer close(signalDone)
		select {
		case <-sigs:
			signalled = true
			stopIntake()
		case <-stopSignal:
		}
	}()

	s.runLoop(stdin)
	stopIntake()
	s.waitWorkers()
	// Shutdown already stopped intake. Close stdin to release any scanner
	// still blocked on a read.
	_ = stdin.Close()
	close(stopSignal)
	<-signalDone
	cleanup()
	return signalled
}

// cancelActive aborts the in-flight run and the compact reservation, if
// any. Reader shutdown and abort share it; it is safe to call twice.
func (s *rpcRunner) cancelActive() {
	s.mu.Lock()
	var cancels []context.CancelFunc
	if s.run != nil {
		cancels = append(cancels, s.run.cancel)
	}
	if s.reservation != nil {
		cancels = append(cancels, s.reservation.cancel)
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
	s.mu.Unlock()
	for _, wait := range waits {
		<-wait
	}
}
