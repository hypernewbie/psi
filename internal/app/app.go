// Package app wires the interactive terminal to the real agent and session lifecycle.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"psi/docs"
	"psi/internal/ac"
	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/event"
	"psi/internal/loop"
	"psi/internal/pibridge"
	"psi/internal/provider"
	"psi/internal/providerregistry"
	"psi/internal/session"
	"psi/internal/skills"
	"psi/internal/terminal"
	pitheme "psi/internal/theme"
	"psi/internal/tools"
)

type Options struct {
	Help, Version, Continue, Resume                                      bool
	SessionPath, SessionID, Provider, Model, Thinking, SessionDir, Tools string
	Name, ExcludeTools, Mode, TUIMode, SystemPrompt                      string
	AppendSystemPrompts                                                  []string
	ListModels                                                           bool
	ListModelsFilter                                                     string
	Docs                                                                 bool
	DocsTopic                                                            string
	NoTools, NoSession, AllowProj, DenyProj                              bool
	NoThemes, NoSkills, NoContextFiles, Offline                          bool
	UseTheme                                                             string
	ThemePaths                                                           []string
	Messages                                                             []string
}

var cliValueFlags = map[string]bool{
	"provider": true, "model": true, "thinking": true, "session": true, "session-id": true,
	"session-dir": true, "tools": true, "t": true, "theme": true,
	"use-theme": true, "name": true, "n": true, "exclude-tools": true,
	"xt": true, "mode": true, "tui-mode": true, "system-prompt": true,
	"append-system-prompt": true,
}

var cliBoolFlags = map[string]bool{
	"help": true, "h": true, "version": true, "v": true,
	"continue": true, "c": true, "resume": true, "r": true,
	"no-session": true, "no-tools": true, "nt": true,
	"no-builtin-tools": true, "nbt": true,
	"allow-project": true, "approve": true, "a": true,
	"deny-project": true, "no-approve": true, "na": true,
	"no-themes": true, "no-skills": true, "ns": true,
	"no-context-files": true, "nc": true, "offline": true,
}

// splitCLIArgs allows Pi-style interspersed options while keeping Go's flag
// parser for value validation. Positional arguments remain separate prompts.
func splitCLIArgs(args []string) (flags, messages []string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			messages = append(messages, args[i+1:]...)
			break
		}
		if arg == "--list-models" {
			value := ""
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+1], "@") {
				i++
				value = args[i]
			}
			flags = append(flags, "--list-models="+value)
			continue
		}
		if strings.HasPrefix(arg, "--list-models=") {
			flags = append(flags, arg)
			continue
		}
		if arg == "--docs" {
			value := ""
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+1], "@") {
				i++
				value = args[i]
			}
			flags = append(flags, "--docs="+value)
			continue
		}
		if strings.HasPrefix(arg, "--docs=") {
			flags = append(flags, arg)
			continue
		}
		if arg == "-" {
			return nil, nil, fmt.Errorf("Unknown option: -")
		}
		if !strings.HasPrefix(arg, "-") {
			messages = append(messages, arg)
			continue
		}
		name := strings.SplitN(arg, "=", 2)[0]
		name = strings.TrimLeft(name, "-")
		if name == "print" || name == "p" {
			return nil, nil, fmt.Errorf("Pi print mode is not supported; psi requires an interactive terminal")
		}
		isOption := strings.HasPrefix(arg, "-") && arg != "-"
		if isOption && (cliValueFlags[name] || cliBoolFlags[name]) {
			flags = append(flags, arg)
			if cliValueFlags[name] && !strings.Contains(arg, "=") && i+1 < len(args) {
				next := args[i+1]
				nextName := strings.TrimLeft(strings.SplitN(next, "=", 2)[0], "-")
				if next == "-" || !strings.HasPrefix(next, "-") || (!cliValueFlags[nextName] && !cliBoolFlags[nextName]) {
					flags = append(flags, next)
					i++
				}
			}
			continue
		}
		flags = append(flags, arg) // Let flag.Parse report unknown options.
	}
	return flags, messages, nil
}

func normalizeModelOption(o *Options) error {
	if o.Model == "" {
		if o.Provider != "" {
			for _, providerID := range providerregistry.IDs() {
				if strings.EqualFold(o.Provider, providerID) {
					o.Provider = providerID
					break
				}
			}
		}
		return nil
	}
	if i := strings.LastIndexByte(o.Model, ':'); i >= 0 {
		level := o.Model[i+1:]
		for _, valid := range config.ValidThinkingLevels {
			if level == valid {
				o.Model = o.Model[:i]
				if o.Thinking == "" {
					o.Thinking = level
				}
				break
			}
		}
	}
	if i := strings.IndexByte(o.Model, '/'); i > 0 {
		prefix := o.Model[:i]
		for _, providerID := range providerregistry.IDs() {
			if strings.EqualFold(prefix, providerID) {
				if o.Provider != "" && !strings.EqualFold(o.Provider, providerID) {
					return fmt.Errorf("--model provider %q conflicts with --provider %q", providerID, o.Provider)
				}
				o.Provider, o.Model = providerID, o.Model[i+1:]
				break
			}
		}
	}
	if strings.TrimSpace(o.Model) == "" {
		return fmt.Errorf("--model requires a model ID")
	}
	if o.Provider != "" {
		for _, providerID := range providerregistry.IDs() {
			if strings.EqualFold(o.Provider, providerID) {
				o.Provider = providerID
				break
			}
		}
	}
	return nil
}

func Parse(args []string) (Options, *flag.FlagSet, error) {
	var o Options
	flagArgs, messages, err := splitCLIArgs(args)
	if err != nil {
		return o, nil, err
	}
	fs := flag.NewFlagSet("psi", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.Help, "help", false, "show help")
	fs.BoolVar(&o.Help, "h", false, "show help (Pi alias)")
	fs.BoolVar(&o.Version, "version", false, "show version")
	fs.BoolVar(&o.Version, "v", false, "show version (Pi alias)")
	fs.BoolVar(&o.Continue, "continue", false, "continue most recent session")
	fs.BoolVar(&o.Continue, "c", false, "continue most recent session (Pi alias)")
	fs.BoolVar(&o.Resume, "resume", false, "resume with selection")
	fs.BoolVar(&o.Resume, "r", false, "resume with selection (Pi alias)")
	fs.StringVar(&o.SessionPath, "session", "", "open session path or ID")
	fs.StringVar(&o.SessionID, "session-id", "", "use this session ID, creating it if missing")
	fs.StringVar(&o.Provider, "provider", "", "provider")
	fs.StringVar(&o.Model, "model", "", "model ID or provider/model")
	fs.StringVar(&o.Thinking, "thinking", "", "thinking level")
	fs.StringVar(&o.SessionDir, "session-dir", "", "session directory")
	fs.StringVar(&o.Tools, "tools", "", "tool list (read,bash,edit,write)")
	fs.StringVar(&o.Tools, "t", "", "tool list (Pi alias)")
	fs.StringVar(&o.ExcludeTools, "exclude-tools", "", "comma-separated tools to disable")
	fs.StringVar(&o.ExcludeTools, "xt", "", "comma-separated tools to disable (Pi alias)")
	fs.BoolVar(&o.NoTools, "no-tools", false, "disable read,bash,edit,write")
	fs.BoolVar(&o.NoTools, "nt", false, "disable read,bash,edit,write (Pi alias)")
	fs.BoolVar(&o.NoTools, "no-builtin-tools", false, "disable read,bash,edit,write (alias for --no-tools)")
	fs.BoolVar(&o.NoTools, "nbt", false, "disable read,bash,edit,write (Pi alias)")
	fs.BoolVar(&o.NoSession, "no-session", false, "in-memory session")
	fs.BoolVar(&o.AllowProj, "allow-project", false, "allow protected project resources")
	fs.BoolVar(&o.AllowProj, "approve", false, "allow project resources (Pi alias)")
	fs.BoolVar(&o.AllowProj, "a", false, "allow project resources (Pi alias)")
	fs.BoolVar(&o.DenyProj, "deny-project", false, "deny protected project resources")
	fs.BoolVar(&o.DenyProj, "no-approve", false, "deny project resources (Pi alias)")
	fs.BoolVar(&o.DenyProj, "na", false, "deny project resources (Pi alias)")
	fs.BoolVar(&o.NoThemes, "no-themes", false, "disable configured and discovered themes")
	fs.BoolVar(&o.NoSkills, "no-skills", false, "disable skills")
	fs.BoolVar(&o.NoSkills, "ns", false, "disable skills (Pi alias)")
	fs.BoolVar(&o.NoContextFiles, "no-context-files", false, "disable AGENTS.md and CLAUDE.md")
	fs.BoolVar(&o.NoContextFiles, "nc", false, "disable context files (Pi alias)")
	fs.BoolVar(&o.Offline, "offline", false, "disable background catalog refresh (no-op in psi)")
	fs.StringVar(&o.Name, "name", "", "set session display name")
	fs.StringVar(&o.Name, "n", "", "set session display name (Pi alias)")
	fs.StringVar(&o.Mode, "mode", "text", "interactive text mode (Pi compatibility)")
	fs.StringVar(&o.TUIMode, "tui-mode", "regular", "regular inline terminal mode")
	fs.StringVar(&o.SystemPrompt, "system-prompt", "", "set the system prompt text or file path")
	fs.Func("append-system-prompt", "append system prompt text or file path (repeatable)", func(value string) error {
		o.AppendSystemPrompts = append(o.AppendSystemPrompts, value)
		return nil
	})
	fs.Func("list-models", "list models from the local Pi catalog (optional search text)", func(value string) error {
		o.ListModels, o.ListModelsFilter = true, value
		return nil
	})
	fs.Func("docs", "print psi documentation (no topic lists topics)", func(value string) error {
		o.Docs, o.DocsTopic = true, value
		return nil
	})
	fs.StringVar(&o.UseTheme, "use-theme", "", "select a Pi theme for this invocation")
	fs.Func("theme", "load an additional local theme file or directory (repeatable)", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("--theme path must not be empty")
		}
		o.ThemePaths = append(o.ThemePaths, value)
		return nil
	})
	if err := fs.Parse(flagArgs); err != nil {
		return o, fs, err
	}
	o.Messages = messages
	for _, message := range o.Messages {
		if strings.HasPrefix(message, "@") {
			return o, fs, fmt.Errorf("Pi @file arguments are not supported; pass file content in a message or use the read tool")
		}
	}
	if o.AllowProj && o.DenyProj {
		return o, fs, fmt.Errorf("--approve/--allow-project and --no-approve/--deny-project conflict")
	}
	count := 0
	for _, selected := range []bool{o.Continue, o.Resume, o.SessionPath != "", o.SessionID != ""} {
		if selected {
			count++
		}
	}
	if count > 1 {
		return o, fs, fmt.Errorf("choose only one of --continue, --resume, --session, and --session-id")
	}
	if o.SessionID != "" {
		if err := session.AssertValidSessionID(o.SessionID); err != nil {
			return o, fs, fmt.Errorf("--session-id: %v", err)
		}
	}
	if o.Name != "" {
		o.Name = strings.TrimSpace(o.Name)
		if o.Name == "" {
			return o, fs, fmt.Errorf("--name requires a non-empty value")
		}
	}
	if o.Mode != "text" && o.Mode != "rpc" {
		return o, fs, fmt.Errorf("psi supports only --mode text and --mode rpc; print and JSON modes are not supported")
	}
	if o.TUIMode != "regular" {
		return o, fs, fmt.Errorf("psi uses the regular inline terminal; --tui-mode fullscreen is not supported")
	}
	if err := normalizeModelOption(&o); err != nil {
		return o, fs, err
	}
	return o, fs, nil
}

