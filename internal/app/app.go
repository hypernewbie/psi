// Package app wires the interactive terminal to the real agent and session lifecycle.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/skills"
	"psi/internal/terminal"
	"psi/internal/tools"
)

type Options struct {
	Help, Version, Continue, Resume                           bool
	SessionPath, Provider, Model, Thinking, SessionDir, Tools string
	NoTools, NoSession, AllowProj, DenyProj                   bool
}

func Parse(args []string) (Options, *flag.FlagSet, error) {
	var o Options
	fs := flag.NewFlagSet("psi", flag.ContinueOnError)
	fs.BoolVar(&o.Help, "help", false, "show help")
	fs.BoolVar(&o.Version, "version", false, "show version")
	fs.BoolVar(&o.Continue, "continue", false, "continue most recent session")
	fs.BoolVar(&o.Resume, "resume", false, "resume with selection")
	fs.StringVar(&o.SessionPath, "session", "", "open explicit session path")
	fs.StringVar(&o.Provider, "provider", "", "provider (anthropic, openai-codex)")
	fs.StringVar(&o.Model, "model", "", "model ID")
	fs.StringVar(&o.Thinking, "thinking", "", "thinking level")
	fs.StringVar(&o.SessionDir, "session-dir", "", "session directory")
	fs.StringVar(&o.Tools, "tools", "", "tool list (read,bash,edit,write)")
	fs.BoolVar(&o.NoTools, "no-tools", false, "disable tools")
	fs.BoolVar(&o.NoSession, "no-session", false, "in-memory session")
	fs.BoolVar(&o.AllowProj, "allow-project", false, "allow protected project resources")
	fs.BoolVar(&o.DenyProj, "deny-project", false, "deny protected project resources")
	if err := fs.Parse(args); err != nil {
		return o, fs, err
	}
	if fs.NArg() != 0 {
		return o, fs, fmt.Errorf("unexpected positional arguments; psi is interactive only")
	}
	if o.AllowProj && o.DenyProj {
		return o, fs, fmt.Errorf("--allow-project and --deny-project conflict")
	}
	count := 0
	for _, selected := range []bool{o.Continue, o.Resume, o.SessionPath != ""} {
		if selected {
			count++
		}
	}
	if count > 1 {
		return o, fs, fmt.Errorf("choose only one of --continue, --resume, and --session")
	}
	return o, fs, nil
}

func Run(args []string, version string) error {
	o, fs, err := Parse(args)
	if err != nil {
		return err
	}
	if o.Version {
		fmt.Printf("psi %s\n", version)
		return nil
	}
	if o.Help {
		fmt.Printf("psi %s — minimal Go coding agent\n", version)
		fs.SetOutput(os.Stdout)
		fs.PrintDefaults()
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("noninteractive input: psi needs a terminal (no print/RPC mode)")
	}
	tty, _, err := terminal.Acquire()
	if err != nil {
		return err
	}
	defer tty.Restore()
	console := terminal.NewConsole(os.Stdin, os.Stdout)
	defer console.Close()
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, handledSignals()...)
	defer signal.Stop(signals)
	lifecycle := &terminalLifecycle{tty: tty, console: console, signals: signals}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	agentDir := session.AgentDir()
	console.SetPresentation(terminal.PresentationSnapshot{WorkingDirectory: cwd, HomeDirectory: os.Getenv("HOME")})
	consent, err := askProjectConsent(o, cwd, console, lifecycle)
	if err != nil {
		return err
	}
	loaded, err := config.LoadSettings(filepath.Join(agentDir, "settings.json"), filepath.Join(cwd, ".pi", "settings.json"), consent)
	if err != nil {
		return err
	}
	st := loaded.Settings
	if o.NoTools {
		st.DefaultTools = []string{}
	} else if o.Tools != "" {
		st.DefaultTools, err = config.ResolveTools(strings.Split(o.Tools, ","), st.DefaultTools)
		if err != nil {
			return err
		}
	}
	models, err := config.LoadModels(filepath.Join(agentDir, "models.json"))
	if err != nil {
		return err
	}
	instructions, err := config.DiscoverInstructions(cwd, agentDir, consent)
	if err != nil {
		return err
	}
	sk, diagnostics := skills.Discover(cwd, agentDir, st.Skills, consent)
	prompt := config.AssemblePrompt(cwd, instructions, []string{skills.FormatForPrompt(sk)}, st.DefaultTools)
	if o.Provider != "" && o.Provider != "anthropic" && o.Provider != "openai-codex" {
		return fmt.Errorf("unsupported provider %q (available: anthropic, openai-codex)", o.Provider)
	}
	ov := config.ResolveStartup(o.Provider, o.Model, o.Thinking, o.SessionDir, o.NoSession, "", "", "", st, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
	store, err := openSession(o, cwd, agentDir, st, ov, console, lifecycle)
	if errors.Is(err, errStartupCancelled) {
		return nil
	}
	if err != nil {
		return err
	}
	if o.SessionPath != "" && !sameDir(store.Header().Cwd, cwd) {
		confirmed, err := confirmWorkspaceMismatch(console, lifecycle, store.Header().Cwd, cwd)
		if err != nil {
			return err
		}
		if !confirmed {
			return nil
		}
	}
	if err := store.TryLock(); err != nil {
		return err
	}
	r := &runtime{store: store, options: o, settings: st, models: models, skills: sk, prompt: prompt, agentDir: agentDir}
	defer func() {
		r.store.Unlock()
		if r.prov != nil {
			r.prov.CloseIdle()
		}
	}()
	if err := r.restore(); err != nil {
		return err
	}
	r.tty, r.lifecycle, r.signals = tty, lifecycle, signals
	startupOut := terminal.RawOutput{W: console}
	grouped := map[string]int{}
	for _, d := range diagnostics {
		grouped[d.Message]++
	}
	if !st.QuietStartup {
		fmt.Fprintln(startupOut, terminal.Sanitize("psi v"+version))
		for msg, n := range grouped {
			if n > 1 {
				fmt.Fprintln(startupOut, terminal.Sanitize(fmt.Sprintf("note: %s (x%d, see /session)", msg, n)))
			} else {
				for _, d := range diagnostics {
					if d.Message == msg {
						fmt.Fprintln(startupOut, terminal.Sanitize("note: skill "+d.Path+": "+d.Message))
						break
					}
				}
			}
		}
		if r.authErr != nil {
			for _, line := range terminal.FormatNotice(terminal.NoticeWarning, r.authErr.Error()+" (offline commands still work; /model to select, /session for status)", 80, false) {
				fmt.Fprintln(startupOut, line)
			}
		}
		fmt.Fprintln(startupOut, "esc interrupt · ctrl+c/d clear/exit · / commands")
	} else if r.authErr != nil {
		for _, line := range terminal.FormatNotice(terminal.NoticeWarning, r.authErr.Error(), 80, false) {
			fmt.Fprintln(startupOut, line)
		}
	}
	r.refreshPresentation(console)
	return r.repl(console)
}

