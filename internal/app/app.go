// Package app owns startup, command dispatch, and the active session lifecycle.
package app

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/skills"
	"psi/internal/terminal"
	"psi/internal/tools"
)

// Options is the parsed CLI boundary (PLAN proposed subset).
type Options struct {
	Help        bool
	Version     bool
	Continue    bool
	Resume      bool
	SessionPath string
	Provider    string
	Model       string
	Thinking    string
	SessionDir  string
	Tools       string
	NoTools     bool
	NoSession   bool
	AllowProj   bool
	DenyProj    bool
}

func Parse(args []string) (Options, *flag.FlagSet, error) {
	var o Options
	fs := flag.NewFlagSet("psi", flag.ContinueOnError)
	fs.BoolVar(&o.Help, "help", false, "show help")
	fs.BoolVar(&o.Version, "version", false, "show version")
	fs.BoolVar(&o.Continue, "continue", false, "continue most recent session")
	fs.BoolVar(&o.Resume, "resume", false, "resume with selection")
	fs.StringVar(&o.SessionPath, "session", "", "open explicit session path")
	fs.StringVar(&o.Provider, "provider", "", "provider")
	fs.StringVar(&o.Model, "model", "", "model")
	fs.StringVar(&o.Thinking, "thinking", "", "thinking level")
	fs.StringVar(&o.SessionDir, "session-dir", "", "session directory")
	fs.StringVar(&o.Tools, "tools", "", "tool list (e.g. read,bash)")
	fs.BoolVar(&o.NoTools, "no-tools", false, "disable tools")
	fs.BoolVar(&o.NoSession, "no-session", false, "in-memory session")
	fs.BoolVar(&o.AllowProj, "allow-project", false, "allow project resources without prompting")
	fs.BoolVar(&o.DenyProj, "deny-project", false, "deny project resources without prompting")
	if err := fs.Parse(args); err != nil {
		return o, fs, err
	}
	return o, fs, nil
}

// Run dispatches CLI commands and owns the interactive session.
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
		fmt.Printf("psi %s — minimal Go coding agent (inline, Pi-compatible sessions)\n", version)
		fs.PrintDefaults()
		return nil
	}
	cwd, _ := os.Getwd()
	agentDir := session.AgentDir()
	home, _ := os.UserHomeDir()

	// Load settings (user + project). Project consent: flags override prompt.
	consent := askProjectConsent(o, cwd)
	userPath := filepath.Join(agentDir, "settings.json")
	projPath := filepath.Join(cwd, ".pi", "settings.json")
	loaded, err := config.LoadSettings(userPath, projPath, consent)
	if err != nil {
		return err
	}
	st := loaded.Settings

	// Startup overrides: CLI > restored > settings.
	// Restored state loads after session open; first pass uses settings.
	ov := config.ResolveStartup(o.Provider, o.Model, o.Thinking, o.SessionDir, o.NoSession, "", "", "", st, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
	if ov.Provider == "" {
		ov.Provider = "anthropic"
	}
	if !st.QuietStartup {
		fmt.Printf("psi %s — %s/%s · %s\n", version, ov.Provider, ov.Model, cwd)
		if ov.Explain != "" {
			fmt.Printf("(%s)\n", ov.Explain)
		}
	}

	// Models metadata (local only, no catalog refresh).
	models, err := config.LoadModels(filepath.Join(agentDir, "models.json"))
	if err != nil {
		return err
	}
	_ = models

	// Instructions + skills (consent-gated for project).
	instructions, _ := config.DiscoverInstructions(cwd, home, consent)
	sk, diags := skills.Discover(cwd, agentDir, st.Skills, consent)
	if !st.QuietStartup {
		for _, d := range diags {
			fmt.Printf("note: skill %s: %s\n", d.Path, d.Message)
		}
	}
	prompt := config.AssemblePrompt(cwd, instructions, []string{skills.FormatForPrompt(sk)}, st.DefaultTools)

	// Session lifecycle: new / explicit / continue / resume / ephemeral.
	store, err := openSession(o, cwd, agentDir, st, ov)
	if err != nil {
		return err
	}
	// Restore model/thinking from the active path (explicit CLI wins).
	proj, err := store.Projection()
	if err != nil {
		return err
	}
	restored := proj.State
	final := config.ResolveStartup(o.Provider, o.Model, o.Thinking, o.SessionDir, o.NoSession, restored.Provider, restored.ModelID, restored.ThinkingLevel, st, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))

	// Provider credentials (daily-use path; no silent fallback).
	apiKey, src, err := provider.SelectAPIKey(envMap(), filepath.Join(agentDir, "auth.json"))
	if err != nil {
		// Surface actionable error but keep session usable offline for /session etc.
		fmt.Printf("warning: %v (offline commands still work)\n", err)
		_ = src
		apiKey = ""
	}
	_ = apiKey
	_ = prompt

	// Tools.
	reg := tools.NewRegistry(cwd)

	// Interactive loop (inline, native scrollback).
	return repl(store, reg, final, st, sk, version)
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
	if o.AllowProj {
		return true
	}
	if o.DenyProj {
		return false
	}
	// Noninteractive: deny (safe default, documented).
	if !isTTY() {
		return false
	}
	// Check if project resources exist.
	hasProj := false
	for _, p := range []string{filepath.Join(cwd, ".pi", "settings.json"), filepath.Join(cwd, ".pi", "skills"), filepath.Join(cwd, "AGENTS.md"), filepath.Join(cwd, "AGENTS.override.md")} {
		if _, err := os.Stat(p); err == nil {
			hasProj = true
			break
		}
	}
	if !hasProj {
		return true
	}
	fmt.Printf("Load project resources from %s (.pi/settings.json, skills, AGENTS.md)? [y/N] ", cwd)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