func fuzzyMatch(text, query string) bool {
	textRunes, queryRunes := []rune(strings.ToLower(text)), []rune(strings.ToLower(query))
	if len(queryRunes) == 0 {
		return true
	}
	matched := 0
	for _, r := range textRunes {
		if r == queryRunes[matched] {
			matched++
			if matched == len(queryRunes) {
				return true
			}
		}
	}
	return false
}

func formatModelTokenCount(count int) string {
	if count >= 1_000_000 {
		millions := float64(count) / 1_000_000
		if millions == math.Trunc(millions) {
			return fmt.Sprintf("%.0fM", millions)
		}
		return fmt.Sprintf("%.1fM", millions)
	}
	if count >= 1_000 {
		thousands := float64(count) / 1_000
		if thousands == math.Trunc(thousands) {
			return fmt.Sprintf("%.0fK", thousands)
		}
		return fmt.Sprintf("%.1fK", thousands)
	}
	return strconv.Itoa(count)
}

// printDocs writes one docs topic, or the topic list when topic is empty or
// "list". It reads embedded bytes only: no terminal, settings, or network.
func printDocs(w io.Writer, topic string) error {
	if topic == "" || topic == "list" {
		_, err := io.WriteString(w, docs.List())
		return err
	}
	body, ok := docs.Get(topic)
	if !ok {
		return fmt.Errorf("unknown docs topic %q\n%s", topic, docs.List())
	}
	_, err := io.WriteString(w, body)
	return err
}

func listAvailableModels(output io.Writer, agentDir, filter string) error {
	models, err := config.LoadModels(filepath.Join(agentDir, "models.json"))
	if err != nil {
		return err
	}
	type row struct {
		provider, model, context, maxOutput, thinking, images string
	}
	var rows []row
	for _, providerID := range providerregistry.IDs() {
		for _, option := range models.ModelOptions(providerID) {
			if !option.Available || !fuzzyMatch(providerID+" "+option.Model.ID, filter) {
				continue
			}
			thinking, images := "no", "no"
			if option.Model.Reasoning {
				thinking = "yes"
			}
			if option.Model.InputImage {
				images = "yes"
			}
			rows = append(rows, row{providerID, option.Model.ID, formatModelTokenCount(option.Model.ContextWindow), formatModelTokenCount(option.Model.MaxTokens), thinking, images})
		}
	}
	if len(rows) == 0 {
		if filter != "" {
			_, err = fmt.Fprintf(output, "No models matching %q\n", filter)
			return err
		}
		_, err = fmt.Fprintln(output, "No models available in the local catalog.")
		return err
	}
	widths := []int{len("provider"), len("model"), len("context"), len("max-out"), len("thinking"), len("images")}
	for _, item := range rows {
		values := []string{item.provider, item.model, item.context, item.maxOutput, item.thinking, item.images}
		for i, value := range values {
			if len(value) > widths[i] {
				widths[i] = len(value)
			}
		}
	}
	format := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds\n", widths[0], widths[1], widths[2], widths[3], widths[4], widths[5])
	if _, err := fmt.Fprintf(output, format, "provider", "model", "context", "max-out", "thinking", "images"); err != nil {
		return err
	}
	for _, item := range rows {
		if _, err := fmt.Fprintf(output, format, item.provider, item.model, item.context, item.maxOutput, item.thinking, item.images); err != nil {
			return err
		}
	}
	return nil
}

func resolveCLIPrompt(input string) string {
	if input == "" {
		return ""
	}
	if data, err := os.ReadFile(input); err == nil {
		return strings.TrimPrefix(string(data), "\uFEFF")
	}
	return input
}

func helpText(version string) string {
	return fmt.Sprintf(`psi %s — minimal Go coding agent

Usage: psi [options] [messages...]

Pi-compatible options:
  --continue, -c             Continue the most recent session
  --resume, -r               Select a session to resume
  --session <path|id>        Open session file or ID prefix in session directory
  --session-id <id>          Use the current directory's existing session with the ID, creating it otherwise
  --session-dir <dir>        Session directory
  --provider <name>          Select provider; use configured model if omitted
  --model <id|provider/id>   Select model; :thinking suffix is supported
  --thinking <level>         Set thinking level
  --system-prompt <text|file> Set system prompt text or load it from a file
  --append-system-prompt <text|file> Append system prompt text or file (repeatable)
  --name, -n <name>          Set session display name
  --no-session               Keep this session in memory only
  --tools, -t <list>         Select read,bash,edit,write tools
  --exclude-tools, -xt <list> Disable selected tools
  --no-tools, -nt            Disable read,bash,edit,write
  --no-builtin-tools, -nbt   Alias for --no-tools
  --approve, -a              Allow project resources
  --no-approve, -na          Deny project resources
  --theme <path>             Load a theme (repeatable)
  --use-theme <name>         Select a theme
  --no-themes                Disable theme loading
  --no-skills, -ns           Disable skills
  --no-context-files, -nc    Disable AGENTS.md and CLAUDE.md
  --mode text|rpc            Select interactive text or headless JSONL RPC mode
  --tui-mode regular         Select the inline terminal mode
  --offline                  No-op; psi does not refresh Pi's model cache
  --list-models [search]     List cached and bundled models without network access
  --docs [topic]             Print psi documentation (no topic prints the list)
  --help, -h                 Show this help
  --version, -v              Show version

Positional messages are sent in order after the terminal starts. Options can appear before or after messages.
Use -- before a message that starts with a hyphen.
Tool-selection flags affect read,bash,edit,write only.
loop_control and syncboard remain available when their separate availability checks pass.
Not supported: --print/-p, JSON modes, fullscreen mode, @file, --api-key, --models, --fork, --export, extensions, MCP, and prompt templates.
`, version)
}

func Run(args []string, version string) error {
	o, _, err := Parse(args)
	if err != nil {
		return err
	}
	if o.Version {
		fmt.Printf("psi %s\n", version)
		return nil
	}
	if o.Help {
		fmt.Print(helpText(version))
		return nil
	}
	if o.ListModels {
		return listAvailableModels(os.Stdout, session.AgentDir(), o.ListModelsFilter)
	}
	if o.Docs {
		return printDocs(os.Stdout, o.DocsTopic)
	}
	if o.Mode == "rpc" {
		return runRPC(o, version)
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
			storedCwd := preparedStore.Cwd()
			info, statErr := os.Stat(storedCwd)
			if statErr != nil || !info.IsDir() {
				confirmed, confirmErr := confirmMissingSessionCwd(console, lifecycle, storedCwd, cwd)
				if confirmErr != nil {
					return confirmErr
				}
				if !confirmed {
					return nil
				}
				preparedStore, err = session.OpenFile(preparedStore.File(), cwd, !o.NoSession)
				if err != nil {
					return err
				}
			} else {
				cwd = storedCwd
			}
		}
	}
	projectPath := filepath.Join(cwd, ".pi", "settings.json")
	themeMode := pitheme.DetectColorMode(userSettings.Settings.TerminalTrueColor, envMap(), goruntime.GOOS)
	colorAppearance := pitheme.DetectTerminalAppearance(pitheme.TerminalColors{}, "", os.Getenv("COLORFGBG"))
	userCatalog := pitheme.LoadCatalog(themeCatalogOptions(userSettings.Settings, o, agentDir, cwd, false, themeMode))
	userTheme, _, _ := resolveThemeSnapshot(userCatalog, selectedThemeSetting(userSettings.Settings, o), colorAppearance, pitheme.TerminalColors{}, 0, themeMode)
	_, noColor := os.LookupEnv("NO_COLOR")
	console.SetPresentation(terminal.PresentationSnapshot{WorkingDirectory: cwd, HomeDirectory: os.Getenv("HOME"), Theme: userTheme, NoColor: noColor, Display: presentationDisplay(userSettings.Settings)})
	consent, err := resolveProjectTrust(o, cwd, agentDir, userSettings.Settings.DefaultProjectTrust, console, lifecycle)
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
	themeMode = pitheme.DetectColorMode(st.TerminalTrueColor, envMap(), goruntime.GOOS)
	themeCatalog := pitheme.LoadCatalog(themeCatalogOptions(st, o, agentDir, cwd, consent, themeMode))
	themeSetting := selectedThemeSetting(st, o)
	activeTheme, activeThemeName, themeFallback := resolveThemeSnapshot(themeCatalog, themeSetting, colorAppearance, pitheme.TerminalColors{}, 0, themeMode)
	console.SetPresentation(terminal.PresentationSnapshot{WorkingDirectory: cwd, HomeDirectory: os.Getenv("HOME"), Theme: activeTheme, NoColor: noColor, Display: presentationDisplay(st)})
	models, sk, diagnostics, prompt, sections, _, store, err := activateSessionWithStore(o, cwd, agentDir, &st, consent, console, lifecycle, preparedStore)
	if errors.Is(err, errStartupCancelled) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := store.TryLock(); err != nil {
		return err
	}
	r := &runtime{
		store: store, version: version, options: o, settings: st, models: models, skills: sk, prompt: prompt, sections: sections, agentDir: agentDir,
		themeCatalog: themeCatalog, theme: activeTheme, themeSetting: themeSetting, themeName: activeThemeName,
		themeMode: themeMode, themeColors: pitheme.TerminalColors{}, themeAppearance: colorAppearance,
		themeFallback: themeFallback, themeDiagnostics: themeCatalog.Diagnostics(), projectTrusted: consent,
		loops: loop.NewController(store),
		ac:    ac.NewController(),
	}
	defer func() {
		r.closeBridge()
		r.store.Unlock()
		if r.prov != nil {
			r.prov.CloseIdle()
		}
	}()
	if err := r.restore(); err != nil {
		return err
	}
	if err := r.loops.Reconstruct(); err != nil {
		return err
	}
	if o.Name != "" {
		if _, err := r.store.AppendSessionInfo(o.Name); err != nil {
			return fmt.Errorf("set session name: %w", err)
		}
	}
	r.tty, r.lifecycle, r.signals = tty, lifecycle, signals
	r.queryTerminalColors(console, 100*time.Millisecond)
	console.SetDeferDraw(true)
	startupOut := terminal.RawOutput{W: console}
	writeThemeDiagnostics(startupOut, r.themeDiagnostics, r.themeFallback)
	grouped := map[string]int{}
	for _, d := range diagnostics {
		grouped[d.Message]++
	}
	if !st.QuietStartup {
		for _, line := range terminal.PsiBanner(version, "esc interrupt · ctrl+c/ctrl+d clear/exit · / commands", console.StatusWidth(), r.theme, noColorEnvironment()) {
			fmt.Fprintln(startupOut, line)
		}
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
			for _, line := range terminal.FormatNotice(terminal.NoticeWarning, r.authErr.Error()+" (offline commands still work; /model to select, /session for status)", 80, noColorEnvironment(), r.theme) {
				fmt.Fprintln(startupOut, line)
			}
		}
	} else if r.authErr != nil {
		for _, line := range terminal.FormatNotice(terminal.NoticeWarning, r.authErr.Error(), 80, noColorEnvironment(), r.theme) {
			fmt.Fprintln(startupOut, line)
		}
	}
	r.refreshPresentation(console)
	return r.repl(console)
}

// newlineRun matches Pi's /name normalization: consecutive CR/LF runs collapse to one space.
var newlineRun = regexp.MustCompile(`[\r\n]+`)