type runtime struct {
	store                    *session.Store
	reg                      *tools.Registry
	options                  Options
	settings                 config.Settings
	models                   *config.ModelsFile
	skills                   []skills.Skill
	prompt, agentDir         string
	ov                       config.StartupOverrides
	cfg                      agent.Config
	prov                     provider.Provider
	authErr                  error
	generation               int64
	state                    State
	selector                 *Selector
	selectorDraft            string
	selectorCursor           int
	selectorRestoreOnSuccess bool
	tty                      *terminal.Term
	lifecycle                *terminalLifecycle
	signals                  chan os.Signal
	footerKey                string
	footerSnapshot           terminal.PresentationSnapshot
}

func (r *runtime) restore() error {
	proj, err := r.store.Projection()
	if err != nil {
		return err
	}
	thinking := ""
	if r.store.EntryCount() > 0 {
		thinking = proj.State.ThinkingLevel
	}
	ov := config.ResolveStartup(r.options.Provider, r.options.Model, r.options.Thinking, r.options.SessionDir, r.options.NoSession,
		proj.State.Provider, proj.State.ModelID, thinking, r.settings, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
	if ov.Provider == "" {
		ov.Provider = "anthropic"
	}
	if err := r.configure(ov); err != nil {
		if r.options.Provider == "" && r.options.Model == "" && r.options.Thinking == "" && (containsUnsupported(err) || containsNoModel(err)) {
			if r.prov != nil {
				r.prov.CloseIdle()
			}
			r.ov, r.authErr, r.prov, r.reg = ov, err, nil, nil
			r.cfg = agent.Config{Model: provider.ModelMeta{ID: ov.Model, Provider: ov.Provider}, Thinking: ov.Thinking}
			return nil // unsupported imported selections still have readable history
		}
		return err
	}
	return nil
}

func (r *runtime) configure(ov config.StartupOverrides) error {
	meta, err := r.models.SelectModel(ov.Provider, ov.Model)
	if err != nil {
		return err
	}
	ov.Model = meta.ID
	if ov.Thinking == "" {
		ov.Thinking = "off"
	}
	pm := provider.ModelMeta{ID: meta.ID, API: meta.API, Provider: ov.Provider, MaxTokens: meta.MaxTokens, ContextWindow: meta.ContextWindow, Reasoning: meta.Reasoning, InputImage: meta.InputImage, ThinkingLevelMap: meta.ThinkingLevelMap, AdaptiveThinking: meta.Compat.ForceAdaptiveThinking, MidConvoEffort: meta.Compat.SupportsMidConvoEffort}
	cfg := agent.Config{Model: pm, Thinking: ov.Thinking, Tools: tools.Definitions(r.settings.DefaultTools), System: r.prompt,
		AutoCompact: r.settings.Compaction.Enabled, ReserveTokens: r.settings.Compaction.ReserveTokens, KeepRecentTokens: r.settings.Compaction.KeepRecentTokens}
	if _, err := provider.BuildRequest(provider.Request{Model: pm, Thinking: ov.Thinking}); err != nil {
		return err
	}
	headers := map[string]string{}
	for name, value := range meta.Headers {
		resolved, err := config.ResolveCredential(value)
		if err != nil {
			return fmt.Errorf("provider header %s: %w", name, err)
		}
		headers[name] = resolved
	}
	authPath := filepath.Join(r.agentDir, "auth.json")
	idle := time.Duration(max(r.settings.HTTPIdleTimeoutMs, 0)) * time.Millisecond
	var client provider.Provider
	var authErr error
	switch meta.API {
	case "openai-codex-responses":
		if meta.APIKey != "" {
			return fmt.Errorf("openai-codex uses the OAuth login in auth.json, not models.json API keys")
		}
		codex := provider.NewCodexClient(meta.BaseURL, authPath, r.settings.MaxRetries, idle)
		codex.Headers, codex.SessionID = headers, r.store.Header().ID
		authErr = codex.Auth.Check()
		client = codex
	case "anthropic-messages":
		env := envMap()
		if meta.APIKey != "" {
			key, err := config.ResolveCredential(meta.APIKey)
			if err != nil {
				return err
			}
			env["ANTHROPIC_API_KEY"] = key
		}
		key, _, err := provider.SelectAPIKey(env, authPath)
		authErr = err
		anthropic := provider.NewClient(meta.BaseURL, key, r.settings.MaxRetries, idle)
		anthropic.Headers = headers
		client = anthropic
	default:
		return fmt.Errorf("unsupported provider API %q", meta.API)
	}
	if r.prov != nil {
		r.prov.CloseIdle()
	}
	r.ov, r.cfg, r.prov, r.authErr = ov, cfg, client, authErr
	r.reg = tools.NewRegistry(r.store.Cwd())
	r.reg.Bash.ShellPath = r.settings.ShellPath
	r.reg.Bash.ShellPrefix = r.settings.ShellPrefix
	r.reg.Read.BlockImages = !meta.InputImage
	return nil
}

func envMap() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

// sameDir reports whether two paths name one directory. Temporary directories
// on macOS sit behind a /var to /private/var symlink, so a textual
// comparison alone prompts to resume sessions unnecessarily.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && ra == rb
}

