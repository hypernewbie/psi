package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/providerregistry"
	"psi/internal/session"
	"psi/internal/skills"
	"psi/internal/terminal"
)

// activateSession loads the console-free core of startup: tool defaults, the
// model catalog, instructions, skills, the assembled prompt, overrides, and
// the opened session. Pickers stay with the caller: console and lifecycle are
// only touched by the interactive resume selector.
func activateSession(o Options, cwd, agentDir string, st *config.Settings, consent bool, console *terminal.Console, lifecycle *terminalLifecycle) (models *config.ModelsFile, sk []skills.Skill, diagnostics []skills.Diagnostic, prompt string, sections []config.PromptSection, ov config.StartupOverrides, store *session.Store, err error) {
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
	sections = config.BuildPromptSections(cwd, instructions, []string{skills.FormatForPrompt(sk)}, st.DefaultTools, resolveCLIPrompt(o.SystemPrompt), o.SystemPrompt != "", appendSystemPrompts)
	prompt = config.RenderSections(sections)
	if o.Provider != "" {
		if _, ok := providerregistry.Lookup(o.Provider); !ok {
			return nil, nil, nil, "", nil, config.StartupOverrides{}, nil, fmt.Errorf("unsupported provider %q (available: %s)", o.Provider, strings.Join(providerregistry.IDs(), ", "))
		}
	}
	ov = config.ResolveStartup(o.Provider, o.Model, o.Thinking, o.SessionDir, o.NoSession, "", "", "", *st, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
	store, err = openSession(o, cwd, agentDir, *st, ov, console, lifecycle)
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
	userSettings, err := config.LoadSettings(filepath.Join(agentDir, "settings.json"), "", false)
	if err != nil {
		return err
	}
	// Headless trust follows pi: an explicit override wins, then the stored
	// decision, then the global defaultProjectTrust setting. There is no UI,
	// so an undecided folder stays untrusted.
	consent, err := resolveProjectTrust(o, cwd, agentDir, userSettings.Settings.DefaultProjectTrust, nil, nil)
	if err != nil {
		return err
	}
	loaded, err := config.LoadSettings(filepath.Join(agentDir, "settings.json"), filepath.Join(cwd, ".pi", "settings.json"), consent)
	if err != nil {
		return err
	}
	st := loaded.Settings
	models, sk, _, prompt, sections, _, store, err := activateSession(o, cwd, agentDir, &st, consent, nil, nil)
	if err != nil {
		return err
	}
	if err := store.TryLock(); err != nil {
		return err
	}
	r := &runtime{
		store: store, version: version, options: o, settings: st,
		models: models, skills: sk, prompt: prompt, sections: sections, agentDir: agentDir,
		projectTrusted: consent,
	}
	cleanup := func() {
		if r.prov != nil {
			r.prov.CloseIdle()
		}
		r.store.Unlock()
	}
	if err := r.restore(); err != nil {
		cleanup()
		return err
	}
	if o.Name != "" {
		if _, err := r.store.AppendSessionInfo(o.Name); err != nil {
			cleanup()
			return fmt.Errorf("set session name: %w", err)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	s := &rpcRunner{r: r, out: enc}
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
	s.loop = agent.New(store, r.reg, r.prov, s.sink)

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	go func() {
		<-sigs
		s.cancelActive()
		cleanup()
		os.Exit(143)
	}()

	s.runLoop(os.Stdin)
	s.cancelActive()
	s.waitIdle(10 * time.Second)
	cleanup()
	return nil
}

// cancelActive aborts the in-flight run, if any. It is safe to call twice.
func (s *rpcRunner) cancelActive() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// waitIdle blocks until the run settles or the budget expires, so stdin EOF
// does not truncate the settled events already queued for stdout.
func (s *rpcRunner) waitIdle(budget time.Duration) {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !s.isRunning() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