type runtime struct {
	store                    *session.Store
	version                  string
	reg                      *tools.Registry
	options                  Options
	settings                 config.Settings
	models                   *config.ModelsFile
	skills                   []skills.Skill
	prompt, agentDir         string
	sections                 []config.PromptSection
	ov                       config.StartupOverrides
	cfg                      agent.Config
	prov                     provider.Provider
	authErr                  error
	generation               int64
	state                    State
	bridge                   *pibridge.Client
	bridgeTried              bool
	selector                 *Selector
	settingsPicker           *terminal.SettingsPicker
	selectorDraft            string
	selectorCursor           int
	selectorRestoreOnSuccess bool
	tty                      *terminal.Term
	lifecycle                *terminalLifecycle
	signals                  chan os.Signal
	footerKey                string
	footerSnapshot           terminal.PresentationSnapshot
	gitBranchDir             string // working directory the branch cache was built for
	gitHeadFile              string // HEAD file of the enclosing work tree
	gitHeadFound             bool   // whether a HEAD file was found
	gitBranchKey             string // cache key of the last read HEAD file
	gitBranchValue           string // branch name of the last read HEAD file
	contextTokens            float64
	contextWindow            float64
	// Bridge compact readiness, mirrored from loop-owned state.
	compactActive  bool
	compactPending bool
	// Native reload fence state. contextRev counts every user message and
	// compaction boundary; reloadPending latches one admitted reload;
	// reloadHigh is the highest reload id observed.
	contextRev    uint64
	reloadPending bool
	reloadHigh    uint64
	queuedInputs  func() int
	bridgeWork    func() bool
	// Bridge recovery holds verified structured input that was admitted but
	// never became durable. It is never replayed automatically; inFlight is
	// the one admitted prompt waiting for RunStart to prove its append.
	recovery            []agent.UserInput
	inFlight            *agent.UserInput
	recoveryNotify      func(string)
	themeCatalog        *pitheme.Catalog
	theme               *pitheme.Theme
	themeSetting        string
	themeName           string
	themeMode           pitheme.ColorMode
	themeColors         pitheme.TerminalColors
	themeColorsReceived bool
	themeColorCollector *terminalColorCollector
	themeAppearance     string
	themeFallback       string
	themeDiagnostics    []pitheme.Diagnostic
	projectTrusted      bool
	loops               *loop.Controller
	ac                  *ac.Controller
	cancelACEndTurn     func()
	reportedColorScheme string
	// activeTools holds the running calls of the current batch in start
	// order. Parallel batches keep several; the live row shows the newest.
	activeTools []activeTool
	timerFrames int
	eventFrames int
}

// activeTool is one running tool call tracked by call ID.
type activeTool struct {
	name    string
	callID  string
	summary event.ToolSummary
}

// liveToolRow renders the newest active call's running row. A parallel
// batch shows the latest start; settled rows still print in call order.
func (r *runtime) liveToolRow(console *terminal.Console) (string, bool) {
	if len(r.activeTools) == 0 {
		return "", false
	}
	active := r.activeTools[len(r.activeTools)-1]
	return terminal.FormatToolRowWithDisplay(active.summary, active.name, "running", console.StatusWidth(), noColorEnvironment(), presentationDisplay(r.settings), r.theme), true
}