func askProjectConsent(o Options, cwd string, console *terminal.Console, lifecycle *terminalLifecycle) (bool, error) {
	if o.DenyProj {
		return false, nil
	}
	if o.AllowProj {
		return true, nil
	}
	hasProject := false
	for _, p := range []string{".pi/settings.json", ".pi/skills", ".pi/SYSTEM.md", ".pi/APPEND_SYSTEM.md", ".agents/skills"} {
		if _, err := os.Stat(filepath.Join(cwd, p)); err == nil {
			hasProject = true
			break
		}
	}
	if !hasProject {
		return false, nil
	}
	picker := &terminal.ChoicePicker{
		Title:   "Project trust",
		Context: "Load protected project resources from " + cwd + "? This applies to this run only.",
		Options: []terminal.ChoiceOption{
			{Label: "Trust for this run", Description: "load project settings, instructions, and skills"},
			{Label: "Deny", Description: "ignore protected project resources", Current: true},
		},
		Selected: 1,
	}
	selected, cancelled, err := runChoicePicker(console, lifecycle, picker)
	if err != nil || cancelled {
		return false, err
	}
	return selected == 0, nil
}

func confirmWorkspaceMismatch(console *terminal.Console, lifecycle *terminalLifecycle, sessionCwd, toolCwd string) (bool, error) {
	picker := &terminal.ChoicePicker{
		Title:   "Workspace confirmation",
		Context: "Session workspace: " + sessionCwd + " · tools will use: " + toolCwd,
		Options: []terminal.ChoiceOption{
			{Label: "Run tools in current workspace", Description: "the session's original workspace is not used"},
			{Label: "Cancel", Description: "do not start this session", Current: true},
		},
		Selected: 1,
	}
	selected, cancelled, err := runChoicePicker(console, lifecycle, picker)
	return !cancelled && selected == 0, err
}

func openSession(o Options, cwd, agentDir string, st config.Settings, ov config.StartupOverrides, console *terminal.Console, lifecycle *terminalLifecycle) (*session.Store, error) {
	dir := session.SessionDirOverride(ov.SessionDir, "", "", cwd, agentDir)
	persist := !ov.NoSession
	if o.SessionPath != "" {
		return session.OpenFile(o.SessionPath, cwd, persist)
	}
	if o.Continue {
		return session.Continue(cwd, dir, agentDir, persist)
	}
	if o.Resume {
		infos, err := workspaceSessions(dir, cwd)
		if err != nil {
			return nil, err
		}
		if len(infos) == 0 {
			return nil, fmt.Errorf("no sessions found in %s", dir)
		}
		selector := NewSelector(infos, "")
		selected, cancelled, err := runSessionPicker(console, lifecycle, selector)
		if err != nil {
			return nil, err
		}
		if cancelled || selected == nil {
			return nil, errStartupCancelled
		}
		return session.OpenFile(selected.Path, cwd, persist)
	}
	return session.New(session.OpenOptions{Cwd: cwd, SessionDir: dir, AgentDir: agentDir, Persist: persist})
}

func workspaceSessions(dir, cwd string) ([]session.SessionInfo, error) {
	infos, err := session.Discover(dir)
	if err != nil {
		return nil, err
	}
	var mine []session.SessionInfo
	for _, info := range infos {
		if info.Cwd == cwd {
			mine = append(mine, info)
		}
	}
	return mine, nil
}

// discoverAll returns every matching workspace session with bounded metadata only.
// Transcript bodies are never loaded for listing (P2-039).
func discoverAll(dir, cwd string) ([]session.SessionInfo, error) { return workspaceSessions(dir, cwd) }

func agentMessageUser(text string) session.AgentMessage {
	return session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: text}}
}

func containsUnsupported(err error) bool {
	return err != nil && containsStr(err.Error(), "unsupported provider")
}

func containsNoModel(err error) bool {
	return err != nil && containsStr(err.Error(), "needs explicit metadata")
}