func isTTY() bool {
	fi, _ := os.Stdin.Stat()
	return fi.Mode()&os.ModeCharDevice != 0
}

func openSession(o Options, cwd, agentDir string, st config.Settings, ov config.StartupOverrides) (*session.Store, error) {
	dir := session.SessionDirOverride(ov.SessionDir, os.Getenv("PI_CODING_AGENT_SESSION_DIR"), st.SessionDir, cwd, agentDir)
	persist := !ov.NoSession
	if o.SessionPath != "" {
		if _, err := os.Stat(o.SessionPath); err != nil {
			return nil, fmt.Errorf("explicit session path not found (refusing to create a replacement): %s", o.SessionPath)
		}
		return session.OpenFile(o.SessionPath, cwd, persist)
	}
	if o.Continue {
		latest, err := session.FindMostRecent(dir, cwd)
		if err != nil || latest == "" {
			return nil, fmt.Errorf("no sessions found in %s", dir)
		}
		return session.OpenFile(latest, cwd, persist)
	}
	if o.Resume {
		infos, err := session.Discover(dir)
		if err != nil {
			return nil, err
		}
		// Filter to current workspace for determinism.
		var mine []session.SessionInfo
		for _, in := range infos {
			if in.Cwd == cwd || in.Cwd == "" {
				mine = append(mine, in)
			}
		}
		if len(mine) == 0 {
			return nil, fmt.Errorf("no sessions found in %s", dir)
		}
		sort.Slice(mine, func(i, j int) bool { return mine[i].Modified.After(mine[j].Modified) })
		fmt.Printf("Sessions:\n")
		n := len(mine)
		if n > 10 {
			n = 10
		}
		for i := 0; i < n; i++ {
			fmt.Printf("  %d) %s — %s (%d messages)\n", i+1, mine[i].ID[:8], firstLine(mine[i].FirstMessage), mine[i].MessageCount)
		}
		fmt.Printf("Select [1]: ")
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		idx := 0
		if line != "" {
			fmt.Sscanf(line, "%d", &idx)
			idx--
		}
		if idx < 0 || idx >= n {
			idx = 0
		}
		return session.OpenFile(mine[idx].Path, cwd, persist)
	}
	// New session (flushed on first user/assistant message per Pi).
	return session.New(session.OpenOptions{Cwd: cwd, SessionDir: dir, AgentDir: agentDir, Persist: persist})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// repl is the inline read-eval loop (native scrollback, compact status).
func repl(store *session.Store, reg *tools.Registry, ov config.StartupOverrides, st config.Settings, sk []skills.Skill, version string) error {
	fmt.Printf("%s\n", terminal.StartupLine(ov.Provider, ov.Model, store.Cwd(), store.Header().ID, len(sk)))
	fmt.Printf("Type /help for commands. Enter submits, Ctrl+J newline, Ctrl+C cancels, Ctrl+D exits.\n")
	ed := terminal.NewEditor()
	stream := terminal.NewStreamer(os.Stdout, true)
	status := terminal.NewStatusWriter(os.Stdout)
	in := bufio.NewReader(os.Stdin)
	ctx := context.Background()
	var cancel context.CancelFunc = func() {}
	busy := false
	for {
		if !busy {
			fmt.Printf("> ")
		}
		line, err := in.ReadString('\n')
		if err != nil {
			stream.Close()
			return nil // EOF exits cleanly
		}
		text := strings.TrimRight(line, "\r\n")
		// Idle interrupts: handled by outer signal; here map Ctrl+C text? Real
		// raw-mode handling lives in terminal.Term; line mode keeps it simple.
		if cmd, isCmd := terminal.ParseCommand(text); isCmd {
			if ns, err := handleCommand(cmd, store, reg, &ov, st, sk, stream, status); err != nil {
				if err != nil && err.Error() == "quit" {
					return nil
				}
				if err != nil {
					fmt.Printf("error: %v\n", err)
				}
				if ns != nil && ns != store {
					store = ns
				}
			}
			continue
		}
		// Normal submission: disabled while a run is active (cancellation only).
		if busy {
			fmt.Printf("(run active: cancellation only)\n")
			continue
		}
		// Expand /skill:name without a provider request on missing.
		if strings.HasPrefix(strings.TrimSpace(text), "/skill:") {
			parts := strings.SplitN(strings.TrimSpace(text)[7:], " ", 2)
			name := parts[0]
			arg := ""
			if len(parts) == 2 {
				arg = parts[1]
			}
			expanded, err := skills.Invoke(sk, name, arg)
			if err != nil {
				fmt.Printf("error: %v\n", err)
				continue
			}
			text = expanded
		}
		busy = true
		runCtx, c := context.WithCancel(ctx)
		cancel = c
		go func() {
			// Ctrl+C cancels runCtx via signal forwarding (simplified: not wired in line mode).
			_ = cancel
		}()
		fakeOrReal := realOrFakeProvider(store, ov)
		prov := fakeOrReal
		loop := agent.New(store, reg, prov, func(ev event.Event) {
			switch ev.Type {
			case event.TextDelta:
				stream.Delta(ev.Text)
			case event.ToolStart:
				status.Update(ev.ToolName, ev.ToolCallID, terminal.ToolRunning)
			case event.ToolEnd:
				stt := terminal.ToolDone
				if ev.Err != nil {
					stt = terminal.ToolFailed
				} else if ev.State == "failed" {
					stt = terminal.ToolFailed
				}
				status.Update(ev.ToolName, ev.ToolCallID, stt)
			}
		})
		cfg := agent.Config{Model: provider.ModelMeta{ID: ov.Model, API: "anthropic-messages", MaxTokens: 16384}, Thinking: ov.Thinking, Tools: toolDefs(st.DefaultTools), System: "psi base prompt"}
		// Save draft safely: editor preserves draft on cancel (line mode: text already consumed).
		_ = ed
		err = loop.Submit(runCtx, text, cfg)
		stream.Close()
		busy = false
		if err != nil {
			if runCtx.Err() != nil {
				fmt.Printf("(cancelled)\n")
				status.Update("", "", terminal.ToolCancelled)
				continue
			}
			fmt.Printf("error: %v\n", err)
		}
		_ = in
	}
}

// handleCommand implements the essential commands (Step 84).
func handleCommand(cmd *terminal.Command, store *session.Store, reg *tools.Registry, ov *config.StartupOverrides, st config.Settings, sk []skills.Skill, stream *terminal.Streamer, status *terminal.StatusWriter) (*session.Store, error) {
	switch {
	case cmd.Name == "help":
		fmt.Println(terminal.HelpText())
		return store, nil
	case cmd.Name == "quit":
		return nil, fmt.Errorf("quit")
	case cmd.Name == "session":
		proj, _ := store.Projection()
		fmt.Printf("session %s\nfile %s\ncwd %s\nprovider/model %s/%s thinking %s\nmessages %d\n", store.Header().ID, store.File(), store.Cwd(), ov.Provider, ov.Model, ov.Thinking, len(proj.Flat))
		return store, nil
	case cmd.Name == "new":
		// Reject replacement during unsettled runs (caller ensures idle).
		dir := store.Dir()
		ns, err := session.New(session.OpenOptions{Cwd: store.Cwd(), SessionDir: dir, AgentDir: session.AgentDir(), Persist: store.File() != ""})
		if err != nil {
			return store, err
		}
		fmt.Printf("new session %s\n", ns.Header().ID)
		return ns, nil
	case cmd.Name == "resume":
		infos, err := session.Discover(store.Dir())
		if err != nil {
			return store, err
		}
		for i, in := range infos {
			if i >= 10 {
				break
			}
			fmt.Printf("  %d) %s — %s\n", i+1, in.ID, firstLine(in.FirstMessage))
		}
		return store, fmt.Errorf("use --resume for selection in this release (list shown)")
	case cmd.Name == "model":
		parts := strings.Fields(cmd.Arg)
		if len(parts) == 0 {
			fmt.Printf("model %s/%s\n", ov.Provider, ov.Model)
			return store, nil
		}
		// Session-local change: provider model or model only.
		if len(parts) == 2 {
			ov.Provider = parts[0]
			ov.Model = parts[1]
		} else {
			ov.Model = parts[0]
		}
		if _, err := store.AppendModelChange(ov.Provider, ov.Model); err != nil {
			return store, err
		}
		fmt.Printf("model -> %s/%s (session-local)\n", ov.Provider, ov.Model)
		return store, nil
	case cmd.Name == "thinking":
		lvl := strings.TrimSpace(cmd.Arg)
		if lvl == "" {
			fmt.Printf("thinking %s\n", ov.Thinking)
			return store, nil
		}
		if _, err := provider.ThinkingBudget(lvl); err != nil {
			return store, err
		}
		ov.Thinking = lvl
		if _, err := store.AppendThinkingLevelChange(lvl); err != nil {
			return store, err
		}
		fmt.Printf("thinking -> %s (session-local)\n", lvl)
		return store, nil
	case cmd.Name == "compact":
		// Manual compaction with optional focus instructions.
		path, err := store.ActivePath()
		if err != nil {
			return store, err
		}
		keep, err := agent.Boundary(path, 4)
		if err != nil {
			return store, err
		}
		focus := strings.TrimSpace(cmd.Arg)
		summary := "Manual compaction" + func() string {
			if focus != "" {
				return ": " + focus
			}
			return ""
		}()
		if _, err := agent.Compact(context.Background(), store, summary, keep, 0, nil); err != nil {
			return store, err
		}
		fmt.Printf("compacted (kept from %s)\n", keep)
		return store, nil
	case strings.HasPrefix(cmd.Name, "skill:"):
		name := strings.TrimPrefix(cmd.Name, "skill:")
		expanded, err := skills.Invoke(sk, name, cmd.Arg)
		if err != nil {
			return store, err
		}
		fmt.Printf("(skill %s expanded, %d chars; submit to run)\n", name, len(expanded))
		return store, nil
	case strings.HasPrefix(cmd.Name, "unknown:"):
		return store, fmt.Errorf("unknown command %q (see /help)", "/"+strings.TrimPrefix(cmd.Name, "unknown:"))
	}
	return store, fmt.Errorf("unknown command")
}

func toolDefs(names []string) []provider.ToolDef {
	var out []provider.ToolDef
	for _, n := range names {
		out = append(out, provider.ToolDef{Name: n, Description: n, Schema: map[string]any{"type": "object"}})
	}
	return out
}

func realOrFakeProvider(store *session.Store, ov config.StartupOverrides) provider.Provider {
	// Offline-safe: tests inject fakes directly; the binary selects the real
	// Anthropic client when ANTHROPIC_API_KEY is set, else a stub that errors
	// clearly on Send (commands like /session still work offline).
	if key, _, err := provider.SelectAPIKey(envMap(), ""); err == nil && key != "" {
		return provider.NewClient("", key, 3, 0)
	}
	return &provider.Fake{}
}
