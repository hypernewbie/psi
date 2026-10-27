// Package app wires the interactive terminal to the real agent and session lifecycle.
package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	agentDir := session.AgentDir()
	consent := askProjectConsent(o, cwd)
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
	ov := config.ResolveStartup(o.Provider, o.Model, o.Thinking, o.SessionDir, o.NoSession, "", "", "", st, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
	store, err := openSession(o, cwd, agentDir, st, ov)
	if err != nil {
		return err
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
	if !st.QuietStartup {
		fmt.Println(terminal.Sanitize(terminal.StartupLine(r.ov.Provider, r.ov.Model, cwd, store.Header().ID, len(sk))))
		for _, d := range diagnostics {
			fmt.Println(terminal.Sanitize("note: skill " + d.Path + ": " + d.Message))
		}
		if r.authErr != nil {
			fmt.Println(terminal.Sanitize("warning: " + r.authErr.Error() + " (offline commands still work)"))
		}
		fmt.Println("Enter submits · Ctrl+J newline · Ctrl+C cancel · /help · /quit")
	}
	tty, _, err := terminal.Acquire()
	if err != nil {
		return err
	}
	defer tty.Restore()
	console := terminal.NewConsole(os.Stdin, os.Stdout)
	defer console.Close()
	return r.repl(console)
}

type runtime struct {
	store            *session.Store
	reg              *tools.Registry
	options          Options
	settings         config.Settings
	models           *config.ModelsFile
	skills           []skills.Skill
	prompt, agentDir string
	ov               config.StartupOverrides
	cfg              agent.Config
	prov             provider.Provider
	authErr          error
	generation       int64
	state            State
	selector         *Selector
}

func (r *runtime) restore() error {
	proj, err := r.store.Projection()
	if err != nil {
		return err
	}
	thinking := ""
	if len(r.store.Entries()) > 0 {
		thinking = proj.State.ThinkingLevel
	}
	ov := config.ResolveStartup(r.options.Provider, r.options.Model, r.options.Thinking, r.options.SessionDir, r.options.NoSession,
		proj.State.Provider, proj.State.ModelID, thinking, r.settings, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
	if ov.Provider == "" {
		ov.Provider = "anthropic"
	}
	return r.configure(ov)
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
	pm := provider.ModelMeta{ID: meta.ID, API: meta.API, Provider: ov.Provider, MaxTokens: meta.MaxTokens, ContextWindow: meta.ContextWindow, Reasoning: meta.Reasoning, InputImage: meta.InputImage, ThinkingLevelMap: meta.ThinkingLevelMap}
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

func askProjectConsent(o Options, cwd string) bool {
	if o.DenyProj {
		return false
	}
	if o.AllowProj {
		return true
	}
	hasProject := false
	for _, p := range []string{".pi/settings.json", ".pi/skills", ".pi/SYSTEM.md", ".pi/APPEND_SYSTEM.md", ".agents/skills"} {
		if _, err := os.Stat(filepath.Join(cwd, p)); err == nil {
			hasProject = true
			break
		}
	}
	if !hasProject {
		return false
	}
	fmt.Printf("%s [y/N] ", terminal.Sanitize("Load protected project resources from "+cwd+"?"))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
}

func openSession(o Options, cwd, agentDir string, st config.Settings, ov config.StartupOverrides) (*session.Store, error) {
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
		for i, info := range infos {
			fmt.Println(terminal.Sanitize(fmt.Sprintf("%d) %s — %s", i+1, info.ID, firstLine(info.FirstMessage))))
		}
		fmt.Print("Select session number: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return nil, err
		}
		idx, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || idx < 1 || idx > len(infos) {
			return nil, fmt.Errorf("invalid session selection")
		}
		return session.OpenFile(infos[idx-1].Path, cwd, persist)
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

func (r *runtime) replace(next *session.Store) error {
	if next.Persistent() && r.store.Persistent() && next.File() != "" && next.File() == r.store.File() {
		return nil
	}
	if err := next.TryLock(); err != nil {
		return err
	}
	old := r.store
	r.store = next
	if err := r.restore(); err != nil {
		next.Unlock()
		r.store = old
		return err
	}
	old.Unlock()
	return nil
}

func (r *runtime) repl(console *terminal.Console) error {
	out := terminal.RawOutput{W: os.Stdout}
	print := func(s string) { fmt.Fprintln(out, terminal.Sanitize(s)) }
	stream := terminal.NewStreamer(out, true)
	status := terminal.NewStatusWriter(out)
	events := make(chan event.Event, 64)
	done := make(chan error, 1)
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)
	defer signal.Stop(signals)
	busy, exiting, compacting := false, false, false
	var cancel context.CancelFunc
	r.generation++
	r.state = StateReplaying
	// Startup activation: display current view before the next editable prompt (P2-035).
	// Same projector + replay engine as /resume (P2-036).
	if act, err := PrepareActivation(r.store); err == nil {
		replayActivation(print, act, true, "")
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
	start := func(text, focus string, compact bool) {
		if r.authErr != nil {
			print("error: " + r.authErr.Error())
			console.Draw()
			return
		}
		ctx, stop := context.WithCancel(context.Background())
		cancel, busy, compacting = stop, true, compact
		if compact {
			print("(compacting...)")
		}
		gen := r.generation
		seq := int64(0)
		loop := agent.New(r.store, r.reg, r.prov, func(e event.Event) {
			seq++
			e.Generation = gen
			e.Seq = seq
			events <- e
		})
		go func() {
			if compact {
				done <- loop.Compact(ctx, r.cfg, focus)
			} else {
				done <- loop.Submit(ctx, text, r.cfg)
			}
		}()
	}
	console.Draw()
	inputs := console.Inputs
	for {
		select {
		case sig := <-signals:
			if sig == syscall.SIGWINCH {
				if !busy {
					console.Draw()
				}
				continue
			}
			if sig == syscall.SIGTERM {
				exiting = true
			}
			if busy {
				cancel()
			} else {
				return nil
			}
		case ev := <-events:
			// Reject stale events after session replacement (P2-060).
			if ev.Generation != 0 && ev.Generation != r.generation {
				continue
			}
			switch ev.Type {
			case event.TextDelta:
				stream.Delta(ev.Text)
			case event.ToolStart:
				stream.Close()
				status.Update(ev.ToolName, ev.ToolCallID, terminal.ToolRunning)
			case event.ToolEnd:
				status.Update(ev.ToolName, ev.ToolCallID, terminal.ToolState(ev.State))
			}
		case err := <-done:
			// Drain events before drawing the next prompt. A completed run can
			// otherwise race its final text delta through this select.
			for len(events) > 0 {
				ev := <-events
				if ev.Generation != 0 && ev.Generation != r.generation {
					continue
				}
				switch ev.Type {
				case event.TextDelta:
					stream.Delta(ev.Text)
				case event.ToolStart:
					stream.Close()
					status.Update(ev.ToolName, ev.ToolCallID, terminal.ToolRunning)
				case event.ToolEnd:
					status.Update(ev.ToolName, ev.ToolCallID, terminal.ToolState(ev.State))
				}
			}
			cancel()
			busy = false
			stream.Close()
			if errors.Is(err, context.Canceled) {
				print("(cancelled)")
			} else if err != nil {
				print("error: " + err.Error())
			} else if compacting {
				print("(compacted)")
			}
			compacting = false
			if exiting {
				return nil
			}
			console.Draw()
		case input, ok := <-inputs:
			if !ok {
				inputs = nil
				if busy {
					cancel()
					exiting = true
					continue
				}
				return nil
			}
			text, submitted, quit, abort := console.Handle(input, busy)
			if abort && busy {
				cancel()
			}
			if quit {
				if busy {
					exiting = true
					cancel()
					continue
				}
				return nil
			}
			if !submitted {
				continue
			}
			if r.selector != nil {
				sel, cancelSel, redraw := r.selector.Resolve(text)
				if cancelSel {
					// Cancelled selector preserves active session, draft, recall (P2-040).
					r.selector = nil
					r.state = StateIdle
					console.Draw()
					continue
				}
				if redraw {
					for _, line := range r.selector.Render() {
						print(line)
					}
					console.Draw()
					continue
				}
				if sel != nil {
					if sel.ID == r.store.Header().ID {
						// Same-session is a no-op, not another transcript copy (3.3).
						print("already on session " + sel.ID)
					} else {
						next, err := session.OpenFile(sel.Path, r.store.Cwd(), r.store.Persistent())
						if err != nil {
							print("error: " + err.Error())
						} else if act, err := PrepareActivation(next); err != nil {
							print("error: " + err.Error())
							// Failed candidate leaves old session/draft/recall/provider/lock intact (P2-012).
						} else {
							oldID := r.store.Header().ID
							if err := r.replace(next); err != nil {
								print("error: " + err.Error())
							} else {
								print("resumed " + r.store.Header().ID)
								r.generation++
								replayActivation(print, act, false, oldID)
								console.Editor.SetHistory(act.Recall)
								if act.Offline {
									r.state = StateOffline
								} else {
									r.state = StateIdle
								}
							}
						}
					}
					r.selector = nil
					r.state = StateIdle
					console.Draw()
					continue
				}
				// Invalid selection: reprompt within same bounded flow (P2-040).
				for _, line := range r.selector.Render() {
					print(line)
				}
				console.Draw()
				continue
			}
			if strings.TrimSpace(text) == "" {
				console.Draw()
				continue
			}
			if cmd, ok := terminal.ParseCommand(text); ok {
				if strings.HasPrefix(cmd.Name, "skill:") {
					expanded, err := skills.Invoke(r.skills, strings.TrimPrefix(cmd.Name, "skill:"), cmd.Arg)
					if err != nil {
						print("error: " + err.Error())
						console.Draw()
					} else {
						start(expanded, "", false)
					}
					continue
				}
				if cmd.Name == "compact" {
					start("", cmd.Arg, true)
					continue
				}
				if cmd.Name == "resume" {
					// Complete inline selector with 20/page + filter (P2-039).
					// Filter arg: /resume [filter].
					infos, err := discoverAll(r.store.Dir(), r.store.Cwd())
					if err != nil {
						print("error: " + err.Error())
					} else if len(infos) == 0 {
						print("no sessions found")
					} else {
						r.selector = NewSelector(infos, r.store.Header().ID)
						if strings.TrimSpace(cmd.Arg) != "" {
							r.selector.SetFilter(strings.TrimSpace(cmd.Arg))
						}
						r.state = StateSelecting
						for _, line := range r.selector.Render() {
							print(line)
						}
					}
					console.Draw()
					continue
				}
				quit, err := r.command(cmd, out)
				if err != nil {
					print("error: " + err.Error())
				}
				if quit {
					return nil
				}
				console.Draw()
				continue
			}
			start(text, "", false)
		}
	}
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
		print(fmt.Sprintf("session %s\nfile %s\ncwd %s\nprovider/model %s/%s thinking %s\ncontext messages %d", r.store.Header().ID, r.store.File(), r.store.Cwd(), r.ov.Provider, r.ov.Model, r.ov.Thinking, len(proj.Flat)))
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
		path, err := r.store.ActivePath()
		if err != nil {
			return false, err
		}
		arg := strings.TrimSpace(cmd.Arg)
		if arg == "" || arg == "20" {
			arg = "20"
		}
		if arg == "all" {
			for _, it := range session.AllHistoryView(path) {
				switch it.Kind {
				case session.KindUser:
					print("You (history)")
					print(it.Text)
					print("")
				case session.KindAssistant:
					print("Assistant (history)")
					print(it.Text)
					print("")
				case session.KindTool:
					print(terminal.Sanitize(it.Text))
				case session.KindCompaction:
					print("[historical] " + it.Text)
				}
			}
			return false, nil
		}
		var n int
		if _, err := fmt.Sscanf(arg, "%d", &n); err != nil || n <= 0 {
			return false, fmt.Errorf("use /history [N|all]")
		}
		items := session.CurrentView(path)
		// Last N user spans with their following assistant/tools.
		var spans [][]session.TranscriptItem
		var cur []session.TranscriptItem
		for _, it := range items {
			if it.Kind == session.KindUser && len(cur) > 0 {
				spans = append(spans, cur)
				cur = nil
			}
			cur = append(cur, it)
		}
		if len(cur) > 0 {
			spans = append(spans, cur)
		}
		if n > len(spans) {
			n = len(spans)
		}
		for _, span := range spans[len(spans)-n:] {
			for _, it := range span {
				if it.Kind == session.KindUser {
					print("You (history)")
					print(it.Text)
				} else if it.Kind == session.KindAssistant {
					print("Assistant (history)")
					print(it.Text)
				} else if it.Kind == session.KindTool {
					print(terminal.Sanitize(it.Text))
				}
			}
			print("")
		}
	case "model", "thinking":
		if strings.TrimSpace(cmd.Arg) == "" {
			print(fmt.Sprintf("%s/%s thinking %s", r.ov.Provider, r.ov.Model, r.ov.Thinking))
			return false, nil
		}
		next := r.ov
		if cmd.Name == "thinking" {
			next.Thinking = strings.TrimSpace(cmd.Arg)
		} else {
			parts := strings.Fields(cmd.Arg)
			if len(parts) == 1 {
				next.Model = parts[0]
			} else if len(parts) == 2 {
				next.Provider, next.Model = parts[0], parts[1]
			} else {
				return false, fmt.Errorf("use /model [provider] model")
			}
		}
		// Validate before recording the change. A failed model selection must
		// neither poison the session nor change its in-memory configuration.
		old := r.ov
		if err := r.configure(next); err != nil {
			return false, err
		}
		var err error
		if cmd.Name == "model" {
			_, err = r.store.AppendModelChange(r.ov.Provider, r.ov.Model)
		} else {
			_, err = r.store.AppendThinkingLevelChange(r.ov.Thinking)
		}
		if err != nil {
			_ = r.configure(old)
			return false, err
		}
		print(fmt.Sprintf("%s/%s thinking %s (session-local)", r.ov.Provider, r.ov.Model, r.ov.Thinking))
	default:
		return false, fmt.Errorf("unknown command (see /help)")
	}
	return false, nil
}