func containsStr(hay, needle string) bool {
	if len(hay) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

func (r *runtime) installConfiguration(candidate *runtime) {
	if r.prov != nil {
		r.prov.CloseIdle()
	}
	r.ov, r.cfg, r.prov, r.authErr, r.reg = candidate.ov, candidate.cfg, candidate.prov, candidate.authErr, candidate.reg
}

func (r *runtime) refreshPresentation(console *terminal.Console) {
	if console == nil || r.store == nil {
		return
	}
	key := fmt.Sprintf("%s/%d/%s/%s/%s/%s", r.store.Header().ID, r.store.EntryCount(), r.ov.Provider, r.ov.Model, r.ov.Thinking, r.store.Cwd())
	if key != r.footerKey {
		home := os.Getenv("HOME")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		snapshot := terminal.PresentationSnapshot{
			WorkingDirectory: r.store.Cwd(), HomeDirectory: home,
			Provider: r.ov.Provider, Model: r.ov.Model, Thinking: r.ov.Thinking,
		}
		if accounting, err := r.store.Accounting(); err == nil {
			if accounting.Usage.Input > 0 {
				snapshot.Usage = "↑" + formatFooterTokens(accounting.Usage.Input)
			}
			if accounting.Usage.Output > 0 {
				if snapshot.Usage != "" {
					snapshot.Usage += " "
				}
				snapshot.Usage += "↓" + formatFooterTokens(accounting.Usage.Output)
			}
		}
		if projection, err := r.store.Projection(); err == nil && r.cfg.Model.ContextWindow > 0 {
			estimated := agent.EstimatedContext(projection, r.cfg)
			percent := 100 * float64(estimated) / float64(r.cfg.Model.ContextWindow)
			snapshot.ContextEstimate = fmt.Sprintf("~%.1f%%/%s", percent, formatFooterTokens(float64(r.cfg.Model.ContextWindow)))
		}
		r.footerKey, r.footerSnapshot = key, snapshot
	}
	console.SetPresentation(r.footerSnapshot)
}

func formatFooterTokens(n float64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", int64(n))
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", n/1000)
	}
	if n < 1000000 {
		return fmt.Sprintf("%dk", int64(n+500)/1000)
	}
	if n < 10000000 {
		return fmt.Sprintf("%.1fM", n/1000000)
	}
	return fmt.Sprintf("%dM", int64(n+500000)/1000000)
}

func (r *runtime) replace(next *session.Store) error {
	if next.Persistent() && r.store.Persistent() && next.File() != "" && next.File() == r.store.File() {
		return nil
	}
	if err := next.TryLock(); err != nil {
		return err
	}
	candidate := *r
	candidate.store, candidate.prov = next, nil
	if _, err := PrepareActivation(next); err != nil {
		next.Unlock()
		return err
	}
	if err := candidate.restore(); err != nil {
		next.Unlock()
		return err
	}
	old := r.store
	r.installConfiguration(&candidate)
	r.store = next
	old.Unlock()
	return nil
}

func (r *runtime) draw(console *terminal.Console) {
	r.refreshPresentation(console)
	if r.selector != nil {
		console.SetPickerRows(r.selector.RenderPickerRows(console.StatusWidth(), r.footerSnapshot.NoColor))
	} else {
		console.ClearPickerRows()
	}
	console.Draw()
}