func (r *runtime) restore() error {
	state, err := r.store.ModelState()
	if err != nil {
		return err
	}
	thinking := ""
	if r.store.EntryCount() > 0 {
		thinking = state.ThinkingLevel
	}
	ov := config.ResolveStartup(r.options.Provider, r.options.Model, r.options.Thinking, r.options.SessionDir, r.options.NoSession,
		state.Provider, state.ModelID, thinking, r.settings, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
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

func providerModel(providerID string, meta config.ModelMeta) provider.ModelMeta {
	forceAdaptive, _, _ := meta.Compat.Bool("forceAdaptiveThinking")
	midConvoEffort, _, _ := meta.Compat.Bool("supportsMidConvoEffort")
	allowEmptySignature, _, _ := meta.Compat.Bool("allowEmptySignature")
	return provider.ModelMeta{ID: meta.ID, API: meta.API, Provider: providerID, BaseURL: meta.BaseURL, MaxTokens: meta.MaxTokens, ContextWindow: meta.ContextWindow, Reasoning: meta.Reasoning, InputImage: meta.InputImage, ThinkingLevelMap: meta.ThinkingLevelMap, AdaptiveThinking: forceAdaptive, MidConvoEffort: midConvoEffort, AllowEmptySignature: allowEmptySignature, Compat: meta.Compat}
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
	pm := providerModel(ov.Provider, meta)
	cfg := agent.Config{Model: pm, Thinking: ov.Thinking, Tools: tools.Definitions(r.settings.DefaultTools), System: r.prompt, Sections: r.sections,
		AutoCompact: r.settings.Compaction.Enabled, ReserveTokens: r.settings.Compaction.ReserveForModel(ov.Provider, ov.Model), KeepRecentTokens: r.settings.Compaction.KeepRecentTokens}
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
		key, _, err := provider.SelectProviderAPIKey(envMap(), authPath, ov.Provider, meta.APIKey)
		authErr = err
		anthropic := provider.NewClient(meta.BaseURL, key, r.settings.MaxRetries, idle)
		anthropic.Headers = headers
		anthropic.SessionID = r.store.Header().ID
		client = anthropic
	case "openai-completions":
		key, _, err := provider.SelectProviderAPIKey(envMap(), authPath, ov.Provider, meta.APIKey)
		authErr = err
		completions := provider.NewCompletionsClient(meta.BaseURL, key, r.settings.MaxRetries, idle)
		completions.Headers = headers
		completions.SessionID = r.store.Header().ID
		client = completions
	case "google-generative-ai":
		key, _, err := provider.SelectProviderAPIKey(envMap(), authPath, ov.Provider, meta.APIKey)
		authErr = err
		google := provider.NewGoogleClient(meta.BaseURL, key, r.settings.MaxRetries, idle)
		google.Headers = headers
		google.SessionID = r.store.Header().ID
		client = google
	case "openai-responses":
		key, _, err := provider.SelectProviderAPIKey(envMap(), authPath, ov.Provider, meta.APIKey)
		authErr = err
		responses := provider.NewResponsesClient(meta.BaseURL, key, r.settings.MaxRetries, idle)
		responses.Headers = headers
		responses.SessionID = r.store.Header().ID
		client = responses
	case "mistral-conversations":
		key, _, err := provider.SelectProviderAPIKey(envMap(), authPath, ov.Provider, meta.APIKey)
		authErr = err
		mistral := provider.NewMistralClient(meta.BaseURL, key, r.settings.MaxRetries, idle)
		mistral.Headers = headers
		mistral.SessionID = r.store.Header().ID
		client = mistral
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

func (r *runtime) compactionProvider(reference string) (provider.Provider, provider.ModelMeta, func(), error) {
	if r.authErr != nil {
		// Pi resolves the summarization request only when it summarizes: the
		// preparation runs first, so a session with nothing to compact never
		// needs credentials.
		return nil, provider.ModelMeta{}, nil, r.authErr
	}
	if reference == "" {
		return nil, provider.ModelMeta{}, nil, nil
	}
	parts := strings.SplitN(reference, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, provider.ModelMeta{}, nil, fmt.Errorf("invalid compactor model %q; use provider/model", reference)
	}
	if parts[0] == r.ov.Provider && parts[1] == r.ov.Model {
		return nil, provider.ModelMeta{}, nil, nil
	}
	if r.models == nil {
		return nil, provider.ModelMeta{}, nil, fmt.Errorf("model catalog is unavailable")
	}
	next := r.ov
	next.Provider, next.Model, next.Thinking = parts[0], parts[1], "off"
	candidate := *r
	candidate.prov = nil
	if err := candidate.configure(next); err != nil {
		return nil, provider.ModelMeta{}, nil, fmt.Errorf("compactor model %s: %w", reference, err)
	}
	if candidate.authErr != nil {
		candidate.prov.CloseIdle()
		return nil, provider.ModelMeta{}, nil, fmt.Errorf("compactor model %s: %w", reference, candidate.authErr)
	}
	client, model := candidate.prov, candidate.cfg.Model
	return client, model, client.CloseIdle, nil
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

func confirmMissingSessionCwd(console *terminal.Console, lifecycle *terminalLifecycle, sessionCwd, fallbackCwd string) (bool, error) {
	picker := &terminal.ChoicePicker{
		Title:   "Session cwd not found",
		Context: fmt.Sprintf("cwd from session file does not exist\n%s\n\ncontinue in current cwd\n%s", sessionCwd, fallbackCwd),
		Options: []terminal.ChoiceOption{
			{Label: "Yes"},
			{Label: "No", Current: true},
		},
		Selected: 1,
	}
	selected, cancelled, err := runChoicePicker(console, lifecycle, picker)
	return !cancelled && selected == 0, err
}

// findSessionID resolves an exact session ID to its file: the canonical
// path first, then one legacy discovery pass. It returns an empty path
// when no existing session matches.
func findSessionID(dir, cwd, id string, discover func(string, string) ([]session.SessionInfo, error)) (string, error) {
	hit, err := session.FindExact(dir, cwd, id)
	if err != nil {
		return "", err
	}
	if hit != "" {
		return hit, nil
	}
	infos, err := discover(dir, cwd)
	if err != nil {
		return "", err
	}
	for _, info := range infos {
		if info.ID == id {
			return info.Path, nil
		}
	}
	return "", nil
}

func openSession(o Options, cwd, agentDir string, st config.Settings, ov config.StartupOverrides, console *terminal.Console, lifecycle *terminalLifecycle) (*session.Store, error) {
	dir := session.SessionDirOverride(ov.SessionDir, "", "", cwd, agentDir)
	persist := !ov.NoSession
	// Pi --session-id semantics: use the current directory's existing
	// session with the ID, creating it otherwise.
	if o.SessionID != "" {
		path, err := findSessionID(dir, cwd, o.SessionID, workspaceSessions)
		if err != nil {
			return nil, err
		}
		if path != "" {
			store, err := session.OpenFile(path, cwd, persist)
			if err != nil {
				return nil, err
			}
			// The candidate may have been replaced between the header
			// probe and the full open. Recheck the opened identity.
			if store.Header().ID != o.SessionID || !session.SameDir(store.Header().Cwd, cwd) {
				return nil, fmt.Errorf("session file %s no longer matches ID %q", path, o.SessionID)
			}
			return store, nil
		}
		return session.New(session.OpenOptions{Cwd: cwd, SessionDir: dir, AgentDir: agentDir, Persist: persist, ID: o.SessionID})
	}
	if o.SessionPath != "" {
		path := o.SessionPath
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if strings.ContainsAny(path, `/\\`) || strings.HasSuffix(path, ".jsonl") {
				return nil, err
			}
			infos, discoverErr := session.Discover(dir)
			if discoverErr != nil {
				return nil, discoverErr
			}
			var matches []session.SessionInfo
			for _, info := range infos {
				if strings.HasPrefix(info.ID, path) {
					matches = append(matches, info)
				}
			}
			switch len(matches) {
			case 0:
				return nil, fmt.Errorf("session %q was not found in %s", path, dir)
			case 1:
				path = matches[0].Path
			default:
				return nil, fmt.Errorf("session ID %q is ambiguous; use a longer prefix", path)
			}
		} else if err != nil {
			return nil, err
		}
		return session.OpenFile(path, cwd, persist)
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
		if session.SameDir(info.Cwd, cwd) {
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
	if err == nil {
		return false
	}
	message := err.Error()
	return containsStr(message, "unsupported") ||
		containsStr(message, "unavailable:") ||
		containsStr(message, "does not support thinking")
}

func containsNoModel(err error) bool {
	return err != nil && (containsStr(err.Error(), "needs explicit metadata") || containsStr(err.Error(), "needs an explicit model selection"))
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

// footerThinkingLabel follows Pi's footer. The thinking level appears only for
// a model that supports reasoning, and the disabled state is spelled out so it
// cannot be mistaken for a model name.
func footerProviderCount(models *config.ModelsFile) int {
	if models == nil {
		return 0
	}
	count := 0
	for _, family := range providerregistry.IDs() {
		available := false
		for _, candidate := range config.BundledModels(family) {
			if _, err := models.SelectModel(family, candidate.ID); err == nil {
				available = true
				break
			}
		}
		if !available {
			for _, candidate := range models.Providers[family].Models {
				if _, err := models.SelectModel(family, candidate.ID); err == nil {
					available = true
					break
				}
			}
		}
		if available {
			count++
		}
	}
	return count
}

func footerThinkingLabel(model provider.ModelMeta, level string) string {
	if !model.Reasoning {
		return ""
	}
	if level == "" || level == "off" {
		return "thinking off"
	}
	return level
}

func (r *runtime) refreshPresentation(console *terminal.Console) {
	if console == nil || r.store == nil {
		return
	}
	projection := r.store.ProjectionIfCached()
	branch := r.gitBranchFor(r.store.Cwd())
	key := fmt.Sprintf("%s/%s/%d/%s/%s/%s/%s/%t/%d/%d/%d/%q/%t/%t/%q/%t", r.store.Header().ID, r.store.LeafID(), r.store.EntryCount(), r.ov.Provider, r.ov.Model, r.ov.Thinking, r.store.Cwd(), r.cfg.AutoCompact, r.settings.OutputPad, r.settings.EditorPaddingX, r.settings.AutocompleteMaxVisible, r.settings.MarkdownCodeBlockIndent, projection != nil, r.settings.HideThinkingBlock, branch, r.settings.SoftWrap)
	if key != r.footerKey {
		home := os.Getenv("HOME")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		sessionName := r.store.SessionName()
		snapshot := terminal.PresentationSnapshot{
			Display:          presentationDisplay(r.settings),
			WorkingDirectory: r.store.Cwd(), HomeDirectory: home, SessionName: sessionName, GitBranch: branch,
			Provider: r.ov.Provider, ProviderCount: footerProviderCount(r.models), Model: r.ov.Model,
			Thinking: footerThinkingLabel(r.cfg.Model, r.ov.Thinking), AutoCompact: r.cfg.AutoCompact, Theme: r.theme,
			NoColor: noColorEnvironment(),
		}
		if accounting, err := r.store.Accounting(); err == nil {
			parts := []string{}
			if accounting.Usage.Input > 0 {
				parts = append(parts, "↑"+formatFooterTokens(accounting.Usage.Input))
			}
			if accounting.Usage.Output > 0 {
				parts = append(parts, "↓"+formatFooterTokens(accounting.Usage.Output))
			}
			if accounting.Usage.CacheRead > 0 {
				parts = append(parts, "R"+formatFooterTokens(accounting.Usage.CacheRead))
			}
			if accounting.Usage.CacheWrite > 0 {
				parts = append(parts, "W"+formatFooterTokens(accounting.Usage.CacheWrite))
			}
			if (accounting.Usage.CacheRead > 0 || accounting.Usage.CacheWrite > 0) && accounting.CacheHitRate != nil {
				parts = append(parts, "CH"+formatFooterFixedOneDecimal(*accounting.CacheHitRate, "%"))
			}
			snapshot.Usage = strings.Join(parts, " ")
		}
		// Bridge telemetry is sampled here, next to the footer estimate, so
		// both report the same point in time. Neither forces a projection.
		r.contextTokens, r.contextWindow = 0, 0
		if r.cfg.Model.ContextWindow > 0 {
			estimated, hasEstimate := 0, false
			// Pi's footer anchors on the newest provider-reported usage and
			// estimates only the messages after it. A cached projection alone
			// must not switch the source to a character estimate, or the value
			// jumps between the two whenever the cache is built or dropped.
			usage, usageID, hasUsage := r.store.LastContextUsage()
			if hasUsage || projection != nil {
				estimated, hasEstimate = agent.FooterContext(projection, r.cfg, usage, usageID, hasUsage), true
			} else if r.store.EntryCount() == 0 {
				// Fresh session: retain the system/tool baseline without decoding.
				estimated, hasEstimate = agent.EstimatedContext(&session.Projection{}, r.cfg), true
			}
			if hasEstimate {
				r.contextTokens = float64(estimated)
				r.contextWindow = float64(r.cfg.Model.ContextWindow)
				percent := 100 * float64(estimated) / float64(r.cfg.Model.ContextWindow)
				percentLabel := formatFooterFixedOneDecimal(percent, "%")
				// Keep the bar's fill and color thresholds on the same rounded value
				// that the footer prints.
				displayPercent := percent
				if rounded, err := strconv.ParseFloat(strings.TrimSuffix(percentLabel, "%"), 64); err == nil {
					displayPercent = rounded
				}
				windowLabel := formatFooterTokens(float64(r.cfg.Model.ContextWindow))
				snapshot.ContextPercent = displayPercent
				snapshot.ContextPercentLabel = percentLabel
				snapshot.ContextWindowLabel = windowLabel
				snapshot.ContextEstimate = "~" + percentLabel + "/" + windowLabel
			}
		}
		r.footerKey, r.footerSnapshot = key, snapshot
	}
	r.footerSnapshot.Theme = r.theme
	console.SetPresentation(r.footerSnapshot)
	r.syncBridge()
}

func formatFooterTokens(n float64) string {
	if n < 1000 {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	if n < 10000 {
		return formatFooterFixedOneDecimal(n/1000, "k")
	}
	if n < 1000000 {
		return fmt.Sprintf("%dk", int64(math.Floor(n/1000+0.5)))
	}
	if n < 10000000 {
		return formatFooterFixedOneDecimal(n/1000000, "M")
	}
	return fmt.Sprintf("%dM", int64(math.Floor(n/1000000+0.5)))
}

// Pi's number.toFixed rounds exact halfway values away from zero. fmt's
// decimal formatter rounds ties to even, so use the float's exact rational
// value for the two one-decimal footer ranges.
func formatFooterFixedOneDecimal(value float64, suffix string) string {
	sign := ""
	if value < 0 {
		sign, value = "-", -value
	}
	scaled := new(big.Rat).SetFloat64(value)
	if scaled == nil {
		return fmt.Sprintf("%.1f%s", value, suffix)
	}
	scaled.Mul(scaled, big.NewRat(10, 1))
	quotient, remainder := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	if new(big.Int).Lsh(remainder, 1).Cmp(scaled.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(quotient, big.NewInt(10), fraction)
	return sign + whole.String() + "." + fraction.String() + suffix
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
	if r.loops != nil {
		if err := r.loops.SetStore(next); err != nil {
			_ = r.loops.SetStore(old)
			next.Unlock()
			return err
		}
	}
	r.installConfiguration(&candidate)
	r.store = next
	// Undelivered prompts belonged to the replaced session; keeping them
	// would let a later attachment send content the user has moved away from.
	r.clearRecovery()
	r.clearInFlight()
	if r.cancelACEndTurn != nil {
		r.cancelACEndTurn()
	}
	if r.ac != nil {
		r.ac.ResetSession()
	}
	if r.lifecycle != nil && r.lifecycle.console != nil && r.loops != nil {
		r.lifecycle.console.SetLoopStatus(r.loops.StatusLine())
	}
	old.Unlock()
	return nil
}

// cloneSession duplicates the durable session file into a new session with a
// new ID, preserving every entry on the active branch byte-for-byte (Pi's
// /clone fork-at-leaf), then switches the runtime to the copy.
func (r *runtime) cloneSession(print func(string)) error {
	data, err := os.ReadFile(r.store.File())
	if err != nil {
		return err
	}
	// First line is the session header; the clone gets a fresh one.
	headerEnd := bytes.IndexByte(data, '\n')
	if headerEnd < 0 {
		return fmt.Errorf("session file has no header to replace")
	}
	next, err := session.New(session.OpenOptions{Cwd: r.store.Cwd(), SessionDir: r.store.Dir(), AgentDir: r.agentDir, Persist: r.store.Persistent()})
	if err != nil {
		return err
	}
	path, err := r.store.ActivePath()
	if err != nil {
		return err
	}
	if len(path) == 0 {
		return fmt.Errorf("nothing to clone yet")
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(data[:headerEnd], &header); err != nil {
		return fmt.Errorf("read session header: %w", err)
	}
	newID, _ := json.Marshal(next.Header().ID)
	newTimestamp, _ := json.Marshal(next.Header().Timestamp)
	header["id"], header["timestamp"] = newID, newTimestamp
	hdr, err := json.Marshal(header)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.Write(hdr)
	buf.WriteByte('\n')
	for _, entry := range path {
		if len(entry.Raw) == 0 {
			return fmt.Errorf("session entry %q has no raw record", entry.ID())
		}
		buf.Write(entry.Raw)
		buf.WriteByte('\n')
	}
	f, err := os.OpenFile(next.File(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		os.Remove(next.File())
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(next.File())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(next.File())
		return err
	}
	opened, err := session.OpenFile(next.File(), r.store.Cwd(), r.store.Persistent())
	if err != nil {
		os.Remove(next.File())
		return err
	}
	if err := r.replace(opened); err != nil {
		os.Remove(next.File())
		return err
	}
	r.generation++
	print("Cloned to new session")
	return nil
}

func (r *runtime) draw(console *terminal.Console) {
	r.refreshPresentation(console)
	if row, ok := r.liveToolRow(console); ok {
		console.SetToolRowNoDraw(row)
	}
	if r.settingsPicker != nil {
		width, rows := console.PickerSize()
		console.SetPickerRows(r.settingsPicker.Rows(width, rows, r.footerSnapshot.NoColor, r.footerSnapshot.Theme))
	} else if r.selector != nil {
		width, rows := console.PickerSize()
		console.SetPickerRows(r.selector.RenderPickerRows(width, rows, r.footerSnapshot.NoColor, r.footerSnapshot.Theme))
	} else {
		console.ClearPickerRows()
	}
	console.Draw()
}

// newLoop builds the agent loop with the configured queue modes, so every
// caller delivers queued input the way settings.json asks.
func (r *runtime) newLoop(sink func(event.Event)) *agent.Loop {
	loop := agent.New(r.store, r.reg, r.prov, sink)
	loop.SetQueueModes(r.settings.SteeringMode, r.settings.FollowUpMode)
	return loop
}

func (r *runtime) repl(console *terminal.Console) error {
	// Some embedded callers construct runtime directly instead of going through
	// Run. Keep the loop feature safe for those callers as well.
	if r.ac == nil {
		r.ac = ac.NewController()
	}
	if r.loops == nil && r.store != nil {
		r.loops = loop.NewController(r.store)
		if err := r.loops.Reconstruct(); err != nil {
			return err
		}
	}
	out := terminal.RawOutput{W: console}
	print := func(s string) { fmt.Fprintln(out, terminal.Sanitize(s)) }
	notice := func(kind terminal.NoticeKind, text string) {
		_ = out.AppendDocumentBlock(terminal.DocumentBlock{Kind: terminal.DocumentNotice, Text: text, NoticeKind: kind})
	}
	var themeChanges <-chan struct{}
	var watch *themeWatch
	var lastThemeWatchError string
	if created, err := newThemeWatch(r.agentDir, r.selectedUserThemePath()); err == nil {
		watch = created
		themeChanges = watch.changes
		defer watch.Close()
	} else {
		notice(terminal.NoticeWarning, "Theme file watching is unavailable; use /reload to apply theme changes.")
	}
	stream := terminal.NewStreamer(terminal.StreamOutput{Console: console}, true)
	thinkingStream := terminal.NewStreamer(terminal.ThinkingOutput{Console: console}, true)
	status := terminal.NewStatusWriter(out)
	events := make(chan event.Event, 64)
	done := make(chan error, 1)
	// Capability requests are served on this loop so mutation stays
	// single-owner; the bridge client only carries them.
	bridge := newBridgeHandler(r, console)
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
	var activeLoop *agent.Loop
	var pendingACCompactFocus *string
	var outputErr error
	var runOutcome string
	var visibleRunText bool
	var toolSpacing toolBatchSpacing
	finishTools := func() {
		if toolSpacing.pending {
			toolSpacing.pending = false
			_ = console.FinishTranscriptGroup()
		}
	}
	var submittedDraft string
	var submittedCursor int
	var suspendPending bool
	inputs := console.Inputs
	pendingInputs := console.TakeHeldInputs()
	var pendingInputBytes int
	for _, input := range pendingInputs {
		pendingInputBytes += len(input.Key) + len(input.Reply)
	}
	initialMessages := append([]string(nil), r.options.Messages...)
	// Readiness must see queued input the moment it appears, not the next
	// draw: Phi decides from the published snapshot alone.
	r.queuedInputs = func() int {
		queued := len(initialMessages)
		if activeLoop != nil {
			queued += len(activeLoop.PendingSteering()) + len(activeLoop.PendingFollowUp())
		}
		return queued
	}
	// Bridge readiness must reflect preparation work that can still install a
	// configuration, not only runs the loop owns.
	r.bridgeWork = bridge.busy
	r.recoveryNotify = func(text string) { notice(terminal.NoticeWarning, text) }
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
		console.SetThinking(false)
		cancel()
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
	openSettings := func() {
		r.settingsPicker = r.newSettingsPicker()
		r.state = StateSelecting
		r.draw(console)
	}
	closeSettings := func() {
		r.settingsPicker = nil
		console.ClearPickerRows()
		r.state = StateIdle
		if r.authErr != nil {
			r.state = StateOffline
		}
		r.draw(console)
	}
	openTrust := func() {
		cwd := r.store.Cwd()
		trustStore := newProjectTrustStore(r.agentDir)
		savedDecision, savedPath, hasSavedDecision := trustStore.getEntry(cwd)
		trustPath := canonicalTrustPath(cwd)
		savedText := "none"
		if hasSavedDecision {
			label := "untrusted"
			if savedDecision {
				label = "trusted"
			}
			if trustPathsEqual(savedPath, trustPath) {
				savedText = label + " (" + savedPath + ")"
			} else {
				savedText = label + " (inherited from " + savedPath + ")"
			}
		}
		currentText := "untrusted"
		if r.projectTrusted {
			currentText = "trusted"
		}
		trustOptions := projectTrustOptions(cwd)
		choices := make([]terminal.ChoiceOption, len(trustOptions))
		selected := 0
		for i, option := range trustOptions {
			current := hasSavedDecision && option.savedPath == savedPath && option.trusted == savedDecision
			if current {
				selected = i
			}
			choices[i] = terminal.ChoiceOption{Label: option.label, Current: current}
		}
		picker := &terminal.ChoicePicker{
			Title:   "Project trust",
			Context: fmt.Sprintf("%s\nSaved decision: %s\nCurrent session: %s", cwd, savedText, currentText),
			Options: choices, Selected: selected,
		}
		index, cancelled, err := runChoicePicker(console, r.lifecycle, picker)
		if err != nil {
			notice(terminal.NoticeError, err.Error())
			r.draw(console)
			return
		}
		if !cancelled && index >= 0 && index < len(trustOptions) {
			option := trustOptions[index]
			if err := trustStore.setMany(option.updates); err != nil {
				notice(terminal.NoticeError, err.Error())
			} else {
				decision := "untrusted"
				if option.trusted {
					decision = "trusted"
				}
				printStatusText(out, "Saved trust decision: "+decision+". Restart psi for this to take effect.")
			}
		}
		r.draw(console)
	}
	cycleThinking := func() {
		levels := provider.SupportedThinking(r.cfg.Model)
		if len(levels) <= 1 {
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
		r.draw(console)
	}
	console.Commands = terminal.CommandNames()
	console.CompletionDescriptions = terminal.CommandDescriptions()
	for command, description := range map[string]string{
		"help": "show AC help and session status", "status": "show context and compaction settings",
		"compact": "compact now", "threshold": "set the adaptive target",
		"end-turn": "set the completed-run threshold", "end-turn-delay": "set the delay in minutes",
		"compactor": "select or clear the summary model", "enable": "enable proactive compaction",
		"disable": "disable proactive compaction",
	} {
		console.CompletionDescriptions[command] = description
	}
	for _, skill := range r.skills {
		console.Skills = append(console.Skills, skill.Name)
		console.CompletionDescriptions["/skill:"+skill.Name] = skill.Description
	}
	emitReplayBlock := func(block terminal.DocumentBlock) bool {
		for checked := 0; checked < 64; checked++ {
			select {
			case input, ok := <-inputs:
				if !ok || input.Err != nil {
					exiting = true
					return false
				}
				if len(input.Reply) > 0 {
					r.handleTerminalReply(console, input.Reply)
					continue
				}
				if input.Resize {
					console.RequestFullRedraw()
					continue
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
				if isResize(sig) {
					console.RequestFullRedraw()
					continue
				}
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
				if err := console.AppendTranscriptBlock(block); err != nil {
					return false
				}
				return true
			}
		}
		return console.AppendTranscriptBlock(block) == nil
	}
	r.generation++
	r.syncBridge()
	r.state = StateReplaying
	// Startup activation: display current view before the next editable prompt (P2-035).
	// Same projector + replay engine as /resume (P2-036).
	if act, err := PrepareActivation(r.store); err == nil {
		replayActivationDocument(emitReplayBlock, act, true, "")
		r.renderProjectTrustWarningIfNeeded(console)
		if count := r.store.CompactionCount(); count > 0 {
			times := "1 time"
			if count != 1 {
				times = fmt.Sprintf("%d times", count)
			}
			printStatusText(out, "Session compacted "+times)
		}
		if act.Offline {
			r.state = StateOffline
		} else {
			r.state = StateIdle
		}
	} else {
		print("error: " + err.Error())
		r.state = StateIdle
	}
	recordEventFrames := func(update func()) {
		_, before, _, _ := console.FrameStats()
		update()
		_, after, _, _ := console.FrameStats()
		r.eventFrames += after - before
	}
	applyEvent := func(ev event.Event) {
		if ev.Generation != 0 && ev.Generation != r.generation {
			return
		}
		// A run can span many tool calls and a mid-run compaction. Each of
		// these events follows a session append, so rebuild the footer before
		// the event's own redraw; otherwise the context estimate stays frozen
		// at its value from the last idle redraw. Deltas append nothing.
		if ev.Type != event.TextDelta && ev.Type != event.ThinkingDelta {
			r.refreshPresentation(console)
		}
		switch ev.Type {
		case event.AssistantStart:
			if r.state != StateCancelling {
				console.SetThinking(true)
			}
		case event.ThinkingDelta:
			// Reasoning is visible output: it ends the tool group and replaces
			// the transient activity row.
			if strings.TrimSpace(ev.Text) != "" {
				finishTools()
				console.SetThinking(false)
				r.state = StateStreaming
				visibleRunText = true
			}
			// Keep arrival order: text that arrived first renders first.
			if stream.Pending() {
				stream.Flush()
			}
			thinkingStream.Delta(ev.Text)
		case event.TextDelta:
			// Tool-only responses are one compact chain, even across requests.
			// A blank delta is not a visible assistant block.
			if strings.TrimSpace(ev.Text) != "" {
				finishTools()
				console.SetThinking(false)
				r.state = StateStreaming
				visibleRunText = true
			}
			// Keep arrival order: reasoning that arrived first renders first.
			if thinkingStream.Pending() {
				thinkingStream.Flush()
			}
			stream.Delta(ev.Text)
		case event.RunStart:
			runOutcome, visibleRunText = "", false
			r.clearInFlight()
			r.bridgeRunStarted()
		case event.SteeringDelivered, event.FollowUpDelivered:
			// A delivered queued message becomes a normal user turn. Remove it
			// from the appropriate dim queue and append its durable transcript row.
			if activeLoop != nil {
				console.SetSteeringRows(activeLoop.PendingSteering())
				console.SetFollowUpRows(activeLoop.PendingFollowUp())
			}
			console.EnsureTranscriptGap()
			_ = out.AppendDocumentBlock(terminal.DocumentBlock{Kind: terminal.DocumentUser, Text: ev.Text})
		case event.RunEnd:
			console.SetThinking(false)
			runOutcome = ev.Outcome
			r.bridgeRunSettled(runOutcomeOf(ev))
		case event.Retry:
			if r.state != StateCancelling {
				r.state = StateWaiting
				setActivity(formatRetryActivity(ev.RetryAttempt, ev.RetryMaximum, time.Duration(ev.RetryDelayMs)*time.Millisecond))
			}
		case event.ToolStart:
			console.SetThinking(false)
			thinkingStream.Close()
			stream.Close()
			r.state = StateRunningTool
			r.activeTools = append(r.activeTools, activeTool{name: ev.ToolName, callID: ev.ToolCallID, summary: ev.Summary})
			display := presentationDisplay(r.settings)
			console.SetToolRow(terminal.FormatToolRowWithDisplay(ev.Summary, ev.ToolName, "running", console.StatusWidth(), noColorEnvironment(), display, r.theme))
			setActivity("Working")
			status.UpdateSummary(ev.ToolName, ev.ToolCallID, ev.Summary, "running", display, r.theme, noColorEnvironment())
		case event.ToolEnd:
			for i, active := range r.activeTools {
				if active.callID == ev.ToolCallID {
					r.activeTools = append(r.activeTools[:i], r.activeTools[i+1:]...)
					break
				}
			}
			if len(r.activeTools) > 0 {
				// Sibling calls still run: keep the live row on the newest one
				// instead of clearing it mid-batch.
				if row, ok := r.liveToolRow(console); ok {
					console.SetToolRow(row)
				}
			} else {
				console.ClearToolRow()
			}
			status.UpdateSummary(ev.ToolName, ev.ToolCallID, ev.Summary, ev.State, presentationDisplay(r.settings), r.theme, noColorEnvironment())
			if ev.ToolName == loop.ControlToolName && r.loops != nil {
				console.SetLoopStatus(r.loops.StatusLine())
			}
			toolSpacing.pending = true
			if r.state != StateCancelling {
				r.state = StateWaiting
				setActivity("Working")
			}
		case event.CompactionStart:
			console.SetThinking(false)
			thinkingStream.Close()
			stream.Close()
			r.state = StateCompacting
			r.compactActive = true
			setActivity(compactionActivity(ev.State))
			r.syncBridge()
		case event.CompactionEnd:
			r.compactActive = false
			if r.state != StateCancelling {
				r.state = StateWaiting
				setActivity("Working")
			}
			r.syncBridge()
		}
	}
	acEndTurnTimer := time.NewTimer(time.Hour)
	if !acEndTurnTimer.Stop() {
		<-acEndTurnTimer.C
	}
	var acEndTurnTimeout <-chan time.Time
	cancelACEndTurn := func() {
		if !acEndTurnTimer.Stop() {
			select {
			case <-acEndTurnTimer.C:
			default:
			}
		}
		acEndTurnTimeout = nil
	}
	r.cancelACEndTurn = cancelACEndTurn
	defer func() {
		cancelACEndTurn()
		r.cancelACEndTurn = nil
	}()
	var compactionCountAtStart uint64
	var resumeAfterCompaction bool
	var activeConfig agent.Config
	startInput := func(input agent.UserInput, focus string, compact bool) (bool, string) {
		resumeThisCompaction := compact && resumeAfterCompaction
		if compact {
			resumeAfterCompaction = false
		}
		cancelACEndTurn()
		if err := r.store.WriteError(); err != nil {
			r.state = StateStorageBlocked
			notice(terminal.NoticeError, "Storage is blocked: "+err.Error()+" (open a checked copy or /new)")
			return false, "storage"
		}
		if r.authErr != nil && !compact {
			// A compaction resolves its provider after the preparation, like
			// Pi: the session is checked before credentials are needed.
			notice(terminal.NoticeError, r.authErr.Error())
			return false, "unavailable"
		}
		// Every submitted message and every compaction boundary moves the
		// reload fence, so a moved-on session reads stale.
		if compact || !input.Empty() {
			r.bumpContextRev()
		}
		if strings.TrimSpace(input.Display) != "" {
			console.EnsureTranscriptGap()
			_ = out.AppendDocumentBlock(terminal.DocumentBlock{Kind: terminal.DocumentUser, Text: input.Display})
		}
		runOutcome, visibleRunText = "", false
		ctx, stop := context.WithCancel(context.Background())
		cancel, busy, compacting = stop, true, compact
		compactionCountAtStart = r.ac.Snapshot().CompactionCount
		r.state = StateWaiting
		setActivity("Working")
		if compact {
			r.state = StateCompacting
		}
		gen, seq := r.generation, int64(0)
		cfg := r.cfg
		cfg.ACPolicy = r.ac
		cfg.CompactionProvider = func() (provider.Provider, provider.ModelMeta, func(), error) {
			return r.compactionProvider(r.ac.Snapshot().CompactorModel)
		}
		cfg.DynamicConfig = func() (string, []config.PromptSection, []provider.ToolDef) {
			state := r.loops.Snapshot()
			definitions := tools.Definitions(r.settings.DefaultTools)
			if state.Active && !state.Forever {
				definitions = append(definitions, loop.ToolDefinition())
			}
			if r.syncboardAvailable() {
				definitions = append(definitions, syncboardToolDefinition())
			}
			sections := r.sections
			if addition := strings.TrimSpace(loop.SystemPromptAddition(state)); addition != "" {
				sections = append(append([]config.PromptSection(nil), sections...), config.PromptSection{Name: "loop", Body: "<loop>\n" + addition + "\n</loop>"})
			}
			return config.RenderSections(sections), sections, definitions
		}
		cfg.InternalTools = map[string]agent.InternalToolHandler{
			loop.ControlToolName: func(_ context.Context, invocation tools.Invocation) tools.Result {
				result := r.loops.HandleControl(invocation.Arguments)
				return tools.Result{Content: []tools.Block{{Type: "text", Text: result.Text}}, Details: result.State, IsError: result.Error}
			},
			syncboardToolName: r.syncboardInternalHandler,
		}
		cfg.AfterToolCall = func(_ context.Context, invocation tools.Invocation, result tools.Result) error {
			if invocation.Name != loop.ControlToolName || result.IsError {
				return nil
			}
			state := r.loops.Snapshot()
			if !state.Active || state.Forever {
				return nil
			}
			_, err := r.store.AppendCustomMessage(loop.IterationMessageType, loop.BuildPrompt(state), false, nil)
			return err
		}
		cfg.ContinueOnEnd = func() (string, string, bool) {
			content, ok := r.loops.ContinuePrompt()
			return loop.NudgeMessageType, content, ok
		}
		if observable, ok := r.prov.(interface {
			SetRetryObserver(func(provider.RetryInfo))
		}); ok {
			observable.SetRetryObserver(func(info provider.RetryInfo) {
				seq++
				events <- event.Event{Type: event.Retry, Generation: gen, Seq: seq, SessionID: r.store.Header().ID,
					RetryAttempt: info.Attempt, RetryMaximum: info.Maximum, RetryDelayMs: int64(info.Delay / time.Millisecond), RetryClass: info.Class}
			})
		}
		activeConfig = cfg
		loop := r.newLoop(func(e event.Event) {
			seq++
			e.Generation, e.Seq, e.SessionID = gen, seq, r.store.Header().ID
			events <- e
		})
		activeLoop = loop
		// RunStart proves the initial append completed. Until then the input is
		// retained so a run that dies early can be recovered without re-asking
		// Phi or flattening verified content into the editor. Only a new
		// structured prompt touches the marker: a compaction start between
		// admission and RunStart must not lose it. A native editor submission
		// while a bridge prompt is in flight is refused, so a non-structured
		// replacement cannot steal the marker either.
		r.retainInFlight(input)
		go func() {
			if compact {
				if resumeThisCompaction {
					done <- loop.CompactAndResume(ctx, cfg, focus)
				} else {
					done <- loop.Compact(ctx, cfg, focus)
				}
			} else if input.Structured() {
				done <- loop.SubmitContent(ctx, input, cfg)
			} else if input.Invocation != "" {
				done <- loop.SubmitWithProvenance(ctx, input.Text(), input.Invocation, input.Source, cfg)
			} else {
				done <- loop.Submit(ctx, input.Text(), cfg)
			}
		}()
		return true, ""
	}
	// start is the editor and command entry point: a refused submission is
	// handed back to the user as a restored draft.
	start := func(text, display, focus, invocation, source string, compact bool) {
		input := agent.UserInput{Content: text, Display: display, Invocation: invocation, Source: source}
		if admitted, _ := startInput(input, focus, compact); admitted {
			return
		}
		console.Editor.RestoreDraft(submittedDraft, submittedCursor)
		console.RefreshCompletion()
		r.draw(console)
	}
	startCompact := func(focus string) error {
		if writeErr := r.store.WriteError(); writeErr != nil {
			return writeErr
		}
		resumeAfterCompaction = false
		start("", "", focus, "", "", true)
		return nil
	}
	bridge.compactRun = startCompact
	bridge.reloadRun = func() error {
		_, err := r.handleReloadCommand("", io.Discard)
		if err != nil {
			notice(terminal.NoticeWarning, "Resource reload failed. "+err.Error())
		}
		r.draw(console)
		return err
	}
	checkAndStartCompact := func(focus string, explicit, resume bool) {
		if writeErr := r.store.WriteError(); writeErr != nil {
			notice(terminal.NoticeError, "Storage is blocked: "+writeErr.Error()+" (open a checked copy or /new)")
			return
		}
		if explicit {
			// Pi starts the compaction and reports the outcome through the
			// run: the preparation happens once, inside the run.
			resumeAfterCompaction = resume
			start("", "", focus, "", "", true)
			return
		}
		checker := agent.New(r.store, r.reg, r.prov, nil)
		_, checkErr := checker.CheckCompaction(r.cfg)
		var noWork *agent.CompactionNoWorkError
		if errors.As(checkErr, &noWork) {
			return
		}
		if checkErr != nil {
			notice(terminal.NoticeError, checkErr.Error())
			return
		}
		resumeAfterCompaction = resume
		start("", "", focus, "", "", true)
	}
	endTurnReady := func() bool {
		if busy || exiting || r.state != StateIdle || r.selector != nil {
			return false
		}
		proj, err := r.store.Projection()
		if err != nil {
			return false
		}
		tokens := agent.EstimatedContext(proj, r.cfg)
		return r.ac.ShouldCompactAtEndOfTurn(tokens, r.cfg.Model.ContextWindow, r.settings.Compaction.Enabled, true, false)
	}
	scheduleEndTurn := func() {
		cancelACEndTurn()
		if !endTurnReady() {
			return
		}
		delay := r.ac.Snapshot().EndOfTurnDelay
		if delay <= 0 {
			checkAndStartCompact("", false, false)
			return
		}
		acEndTurnTimer.Reset(delay)
		acEndTurnTimeout = acEndTurnTimer.C
	}
	acCommand := func(args string) {
		r.handleACCommand(args, out, func(focus string) { checkAndStartCompact(focus, true, false) }, cancelACEndTurn,
			func() bool { return acEndTurnTimeout != nil }, scheduleEndTurn)
	}
	stopLoopCommand := func() {
		if r.loops == nil {
			return
		}
		state, stopped, err := r.loops.Stop("Stopped by user")
		if err != nil {
			notice(terminal.NoticeError, "Could not stop loop: "+err.Error())
		} else if !stopped {
			notice(terminal.NoticeInfo, "No active loop")
		} else {
			notice(terminal.NoticeWarning, fmt.Sprintf("Loop stopped after %d iteration(s)", state.CurrentStep+1))
		}
		console.SetLoopStatus(r.loops.StatusLine())
		r.draw(console)
	}
	suspend := func() error {
		if err := r.lifecycle.Suspend(); err != nil {
			return err
		}
		inputs = console.Inputs
		r.beginTerminalColorQuery(console)
		r.draw(console)
		return console.Err()
	}
	console.SetDeferDraw(false)
	if r.loops != nil {
		console.SetLoopStatus(r.loops.StatusLine())
	}
	r.draw(console)
	if len(initialMessages) > 0 {
		message := initialMessages[0]
		initialMessages = initialMessages[1:]
		start(message, message, "", "", "", false)
	}
	resizeTimer := time.NewTimer(time.Hour)
	if !resizeTimer.Stop() {
		<-resizeTimer.C
	}
	defer resizeTimer.Stop()
	var resizeTimeout <-chan time.Time
	requestResizeDraw := func() {
		console.RequestFullRedraw()
		if !resizeTimer.Stop() {
			select {
			case <-resizeTimer.C:
			default:
			}
		}
		resizeTimer.Reset(150 * time.Millisecond)
		resizeTimeout = resizeTimer.C
	}
	for {
		if exiting && !busy {
			if count := len(r.recovery); count > 0 {
				fmt.Fprintln(out, terminal.Sanitize(fmt.Sprintf("%d undelivered attachment prompt(s) were not sent; Phi keeps a saved copy.", count)))
			}
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
		case <-resizeTimeout:
			resizeTimeout = nil
			r.draw(console)
		case <-acEndTurnTimeout:
			acEndTurnTimeout = nil
			if endTurnReady() {
				checkAndStartCompact("", false, false)
			}
		case <-themeChanges:
			if err := r.reloadTheme(); err != nil {
				if err.Error() != lastThemeWatchError {
					notice(terminal.NoticeWarning, "Theme reload failed; the last valid theme remains active. "+err.Error())
					lastThemeWatchError = err.Error()
				}
				continue
			}
			lastThemeWatchError = ""
			if watch != nil {
				watch.SetSelected(r.selectedUserThemePath())
			}
			r.draw(console)
		case <-frameTick:
			frameTick = nil
			_, beforeFrames, _, _ := console.FrameStats()
			console.BeginFrame()
			thinkingStream.Flush()
			stream.Flush()
			if busy && activityText != "" && time.Since(lastSpinner) >= 80*time.Millisecond {
				spinnerIndex = (spinnerIndex + 1) % len(spinnerFrames)
				lastSpinner = time.Now()
				console.Status(activityFrame())
			}
			console.Flush()
			_ = console.EndFrame()
			_, afterFrames, _, _ := console.FrameStats()
			r.timerFrames += afterFrames - beforeFrames
		case sig := <-signals:
			if isResize(sig) {
				requestResizeDraw()
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
		case req := <-bridge.requests():
			bridge.start, bridge.startInput, bridge.activeLoop = start, startInput, activeLoop
			bridge.handle(req, busy)
		case <-bridge.completed():
			bridge.start, bridge.startInput, bridge.activeLoop = start, startInput, activeLoop
			bridge.finishPreparation(busy)
			r.draw(console)
		case <-bridge.timeout():
			bridge.expireAttachment()
			r.draw(console)
		case job := <-bridge.searchResults():
			bridge.finishSearch(job)
		case ev := <-events:
			recordEventFrames(func() { applyEvent(ev) })
		case err := <-done:
			if observable, ok := r.prov.(interface {
				SetRetryObserver(func(provider.RetryInfo))
			}); ok {
				observable.SetRetryObserver(nil)
			}
			_, doneFramesBefore, _, _ := console.FrameStats()
			// Drain events before drawing the next prompt. A completed run can
			// otherwise race its final text delta through this select.
			for len(events) > 0 {
				ev := <-events
				recordEventFrames(func() { applyEvent(ev) })
			}
			cancel()
			busy = false
			bridge.resolveCompactRun(err)
			compactionCommitted := r.ac.Snapshot().CompactionCount > compactionCountAtStart
			// Queued input can arrive after the run's last queue poll but before
			// its settled event reaches this loop. Continue it after a clean run;
			// on cancellation or failure, restore it to the editor instead.
			var undelivered []string
			var lateQueueLoop *agent.Loop
			if r.recoverInFlight() > 0 {
				notice(terminal.NoticeWarning, "An attachment prompt was not delivered. Phi keeps a saved copy you can restore.")
			}
			if activeLoop != nil {
				pendingSteering := activeLoop.PendingSteering()
				pendingFollowUp := activeLoop.PendingFollowUp()
				if err == nil && !exiting && (len(pendingSteering) > 0 || len(pendingFollowUp) > 0) {
					lateQueueLoop = activeLoop
				} else {
					undelivered = append(r.takeSteering(activeLoop), activeLoop.ClearFollowUp()...)
				}
			}
			if lateQueueLoop != nil {
				activeLoop = lateQueueLoop
				console.SetSteeringRows(activeLoop.PendingSteering())
				console.SetFollowUpRows(activeLoop.PendingFollowUp())
			} else {
				activeLoop = nil
				console.SetSteeringRows(nil)
				console.SetFollowUpRows(nil)
			}
			if len(undelivered) > 0 {
				combined := strings.Join(undelivered, "\n\n")
				if current := console.Editor.Text(); strings.TrimSpace(current) != "" {
					combined += "\n\n" + current
				}
				console.Editor.RestoreDraft(combined, len([]rune(combined)))
				console.RefreshCompletion()
			}
			thinkingStream.Close()
			stream.Close()
			console.SetThinking(false)
			console.ClearToolRow()
			for _, active := range r.activeTools {
				// A result append can fail after execution. Keep its identity,
				// but never claim success or leave a queued start running.
				status.UpdateSummary(active.name, active.callID, active.summary, "interrupted", presentationDisplay(r.settings), r.theme, noColorEnvironment())
			}
			if len(r.activeTools) > 0 {
				toolSpacing.pending = true
			}
			finishTools()
			r.activeTools = nil
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
					notice(terminal.NoticeWarning, "Visible output was saved, but the operation was cancelled.")
				} else {
					notice(terminal.NoticeWarning, "Request cancelled.")
				}
			} else if err != nil {
				var noWork *agent.CompactionNoWorkError
				switch {
				case errors.As(err, &noWork):
					message := "Nothing to compact yet. Recent conversation is already within the retained budget."
					switch noWork.Disposition {
					case agent.CompactionEmpty:
						message = "Nothing to compact yet. The session has no conversation history."
					case agent.CompactionAlreadyDone:
						message = "Nothing to compact yet. This session is already compacted."
					}
					notice(terminal.NoticeInfo, message)
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
			} else if compacting || compactionCommitted {
				// Pi renders the committed compaction entry as a collapsed
				// summary box with the token count; the summary stays hidden.
				tokens := 0.0
				if path, pathErr := r.store.ActivePath(); pathErr == nil {
					for i := len(path) - 1; i >= 0; i-- {
						if path[i].Compaction != nil {
							tokens = path[i].Compaction.TokensBefore
							break
						}
					}
				}
				for _, line := range terminal.FormatMetadataMessage("[compaction] ", session.CompactionCollapsedText(tokens), console.StatusWidth(), noColorEnvironment(), presentationDisplay(r.settings), r.theme) {
					fmt.Fprintln(out, line)
				}
			}
			if compacting {
				// A finished compaction rewrote history: move the fence even
				// when the run itself was refused or failed.
				r.bumpContextRev()
			}
			compacting = false
			if exiting {
				_, doneFramesAfter, _, _ := console.FrameStats()
				r.eventFrames += doneFramesAfter - doneFramesBefore
				return outputErr
			}
			r.state = StateIdle
			if runOutcome == event.OutcomeUnsaved || r.store.WriteError() != nil {
				r.state = StateStorageBlocked
			}
			if suspendPending {
				suspendPending = false
				if err := suspend(); err != nil {
					_, doneFramesAfter, _, _ := console.FrameStats()
					r.eventFrames += doneFramesAfter - doneFramesBefore
					return err
				}
			} else {
				console.Status("")
				r.refreshPresentation(console)
				r.draw(console)
			}
			if r.state == StateIdle && len(initialMessages) > 0 {
				message := initialMessages[0]
				initialMessages = initialMessages[1:]
				start(message, message, "", "", "", false)
			}
			if lateQueueLoop != nil && r.state == StateIdle && len(initialMessages) == 0 && !exiting {
				ctx, stop := context.WithCancel(context.Background())
				cancel = stop
				busy = true
				compacting = false
				r.state = StateWaiting
				setActivity("Working")
				activeLoop = lateQueueLoop
				compactionCountAtStart = r.ac.Snapshot().CompactionCount
				lateLoop, cfg := lateQueueLoop, activeConfig
				go func() { done <- lateLoop.ContinuePending(ctx, cfg) }()
				r.draw(console)
			}
			if pendingACCompactFocus != nil && r.state == StateIdle && len(initialMessages) == 0 && len(undelivered) == 0 && !exiting {
				focus := *pendingACCompactFocus
				pendingACCompactFocus = nil
				checkAndStartCompact(focus, true, true)
			}
			if err == nil && lateQueueLoop == nil && r.state == StateIdle && len(initialMessages) == 0 && len(undelivered) == 0 && !exiting && r.ac.Snapshot().CompactionCount == compactionCountAtStart {
				scheduleEndTurn()
			}
			_, doneFramesAfter, _, _ := console.FrameStats()
			r.eventFrames += doneFramesAfter - doneFramesBefore
		case input, ok := <-inputSource:
			if fromPendingInput {
				pendingInputs = pendingInputs[1:]
				pendingInputBytes -= len(input.Key)
			}
			if ok && !input.Paste && r.loops != nil && r.loops.Snapshot().Active {
				key := string(input.Key)
				if key == "\x18" || key == "\x1b[88;6u" {
					stopLoopCommand()
					if busy {
						cancelActive()
					}
					continue
				}
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
			if len(input.Reply) > 0 {
				if r.handleTerminalReply(console, input.Reply) {
					if watch != nil {
						watch.SetSelected(r.selectedUserThemePath())
					}
					r.draw(console)
				}
				continue
			}
			if input.Resize {
				requestResizeDraw()
				continue
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
			if !busy && r.settingsPicker != nil {
				if input.Paste {
					continue
				}
				switch string(input.Key) {
				case "\x1b", "\x03":
					closeSettings()
				case "\x1b[A", "\x1bOA":
					r.settingsPicker.Move(-1)
					r.draw(console)
				case "\x1b[B", "\x1bOB":
					r.settingsPicker.Move(1)
					r.draw(console)
				case "\x1b[5~":
					r.settingsPicker.Page(-1)
					r.draw(console)
				case "\x1b[6~":
					r.settingsPicker.Page(1)
					r.draw(console)
				case "\r", " ":
					item, value, ok := r.settingsPicker.Cycle()
					if !ok {
						r.draw(console)
						continue
					}
					path, err := r.applySetting(item.ID, value, console)
					if err != nil {
						notice(terminal.NoticeError, err.Error())
						index := r.settingsPicker.Index
						r.settingsPicker = r.newSettingsPicker()
						r.settingsPicker.Index = index
					} else if path != nil {
						if err := r.persistSettingsValue(path, value.Value); err != nil {
							notice(terminal.NoticeError, "Saved for this session only: "+err.Error())
						}
					}
					if err == nil {
						if item.ID == "theme" || item.ID == "color-mode" {
							if watch != nil {
								watch.SetSelected(r.selectedUserThemePath())
							}
						}
						if item.ID == "theme" && r.options.UseTheme != "" {
							notice(terminal.NoticeInfo, "Stored the default theme; --use-theme keeps the current display.")
						}
					}
					console.RequestFullRedraw()
					r.footerKey = ""
					r.draw(console)
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
					case "\x1b[A", "\x1bOA":
						r.selector.Move(-1)
						r.draw(console)
						continue
					case "\x1b[B", "\x1bOB":
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
						replayActivationDocument(emitReplayBlock, act, false, oldID)
						closeSelector(false)
						continue
					}
				}
				before := console.Editor.Text()
				if key == "\x04" {
					// Ctrl+D stays with the focused picker. It deletes forward in
					// the query and never exits the session.
					console.Editor.Delete()
				} else {
					_, _, quit, _ := console.Handle(input, false)
					if quit {
						return console.Err()
					}
				}
				if query := console.Editor.Text(); query != before {
					r.selector.SetFilter(query)
					r.draw(console)
				}
				continue
			}
			if !input.Paste && len(input.Key) == 1 && input.Key[0] == 16 {
				status, err := r.cycleModel(1)
				if err != nil {
					notice(terminal.NoticeError, err.Error())
				} else {
					console.Status(status)
				}
				r.draw(console)
				continue
			}
			if !busy && !input.Paste && len(input.Key) == 1 && input.Key[0] == 12 {
				openSelector(modelSelector(r), console.Editor.Text(), console.Editor.Pos(), "", true)
				continue
			}
			if !input.Paste && len(input.Key) == 1 && input.Key[0] == 20 {
				// Ctrl+T toggles Pi's reasoning visibility, also during a run.
				r.settings.HideThinkingBlock = !r.settings.HideThinkingBlock
				if err := r.persistSettingsValue([]string{"hideThinkingBlock"}, r.settings.HideThinkingBlock); err != nil {
					notice(terminal.NoticeError, "Saved for this session only: "+err.Error())
				}
				r.footerKey = ""
				console.RequestFullRedraw()
				r.draw(console)
				state := "visible"
				if r.settings.HideThinkingBlock {
					state = "hidden"
				}
				console.Status("Thinking blocks: " + state)
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
			if strings.TrimSpace(text) == "" {
				r.draw(console)
				continue
			}
			if busy {
				// Pi's editor stays live while a run is active. AC policy commands
				// can update session-local policy; /ac compact waits for this run
				// to settle and then resumes it after compaction.
				if strings.HasPrefix(strings.TrimSpace(text), "/") {
					if cmd, ok := terminal.ParseCommand(text); ok && cmd.Name == "settings" {
						notice(terminal.NoticeInfo, "Finish or cancel the run before changing settings.")
						console.Editor.RestoreDraft(text, len([]rune(text)))
						console.RefreshCompletion()
						r.draw(console)
						continue
					}
					if cmd, ok := terminal.ParseCommand(text); ok && (cmd.Name == "ac" || cmd.Name == "autocompact") {
						parts := strings.Fields(cmd.Arg)
						if len(parts) > 0 && strings.EqualFold(parts[0], "compact") {
							if compacting {
								notice(terminal.NoticeInfo, "Compaction is already running.")
							} else if pendingACCompactFocus != nil {
								notice(terminal.NoticeInfo, "An /ac compact request is already queued.")
							} else {
								focus := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cmd.Arg), parts[0]))
								pendingACCompactFocus = &focus
								notice(terminal.NoticeInfo, "Compaction queued until the active run settles; it will resume the task afterward.")
							}
							console.Editor.Clear()
							r.draw(console)
							continue
						}
						acCommand(cmd.Arg)
						r.draw(console)
						continue
					}
					if cmd, ok := terminal.ParseCommand(text); ok && cmd.Name == "loop-stop" {
						if strings.TrimSpace(cmd.Arg) != "" {
							notice(terminal.NoticeError, "use /loop-stop")
							r.draw(console)
						} else {
							stopLoopCommand()
						}
						continue
					}
					console.Editor.RestoreDraft(text, len([]rune(text)))
					console.RefreshCompletion()
					r.draw(console)
					continue
				}
				if activeLoop != nil {
					if input.FollowUp {
						activeLoop.FollowUp(text)
						console.SetFollowUpRows(activeLoop.PendingFollowUp())
					} else {
						activeLoop.Steer(text)
						console.SetSteeringRows(activeLoop.PendingSteering())
					}
					r.bumpContextRev()
				}
				r.draw(console)
				continue
			}
			if cmd, ok := terminal.ParseCommand(text); ok {
				if cmd.Name == "loop" {
					if err := r.store.WriteError(); err != nil {
						notice(terminal.NoticeError, "Storage is blocked: "+err.Error())
						console.Editor.RestoreDraft(text, len([]rune(text)))
						console.RefreshCompletion()
						r.draw(console)
						continue
					}
					if r.authErr != nil {
						notice(terminal.NoticeError, r.authErr.Error())
						console.Editor.RestoreDraft(text, len([]rune(text)))
						console.RefreshCompletion()
						r.draw(console)
						continue
					}
					state, err := r.loops.Start(cmd.Arg)
					if err != nil {
						notice(terminal.NoticeError, err.Error())
						console.Editor.RestoreDraft(text, len([]rune(text)))
						console.RefreshCompletion()
						r.draw(console)
					} else {
						prompt := loop.BuildPrompt(state)
						console.SetLoopStatus(r.loops.StatusLine())
						start(prompt, prompt, "", "", "", false)
					}
					continue
				}
				if cmd.Name == "loop-stop" {
					if strings.TrimSpace(cmd.Arg) != "" {
						notice(terminal.NoticeError, "use /loop-stop")
						r.draw(console)
					} else {
						stopLoopCommand()
					}
					continue
				}
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
				if cmd.Name == "ac" || cmd.Name == "autocompact" {
					acCommand(cmd.Arg)
					r.draw(console)
					continue
				}
				if cmd.Name == "compact" {
					if writeErr := r.store.WriteError(); writeErr != nil {
						notice(terminal.NoticeError, "Storage is blocked: "+writeErr.Error()+" (open a checked copy or /new)")
						r.draw(console)
						continue
					}
					// Pi starts the compaction and reports the outcome through
					// the run: the preparation happens once, inside the run.
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
				if cmd.Name == "trust" {
					openTrust()
					continue
				}
				if cmd.Name == "settings" {
					if strings.TrimSpace(cmd.Arg) != "" {
						notice(terminal.NoticeError, "use /settings")
						r.draw(console)
						continue
					}
					openSettings()
					continue
				}
				quit, err := r.command(cmd, out)
				if cmd.Name == "reload" && err == nil && watch != nil {
					watch.SetSelected(r.selectedUserThemePath())
				}
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
	switch cmd.Name {
	case "quit":
		return r.handleQuitCommand(cmd.Arg, out)
	case "hotkeys":
		return r.handleHotkeysCommand(cmd.Arg, out)
	case "session":
		return r.handleSessionCommand(cmd.Arg, out)
	case "settings":
		return r.handleSettingsCommand(cmd.Arg, out)
	case "reload":
		return r.handleReloadCommand(cmd.Arg, out)
	case "new":
		return r.handleNewCommand(cmd.Arg, out)
	case "name":
		return r.handleNameCommand(cmd.Arg, out)
	case "clone":
		return r.handleCloneCommand(cmd.Arg, out)
	case "model":
		return r.handleModelCommand(cmd.Arg, out)
	case "thinking":
		return r.handleThinkingCommand(cmd.Arg, out)
	default:
		return false, fmt.Errorf("unknown command")
	}
}

// printStatusText renders Pi's showStatus line: a dim status message that a
// following status replaces. Plain writers keep one plain line.
func printStatusText(out io.Writer, text string) {
	if structured, ok := out.(interface {
		AppendDocumentBlock(terminal.DocumentBlock) error
	}); ok {
		_ = structured.AppendDocumentBlock(terminal.DocumentBlock{Kind: terminal.DocumentStatus, Text: text})
		return
	}
	fmt.Fprintln(out, terminal.Sanitize(text))
}

func (r *runtime) handleQuitCommand(arg string, out io.Writer) (bool, error) {
	return true, nil
}

func (r *runtime) handleHotkeysCommand(arg string, out io.Writer) (bool, error) {
	fmt.Fprintln(out, terminal.Sanitize(terminal.HotkeysText()))
	return false, nil
}

func (r *runtime) handleSessionCommand(arg string, out io.Writer) (bool, error) {
	print := func(s string) { fmt.Fprintln(out, terminal.Sanitize(s)) }
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
	nameLine := ""
	if name := r.store.SessionName(); name != "" {
		nameLine = "name " + name + "\n"
	}
	print(fmt.Sprintf("session %s\n%sfile %s\ncwd %s\nprovider %s api %s\nrequested model %s\nlast returned model %s\nthinking %s\ncontext messages %d, estimated context tokens %d\nstored usage: input %g, output %g, cache read %g, cache write %g, total %g (%d attempts)\nprice unknown (no cost calculation)\ncontinuation: %s", r.store.Header().ID, nameLine, r.store.File(), r.store.Cwd(), r.ov.Provider, r.cfg.Model.API, r.ov.Model, returned, provider.ThinkingDescription(r.cfg.Model, r.ov.Thinking), len(proj.Flat), agent.EstimatedContext(proj, r.cfg), accounting.Usage.Input, accounting.Usage.Output, accounting.Usage.CacheRead, accounting.Usage.CacheWrite, accounting.Usage.TotalTokens, accounting.Attempts, continuable(proj)))
	themeSource := "built-in"
	if r.theme != nil && r.theme.SourcePath != "" {
		themeSource = r.theme.SourcePath
	}
	print(fmt.Sprintf("theme %s (%s, %s, %s; %s)", r.themeName, r.themeAppearance, r.themeMode, themeSource, r.themeSetting))
	if r.themeFallback != "" {
		print("theme fallback: " + r.themeFallback)
	}
	return false, nil
}

func (r *runtime) handleReloadCommand(arg string, out io.Writer) (bool, error) {
	if strings.TrimSpace(arg) != "" {
		return false, fmt.Errorf("use /reload")
	}
	if err := r.reloadTheme(); err != nil {
		return false, err
	}
	if err := r.reloadResources(); err != nil {
		return false, err
	}
	if r.lifecycle != nil && r.lifecycle.console != nil {
		r.lifecycle.console.SetTerminalColorSchemeNotifications(r.themeFollowsAppearance() && !noColorEnvironment())
	}
	printStatusText(out, "Reloaded keybindings, extensions, skills, prompts, themes, and context files")
	return false, nil
}

func (r *runtime) handleNewCommand(arg string, out io.Writer) (bool, error) {
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
	if structured, ok := out.(interface {
		AppendDocumentBlock(terminal.DocumentBlock) error
	}); ok {
		_ = structured.AppendDocumentBlock(terminal.DocumentBlock{Kind: terminal.DocumentAccent, Text: "✓ New session started"})
	} else {
		fmt.Fprintln(out, terminal.Sanitize("✓ New session started"))
	}
	return false, nil
}

func (r *runtime) handleNameCommand(arg string, out io.Writer) (bool, error) {
	// Pi's /name: no argument shows the current name, an argument appends a
	// session_info entry. Newlines collapse to spaces; the latest entry wins.
	arg = strings.TrimSpace(arg)
	if arg == "" {
		if current := r.store.SessionName(); current != "" {
			printStatusText(out, "Session name: "+current)
		} else {
			for _, line := range terminal.FormatNotice(terminal.NoticeWarning, "Usage: /name <name>", 80, noColorEnvironment(), r.theme) {
				fmt.Fprintln(out, line)
			}
		}
		return false, nil
	}
	sanitized := strings.TrimSpace(newlineRun.ReplaceAllString(arg, " "))
	if _, err := r.store.AppendSessionInfo(sanitized); err != nil {
		return false, err
	}
	if sanitized != arg {
		for _, line := range terminal.FormatNotice(terminal.NoticeWarning, fmt.Sprintf("Session name was normalized from %q to %q", arg, sanitized), 80, noColorEnvironment(), r.theme) {
			fmt.Fprintln(out, line)
		}
	}
	printStatusText(out, "Session name set: "+r.store.SessionName())
	return false, nil
}

func (r *runtime) handleCloneCommand(arg string, out io.Writer) (bool, error) {
	if strings.TrimSpace(arg) != "" {
		return false, fmt.Errorf("use /clone")
	}
	// Pi's /clone forks at the current leaf into a new session file and
	// reports the outcome as a status message.
	if r.store.File() == "" || (len(r.store.Entries()) == 0 && r.store.EntryCount() == 0) {
		printStatusText(out, "Nothing to clone yet")
		return false, nil
	}
	if _, err := os.Stat(r.store.File()); os.IsNotExist(err) {
		printStatusText(out, "Nothing to clone yet")
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := r.cloneSession(func(string) {}); err != nil {
		return false, err
	}
	printStatusText(out, "Cloned to new session")
	return false, nil
}

// renderProjectTrustWarningIfNeeded shows Pi's warning when the project is
// not trusted and holds trust-requiring resources.
func (r *runtime) renderProjectTrustWarningIfNeeded(console *terminal.Console) {
	if r.projectTrusted || !hasTrustRequiringProjectResources(r.store.Cwd()) {
		return
	}
	// Pi renders warning-colored chat text without the showWarning prefix.
	_ = console.AppendTranscriptBlock(terminal.DocumentBlock{
		Kind: terminal.DocumentWarning,
		Text: "This project is not trusted. Project .pi resources and packages are ignored. Use /trust to save a trust decision, then restart " + terminal.AppName + ".",
	})
}

func (r *runtime) handleModelCommand(arg string, out io.Writer) (bool, error) {
	print := func(s string) { fmt.Fprintln(out, terminal.Sanitize(s)) }
	if strings.TrimSpace(arg) == "" {
		// The interactive loop opens the searchable picker for an empty
		// argument, so this fallback never prints another result page.
		print(fmt.Sprintf("current %s/%s thinking %s (use /model with a provider/model name)", r.ov.Provider, r.ov.Model, provider.ThinkingDescription(r.cfg.Model, r.ov.Thinking)))
		return false, nil
	}
	next := r.ov
	parts := strings.Fields(arg)
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
	if strings.TrimSpace(next.Model) == "" {
		return false, fmt.Errorf("use /model [provider/]model")
	}
	candidate := *r
	candidate.prov = nil
	if err := candidate.configure(next); err != nil {
		return false, err
	}
	if _, err := r.store.AppendModelChange(candidate.ov.Provider, candidate.ov.Model); err != nil {
		if candidate.prov != nil {
			candidate.prov.CloseIdle()
		}
		return false, err
	}
	r.installConfiguration(&candidate)
	if r.ac != nil {
		r.ac.ResetGrowth()
	}
	// Pi names the selected model as a status message.
	printStatusText(out, "Model: "+candidate.ov.Model)
	return false, nil
}

func (r *runtime) handleThinkingCommand(arg string, out io.Writer) (bool, error) {
	print := func(s string) { fmt.Fprintln(out, terminal.Sanitize(s)) }
	if strings.TrimSpace(arg) == "" {
		print(fmt.Sprintf("thinking %s (supported: %s)", provider.ThinkingDescription(r.cfg.Model, r.ov.Thinking), provider.ThinkingChoices(r.cfg.Model)))
		return false, nil
	}
	// Pi matches the level case-insensitively against the levels the current
	// model supports and reports the exact list in the error.
	levels := provider.SupportedThinking(r.cfg.Model)
	normalized := strings.ToLower(strings.TrimSpace(arg))
	level := ""
	for _, candidate := range levels {
		if strings.ToLower(candidate) == normalized {
			level = candidate
			break
		}
	}
	if level == "" {
		return false, fmt.Errorf("Unknown thinking level %q. Available levels: %s.", strings.TrimSpace(arg), strings.Join(levels, ", "))
	}
	if level == r.ov.Thinking {
		print("Thinking level: " + level)
		return false, nil
	}
	next := r.ov
	next.Thinking = level
	candidate := *r
	candidate.prov = nil
	if err := candidate.configure(next); err != nil {
		return false, err
	}
	if _, err := r.store.AppendThinkingLevelChange(level); err != nil {
		if candidate.prov != nil {
			candidate.prov.CloseIdle()
		}
		return false, err
	}
	r.installConfiguration(&candidate)
	printStatusText(out, "Thinking level: "+level)
	return false, nil
}