func (r *runtime) repl(console *terminal.Console) error {
	out := terminal.RawOutput{W: console}
	print := func(s string) { fmt.Fprintln(out, terminal.Sanitize(s)) }
	notice := func(kind terminal.NoticeKind, text string) {
		for _, line := range terminal.FormatNotice(kind, text, console.StatusWidth(), false) {
			fmt.Fprintln(out, line)
		}
	}
	stream := terminal.NewStreamer(terminal.StreamOutput{Console: console}, true)
	status := terminal.NewStatusWriter(out)
	events := make(chan event.Event, 64)
	done := make(chan error, 1)
	signals := r.signals
	if signals == nil {
		signals = make(chan os.Signal, 8)
		signal.Notify(signals, handledSignals()...)
		defer signal.Stop(signals)
	}
	if r.lifecycle == nil {
		r.lifecycle = &terminalLifecycle{tty: r.tty, console: console, signals: signals}
	}
	busy, exiting, compacting := false, false, false
	var cancel context.CancelFunc
	var outputErr error
	var runOutcome string
	var visibleRunText bool
	var submittedDraft string
	var submittedCursor int
	var suspendPending bool
	inputs := console.Inputs
	var pendingInputs []terminal.Input
	var pendingInputBytes int
	frameTimer := time.NewTimer(time.Hour)
	frameTimer.Stop()
	defer frameTimer.Stop()
	var frameTick <-chan time.Time
	spinnerFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinnerIndex := 0
	activityText := ""
	lastSpinner := time.Time{}
	activityFrame := func() string {
		if activityText == "" {
			return ""
		}
		return spinnerFrames[spinnerIndex] + " " + activityText
	}
	setActivity := func(text string) {
		activityText = text
		spinnerIndex = 0
		lastSpinner = time.Now()
		console.Status(activityFrame())
	}
	cancelActive := func() {
		if !busy || cancel == nil {
			return
		}
		r.state = StateCancelling
		setActivity("Cancelling… (settling)")
		cancel()
	}
	refreshRecall := func() {
		if recall, err := r.store.Recall(); err == nil {
			console.Editor.SetHistory(recall)
		}
	}
	openSelector := func(selector *Selector, draft string, cursor int, query string, restoreOnSuccess bool) {
		r.selector = selector
		r.selectorDraft, r.selectorCursor = draft, cursor
		r.selectorRestoreOnSuccess = restoreOnSuccess
		selector.SetFilter(query)
		console.Editor.RestoreDraft(query, len([]rune(query)))
		r.state = StateSelecting
		r.draw(console)
	}
	closeSelector := func(restoreDraft bool) {
		r.selector = nil
		console.ClearPickerRows()
		r.selectorRestoreOnSuccess = false
		r.state = StateIdle
		if r.authErr != nil {
			r.state = StateOffline
		}
		if restoreDraft {
			console.Editor.RestoreDraft(r.selectorDraft, r.selectorCursor)
			console.RefreshCompletion()
		} else {
			console.Editor.Clear()
		}
		r.selectorDraft, r.selectorCursor = "", 0
		r.draw(console)
	}
	cycleThinking := func() {
		levels := provider.SupportedThinking(r.cfg.Model)
		if len(levels) == 0 {
			return
		}
		current := 0
		for i, level := range levels {
			if level == r.ov.Thinking {
				current = i
				break
			}
		}
		next := levels[(current+1)%len(levels)]
		if _, err := r.command(&terminal.Command{Name: "thinking", Arg: next}, out); err != nil {
			notice(terminal.NoticeError, err.Error())
		}
		refreshRecall()
		r.draw(console)
	}
	console.Commands = terminal.CommandNames()
	console.CompletionDescriptions = make(map[string]string)
	for _, spec := range terminal.CommandMetadata() {
		console.CompletionDescriptions["/"+spec.Name] = spec.Description
	}
	for _, skill := range r.skills {
		console.Skills = append(console.Skills, skill.Name)
		console.CompletionDescriptions["/skill:"+skill.Name] = skill.Description
	}
	emitReplay := func(text string) bool {
		for _, batch := range terminal.Batches(text+"\n", 64*1024) {
		inputScan:
			for i := 0; i < 64; i++ {
				select {
				case input, ok := <-inputs:
					if !ok || input.Err != nil {
						exiting = true
						return false
					}
					if !input.Paste && len(input.Key) == 1 && input.Key[0] == 3 {
						return false
					}
					if len(pendingInputs) >= 8192 || pendingInputBytes+len(input.Key) > 256*1024 {
						pendingInputs = append(pendingInputs, input)
						pendingInputBytes += len(input.Key)
						return false
					}
					pendingInputs = append(pendingInputs, input)
					pendingInputBytes += len(input.Key)
				case sig := <-signals:
					switch {
					case sig == syscall.SIGINT:
						return false
					case isShutdown(sig):
						exiting = true
						return false
					case isSuspend(sig):
						suspendPending = true
						return false
					}
				default:
					break inputScan
				}
			}
			fmt.Fprint(out, batch)
			if console.Err() != nil {
				return false
			}
		}
		return true
	}
	stoppedReplay := func() {
		if !exiting {
			print("History display stopped. Use /history all to display stored messages.")
		}
	}
	r.generation++
	r.state = StateReplaying
	// Startup activation: display current view before the next editable prompt (P2-035).
	// Same projector + replay engine as /resume (P2-036).
	if act, err := PrepareActivation(r.store); err == nil {
		if !replayActivationAtWidth(emitReplay, act, true, "", console.StatusWidth()) {
			stoppedReplay()
		}
		console.Editor.SetHistory(act.Recall)
		if act.Offline {
			r.state = StateOffline
		} else {
			r.state = StateIdle
		}
	} else {
		print("error: " + err.Error())
		r.state = StateIdle
	}
	applyEvent := func(ev event.Event) {
		if ev.Generation != 0 && ev.Generation != r.generation {
			return
		}
		switch ev.Type {
		case event.TextDelta:
			r.state = StateStreaming
			visibleRunText = visibleRunText || ev.Text != ""
			stream.Delta(ev.Text)
		case event.RunStart:
			runOutcome, visibleRunText = "", false
		case event.RunEnd:
			runOutcome = ev.Outcome
		case event.ToolStart:
			stream.Close()
			r.state = StateRunningTool
			setActivity(terminal.FormatStatus(ev.ToolName, terminal.ToolRunning) + " · Esc/Ctrl+C cancel, input paused")
			status.Update(ev.ToolName, ev.ToolCallID, terminal.ToolRunning)
		case event.ToolEnd:
			status.Update(ev.ToolName, ev.ToolCallID, terminal.ToolState(ev.State))
			if r.state != StateCancelling {
				r.state = StateWaiting
				setActivity("Working… (Esc/Ctrl+C cancel, input paused)")
			}
		case event.CompactionStart:
			r.state = StateCompacting
			message := "Compacting context… (Esc/Ctrl+C cancel)"
			switch ev.State {
			case event.CompactionThreshold:
				message = "Auto-compacting context… (Esc/Ctrl+C cancel)"
			case event.CompactionOverflow:
				message = "Context overflow; compacting… (Esc/Ctrl+C cancel)"
			}
			setActivity(message)
		case event.CompactionEnd:
			if r.state != StateCancelling {
				r.state = StateWaiting
				setActivity("Working… (Esc/Ctrl+C cancel, input paused)")
			}
		}
	}
	start := func(text, display, focus, invocation, source string, compact bool) {
		if err := r.store.WriteError(); err != nil {
			r.state = StateStorageBlocked
			notice(terminal.NoticeError, "Storage is blocked: "+err.Error()+" (open a checked copy or /new)")
			console.Editor.RestoreDraft(submittedDraft, submittedCursor)
			console.RefreshCompletion()
			r.draw(console)
			return
		}
		if r.authErr != nil {
			notice(terminal.NoticeError, r.authErr.Error())
			console.Editor.RestoreDraft(submittedDraft, submittedCursor)
			console.RefreshCompletion()
			r.draw(console)
			return
		}
		if strings.TrimSpace(display) != "" {
			for _, line := range terminal.FormatMessage(display, console.StatusWidth(), false) {
				fmt.Fprintln(out, line)
			}
		}
		runOutcome, visibleRunText = "", false
		ctx, stop := context.WithCancel(context.Background())
		cancel, busy, compacting = stop, true, compact
		r.state = StateWaiting
		console.ResetBusyNotice()
		setActivity("Working… (Esc/Ctrl+C cancel, input paused)")
		if compact {
			r.state = StateCompacting
			setActivity("Compacting context… (Esc/Ctrl+C cancel)")
		}
		gen, seq := r.generation, int64(0)
		cfg := r.cfg
		loop := agent.New(r.store, r.reg, r.prov, func(e event.Event) {
			seq++
			e.Generation, e.Seq, e.SessionID = gen, seq, r.store.Header().ID
			events <- e
		})
		go func() {
			if compact {
				done <- loop.Compact(ctx, cfg, focus)
			} else if invocation != "" {
				done <- loop.SubmitWithProvenance(ctx, text, invocation, source, cfg)
			} else {
				done <- loop.Submit(ctx, text, cfg)
			}
		}()
	}
	suspend := func() error {
		if err := r.lifecycle.Suspend(); err != nil {
			return err
		}
		inputs = console.Inputs
		r.draw(console)
		return console.Err()
	}
	r.draw(console)
	for {
		if exiting && !busy {
			return console.Err()
		}
		if suspendPending && !busy {
			suspendPending = false
			if err := suspend(); err != nil {
				return err
			}
		}
		if err := console.Err(); err != nil {
			if !busy {
				return err
			}
			outputErr, exiting = err, true
			cancel()
		}
		inputSource := inputs
		fromPendingInput := len(pendingInputs) > 0
		if fromPendingInput {
			queued := make(chan terminal.Input, 1)
			queued <- pendingInputs[0]
			close(queued)
			inputSource = queued
		}
		if frameTick == nil && (stream.Pending() || console.NeedsDraw() || (busy && activityText != "")) {
			delay := time.Second / 30
			if busy && !stream.Pending() && !console.NeedsDraw() {
				delay = 80 * time.Millisecond
			}
			frameTimer.Reset(delay)
			frameTick = frameTimer.C
		}
		select {
		case <-frameTick:
			frameTick = nil
			console.BeginFrame()
			stream.Flush()
			if busy && activityText != "" && time.Since(lastSpinner) >= 80*time.Millisecond {
				spinnerIndex = (spinnerIndex + 1) % len(spinnerFrames)
				lastSpinner = time.Now()
				console.Status(activityFrame())
			}
			if !busy {
				console.Flush()
			}
			_ = console.EndFrame()
		case sig := <-signals:
			if isResize(sig) {
				r.draw(console)
				continue
			}
			if isSuspend(sig) {
				if busy {
					suspendPending = true
					cancelActive()
				} else if err := suspend(); err != nil {
					return err
				}
				continue
			}
			if isResume(sig) {
				// Reacquire tail only, no full replay (3.3, P2-032).
				if !busy {
					r.draw(console)
				}
				continue
			}
			if isShutdown(sig) {
				exiting = true
			}
			if busy {
				cancelActive()
			} else {
				return nil
			}
		case ev := <-events:
			applyEvent(ev)
		case err := <-done:
			// Drain events before drawing the next prompt. A completed run can
			// otherwise race its final text delta through this select.
			for len(events) > 0 {
				applyEvent(<-events)
			}
			cancel()
			busy = false
			stream.Close()
			if !exiting {
				setActivity("")
			} else {
				activityText = ""
			}
			if errors.Is(err, context.Canceled) {
				if runOutcome == event.OutcomeUnsaved {
					r.state = StateStorageBlocked
					notice(terminal.NoticeError, "UNSAVED output. Storage is blocked; do not continue this session. Open a checked copy or use /new.")
				} else if visibleRunText && runOutcome == event.OutcomeInterrupted {
					notice(terminal.NoticeWarning, "The run was interrupted. Any streamed partial response was saved with an interrupted outcome; review it before continuing.")
				} else if visibleRunText {
					notice(terminal.NoticeWarning, "Visible response text was saved, but the operation was cancelled.")
				} else {
					notice(terminal.NoticeWarning, "Request cancelled.")
				}
			} else if err != nil {
				switch {
				case runOutcome == event.OutcomeUnsaved:
					r.state = StateStorageBlocked
					notice(terminal.NoticeError, "UNSAVED output. Storage is blocked; do not continue this session. Open a checked copy or use /new. "+err.Error())
				case runOutcome == event.OutcomeInterrupted && visibleRunText:
					notice(terminal.NoticeWarning, "The run was interrupted. Any streamed partial response was saved with an interrupted outcome. Review it before continuing. "+err.Error())
				case visibleRunText && runOutcome == event.OutcomeDurable:
					notice(terminal.NoticeWarning, "The visible response was saved, but the operation did not complete. "+err.Error())
				default:
					notice(terminal.NoticeError, err.Error())
				}
			} else if compacting {
				print("(compacted)")
			}
			compacting = false
			if exiting {
				return outputErr
			}
			refreshRecall()
			r.state = StateIdle
			if runOutcome == event.OutcomeUnsaved || r.store.WriteError() != nil {
				r.state = StateStorageBlocked
			}
			if suspendPending {
				suspendPending = false
				if err := suspend(); err != nil {
					return err
				}
			} else {
				console.Status("")
				r.refreshPresentation(console)
				r.draw(console)
			}
		case input, ok := <-inputSource:
			if fromPendingInput {
				pendingInputs = pendingInputs[1:]
				pendingInputBytes -= len(input.Key)
			}
			if !ok {
				inputs = nil
				if busy {
					exiting = true
					cancelActive()
					continue
				}
				return nil
			}
			if !input.Paste && len(input.Key) == 1 && input.Key[0] == 26 {
				if busy {
					suspendPending = true
					cancelActive()
				} else if err := suspend(); err != nil {
					return err
				}
				continue
			}
			if !busy && r.selector != nil {
				key := string(input.Key)
				if !input.Paste {
					switch key {
					case "\x1b", string([]byte{3}):
						closeSelector(true)
						continue
					case "\x1b[A":
						r.selector.Move(-1)
						r.draw(console)
						continue
					case "\x1b[B":
						r.selector.Move(1)
						r.draw(console)
						continue
					case "\x1b[5~":
						r.selector.MovePage(-1)
						r.draw(console)
						continue
					case "\x1b[6~":
						r.selector.MovePage(1)
						r.draw(console)
						continue
					case "\r":
						selected := r.selector.SelectedItem()
						if selected == nil {
							continue
						}
						if r.selector.Thinking {
							if _, err := r.command(&terminal.Command{Name: "thinking", Arg: selected.ID}, out); err != nil {
								notice(terminal.NoticeError, err.Error())
								closeSelector(true)
								continue
							}
							closeSelector(false)
							continue
						}
						if r.selector.Models {
							if _, err := r.command(&terminal.Command{Name: "model", Arg: selected.ID}, out); err != nil {
								notice(terminal.NoticeError, err.Error())
								closeSelector(true)
								continue
							}
							closeSelector(r.selectorRestoreOnSuccess)
							continue
						}
						if selected.ID == r.store.Header().ID {
							print("already on session " + selected.ID)
							closeSelector(false)
							continue
						}
						next, err := session.OpenFile(selected.Path, r.store.Cwd(), r.store.Persistent())
						if err != nil {
							notice(terminal.NoticeError, err.Error())
							closeSelector(true)
							continue
						}
						act, err := PrepareActivation(next)
						if err != nil {
							notice(terminal.NoticeError, err.Error())
							closeSelector(true)
							continue
						}
						oldID := r.store.Header().ID
						if err := r.replace(next); err != nil {
							notice(terminal.NoticeError, err.Error())
							closeSelector(true)
							continue
						}
						print("resumed " + r.store.Header().ID)
						r.generation++
						if !replayActivationAtWidth(emitReplay, act, false, oldID, console.StatusWidth()) {
							stoppedReplay()
						}
						console.Editor.SetHistory(act.Recall)
						refreshRecall()
						closeSelector(false)
						continue
					}
				}
				before := console.Editor.Text()
				_, _, quit, _ := console.Handle(input, false)
				if quit {
					return console.Err()
				}
				if query := console.Editor.Text(); query != before {
					r.selector.SetFilter(query)
					r.draw(console)
				}
				continue
			}
			if !busy && !input.Paste && len(input.Key) == 1 && input.Key[0] == 12 {
				openSelector(modelSelector(r), console.Editor.Text(), console.Editor.Pos(), "", true)
				continue
			}
			if !busy && !input.Paste && string(input.Key) == "\x1b[Z" {
				cycleThinking()
				continue
			}
			draftBefore, cursorBefore := console.Editor.Text(), console.Editor.Pos()
			text, submitted, quit, abort := console.Handle(input, busy)
			if submitted {
				submittedDraft, submittedCursor = draftBefore, cursorBefore
			}
			if abort && busy {
				cancelActive()
			}
			if quit {
				if busy {
					exiting = true
					cancelActive()
					continue
				}
				return nil
			}
			if !submitted {
				continue
			}
			// Only durable user submissions belong to recall, never commands or selector answers.
			refreshRecall()
			if strings.TrimSpace(text) == "" {
				r.draw(console)
				continue
			}
			if cmd, ok := terminal.ParseCommand(text); ok {
				if strings.HasPrefix(cmd.Name, "skill:") {
					name := strings.TrimPrefix(cmd.Name, "skill:")
					expanded, err := skills.Invoke(r.skills, name, cmd.Arg)
					if err != nil {
						print("error: " + err.Error())
						r.draw(console)
					} else {
						// P2-043, P2-054: expanded input + visible invocation provenance, one user entry.
						invocation := "/skill:" + name
						if strings.TrimSpace(cmd.Arg) != "" {
							invocation += " " + cmd.Arg
						}
						start(expanded, text, "", invocation, "skill", false)
					}
					continue
				}
				if cmd.Name == "history" {
					complete, err := renderStoreHistoryAtWidth(r.store, cmd.Arg, emitReplay, console.StatusWidth())
					if err != nil {
						print("error: " + err.Error())
					} else if !complete {
						stoppedReplay()
					}
					r.draw(console)
					continue
				}
				if cmd.Name == "compact" {
					start("", "", cmd.Arg, "", "", true)
					continue
				}
				if cmd.Name == "model" {
					query := strings.TrimSpace(cmd.Arg)
					if query == "" || !r.modelArgumentKnown(query) {
						openSelector(modelSelector(r), submittedDraft, submittedCursor, query, false)
						continue
					}
				}
				if cmd.Name == "thinking" && strings.TrimSpace(cmd.Arg) == "" {
					openSelector(thinkingSelector(r), submittedDraft, submittedCursor, "", false)
					continue
				}
				if cmd.Name == "resume" {
					infos, err := discoverAll(r.store.Dir(), r.store.Cwd())
					if err != nil {
						notice(terminal.NoticeError, err.Error())
						console.Editor.RestoreDraft(submittedDraft, submittedCursor)
						console.RefreshCompletion()
					} else if len(infos) == 0 {
						print("no sessions found")
						console.Editor.RestoreDraft(submittedDraft, submittedCursor)
						console.RefreshCompletion()
					} else {
						openSelector(NewSelector(infos, r.store.Header().ID), submittedDraft, submittedCursor, cmd.Arg, false)
					}
					r.draw(console)
					continue
				}
				quit, err := r.command(cmd, out)
				refreshRecall()
				if err != nil {
					print("error: " + err.Error())
				}
				if quit {
					return nil
				}
				r.draw(console)
				continue
			}
			start(text, text, "", "", "", false)
		}
	}
}

func (r *runtime) modelArgumentKnown(arg string) bool {
	if r.models == nil {
		return false
	}
	parts := strings.Fields(arg)
	providerName, modelID := r.ov.Provider, ""
	switch len(parts) {
	case 1:
		if strings.Contains(parts[0], "/") {
			qualified := strings.SplitN(parts[0], "/", 2)
			providerName, modelID = qualified[0], qualified[1]
		} else {
			modelID = parts[0]
		}
	case 2:
		providerName, modelID = parts[0], parts[1]
	default:
		return false
	}
	if providerName == "" || modelID == "" {
		return false
	}
	_, err := r.models.SelectModel(providerName, modelID)
	return err == nil
}

func (r *runtime) command(cmd *terminal.Command, out io.Writer) (bool, error) {
	print := func(s string) { fmt.Fprintln(out, terminal.Sanitize(s)) }
	switch cmd.Name {
	case "quit":
		return true, nil
	case "help":
		print(terminal.HelpText())
	case "session":
		proj, err := r.store.Projection()
		if err != nil {
			return false, err
		}
		accounting, err := r.store.Accounting()
		if err != nil {
			return false, err
		}
		returned := accounting.ReturnedModel
		if returned == "" {
			returned = "unknown (no stored response)"
		}
		print(fmt.Sprintf("session %s\nfile %s\ncwd %s\nprovider %s api %s\nrequested model %s\nlast returned model %s\nthinking %s\ncontext messages %d, estimated context tokens %d\nstored usage: input %g, output %g, cache read %g, cache write %g, total %g (%d attempts)\nprice unknown (no cost calculation)\ncontinuation: %s", r.store.Header().ID, r.store.File(), r.store.Cwd(), r.ov.Provider, r.cfg.Model.API, r.ov.Model, returned, provider.ThinkingDescription(r.cfg.Model, r.ov.Thinking), len(proj.Flat), agent.EstimatedContext(proj, r.cfg), accounting.Usage.Input, accounting.Usage.Output, accounting.Usage.CacheRead, accounting.Usage.CacheWrite, accounting.Usage.TotalTokens, accounting.Attempts, continuable(proj)))
	case "new":
		next, err := session.New(session.OpenOptions{Cwd: r.store.Cwd(), SessionDir: r.store.Dir(), AgentDir: r.agentDir, Persist: r.store.Persistent()})
		if err != nil {
			return false, err
		}
		if err := r.replace(next); err != nil {
			return false, err
		}
		r.generation++
		// Reset recall and session-local state; keep prior scrollback (P2-048).
		// New sessions start with empty recall.
		print("new session " + next.Header().ID)
	case "history":
		// /history [N|all]: append last N user spans or full active-branch history (P2-041).
		// Display-only: no model request, no file change.
		_, err := renderStoreHistory(r.store, cmd.Arg, func(s string) bool { print(s); return true })
		if err != nil {
			return false, err
		}
	case "model":
		if strings.TrimSpace(cmd.Arg) == "" {
			print(modelList(r))
			return false, nil
		}
		next := r.ov
		parts := strings.Fields(cmd.Arg)
		if len(parts) == 1 {
			if strings.Contains(parts[0], "/") {
				pp := strings.SplitN(parts[0], "/", 2)
				next.Provider, next.Model = pp[0], pp[1]
			} else {
				next.Model = parts[0]
			}
		} else if len(parts) == 2 {
			next.Provider, next.Model = parts[0], parts[1]
		} else {
			return false, fmt.Errorf("use /model [provider/]model")
		}
		candidate := *r
		candidate.prov = nil
		if err := candidate.configure(next); err != nil {
			return false, err
		}
		if _, err := r.store.AppendModelChange(candidate.ov.Provider, candidate.ov.Model); err != nil {
			candidate.prov.CloseIdle()
			return false, err
		}
		r.installConfiguration(&candidate)
		print(fmt.Sprintf("%s/%s thinking %s (session-local)", r.ov.Provider, r.ov.Model, r.ov.Thinking))
	case "thinking":
		if strings.TrimSpace(cmd.Arg) == "" {
			print(fmt.Sprintf("thinking %s (supported: %s)", provider.ThinkingDescription(r.cfg.Model, r.ov.Thinking), provider.ThinkingChoices(r.cfg.Model)))
			return false, nil
		}
		lvl := strings.TrimSpace(cmd.Arg)
		found := false
		for _, v := range config.ValidThinkingLevels {
			if v == lvl {
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Errorf("unsupported thinking level %q", lvl)
		}
		next := r.ov
		next.Thinking = lvl
		candidate := *r
		candidate.prov = nil
		if err := candidate.configure(next); err != nil {
			return false, err
		}
		if _, err := r.store.AppendThinkingLevelChange(lvl); err != nil {
			candidate.prov.CloseIdle()
			return false, err
		}
		r.installConfiguration(&candidate)
		print(fmt.Sprintf("thinking %s (session-local)", provider.ThinkingDescription(r.cfg.Model, lvl)))
	default:
		return false, fmt.Errorf("unknown command (see /help)")
	}
	return false, nil
}
