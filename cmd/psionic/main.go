package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/session"
)

var version = "0.1.0"

type options struct {
	configPath string
	agentID    string
	workspace  string
	stateDir   string
	agentDir   string
	// sharedAgentDir holds models.json and auth.json. It lets a service keep
	// its own settings.json while sharing one catalogue and one credential
	// store with the main Psi installation. It defaults to the agent
	// directory.
	sharedAgentDir string
	secretStoreDB  string
	check          bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "psionic: %s\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	opts, showVersion, err := parseFlags(args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if showVersion {
		fmt.Fprintln(stdout, version)
		return nil
	}
	loaded, err := prepare(opts)
	if err != nil {
		return err
	}
	if opts.check {
		return printCheck(stdout, loaded)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, loaded, stderr)
}

func parseFlags(args []string, stdout, stderr io.Writer) (options, bool, error) {
	var opts options
	var showVersion bool
	flags := flag.NewFlagSet("psionic", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.configPath, "config", "", "path to an existing settings file")
	flags.StringVar(&opts.agentID, "agent", "", "agent entry to run")
	flags.StringVar(&opts.workspace, "workspace", "", "workspace override")
	flags.StringVar(&opts.stateDir, "state-dir", "", "private state directory")
	flags.StringVar(&opts.agentDir, "agent-dir", "", "Psi agent directory for settings.json")
	flags.StringVar(&opts.sharedAgentDir, "shared-agent-dir", "", "Psi agent directory for models.json and auth.json; defaults to the agent directory")
	flags.StringVar(&opts.secretStoreDB, "secret-store-db", "", "existing read-only SQLite secret-store database")
	flags.BoolVar(&opts.check, "check", false, "validate settings without a Discord connection or state writes; may execute the configured exec token provider")
	flags.BoolVar(&showVersion, "version", false, "print the version")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: psionic [--config PATH] [--agent ID] [--workspace DIR] [--state-dir DIR] [--agent-dir DIR] [--shared-agent-dir DIR] [--secret-store-db PATH] [--check]")
		fmt.Fprintln(stderr, "Running psionic with no flag uses ~/.config/psionic/config.json5, or PSIONIC_CONFIG when it is set.")
		fmt.Fprintln(stderr, "Discord replies are capped at 16 messages; truncated replies include a notice.")
		fmt.Fprintln(stderr, "Discord token sources: literal, environment, private file, exec, or store.")
		fmt.Fprintln(stderr, "DISCORD_BOT_TOKEN is used only when channels.discord.token is absent.")
		fmt.Fprintln(stderr, "Store uses --secret-store-db, PSIONIC_SECRET_STORE_DB, or a unique private sibling database.")
		fmt.Fprintln(stderr, "Owner commands: /new, /reset, /stop, /status, /model, /compact.")
		fmt.Fprintln(stderr, "Group direct messages are off unless channels.discord.dm.groupEnabled is true.")
		fmt.Fprintln(stderr, "The exec tool is off unless tools.exec.enabled is true and it runs only in owner direct messages.")
		fmt.Fprintln(stderr, "Attachments are downloaded to memory from Discord's CDN only, with per-kind size caps.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return options{}, false, err
	}
	if flags.NArg() != 0 {
		return options{}, false, fmt.Errorf("unexpected positional argument")
	}
	if showVersion {
		return opts, true, nil
	}
	return opts, false, nil
}

type loaded struct {
	options  options
	settings *fileSettings
	agent    selectedAgent
	models   *config.ModelsFile
	agentDir string
	// authPath is the credential file the providers read.
	authPath string
	// sharedCatalogue reports that the catalogue and credentials come from a
	// different directory than the settings file.
	sharedCatalogue bool
	token           string
	defaults        config.Settings
}

// resolveConfigPath finds the settings file. Precedence: the --config flag,
// then PSIONIC_CONFIG, then the standard configuration file under the user's
// configuration directory. Running Psionic without any flag must work, so the
// standard location is the normal case.
func resolveConfigPath(flagValue string, lookupEnv func(string) (string, bool)) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	if env, ok := lookupEnv("PSIONIC_CONFIG"); ok && strings.TrimSpace(env) != "" {
		expanded, err := expandUser(env)
		if err != nil {
			return "", fmt.Errorf("configuration file path is invalid")
		}
		return filepath.Abs(expanded)
	}
	for _, candidate := range defaultConfigPaths() {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no configuration file: create %s or pass --config", defaultConfigPaths()[0])
}

// defaultConfigPaths lists the standard configuration file locations, most
// preferred first.
func defaultConfigPaths() []string {
	dir := psionicConfigDir()
	return []string{filepath.Join(dir, "config.json5"), filepath.Join(dir, "config.json")}
}

// psionicConfigDir is the directory that holds Psionic's configuration file.
func psionicConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "psionic")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".config", "psionic")
}

// psionicStateDir is the directory that holds Psionic's transcripts.
func psionicStateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "psionic")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".local", "state", "psionic")
}

// prepare resolves configuration and token credentials, validates model
// metadata and provider setup without a model request, and checks paths. An
// exec token provider may run during --check.
func prepare(opts options) (*loaded, error) {
	configPath, err := resolveConfigPath(opts.configPath, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	settings, err := loadFileSettings(configPath, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	agent, err := resolveAgent(settings, opts.agentID)
	if err != nil {
		return nil, err
	}
	if opts.workspace != "" {
		workspace, err := expandUser(opts.workspace)
		if err != nil {
			return nil, fmt.Errorf("workspace: invalid path")
		}
		if workspace, err = filepath.Abs(workspace); err != nil {
			return nil, fmt.Errorf("workspace: invalid path")
		}
		agent.Workspace = workspace
	}
	if agent.Workspace == "" {
		return nil, fmt.Errorf("workspace: no workspace configured")
	}
	info, err := os.Stat(agent.Workspace)
	if err != nil {
		return nil, fmt.Errorf("workspace: directory is unavailable")
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace: not a directory")
	}
	if resolved, err := filepath.EvalSymlinks(agent.Workspace); err == nil {
		agent.Workspace = resolved
	}
	agentDir := opts.agentDir
	if agentDir == "" && settings.Psi != nil {
		agentDir = settings.Psi.AgentDir
	}
	if agentDir == "" {
		agentDir = session.AgentDir()
	}
	if agentDir, err = filepath.Abs(expandHomePath(agentDir)); err != nil {
		return nil, fmt.Errorf("agent directory: invalid path")
	}
	// The catalogue and the credentials may live in another Psi agent
	// directory than the settings file. That lets a service keep its own
	// model selection while sharing one catalogue and one credential store
	// with the main Psi installation, without copying or linking files.
	sharedDir := opts.sharedAgentDir
	if sharedDir == "" && settings.Psi != nil {
		sharedDir = settings.Psi.SharedAgentDir
	}
	if sharedDir == "" {
		sharedDir = agentDir
	}
	if sharedDir, err = filepath.Abs(expandHomePath(sharedDir)); err != nil {
		return nil, fmt.Errorf("shared agent directory: invalid path")
	}
	models, err := config.LoadModels(filepath.Join(sharedDir, "models.json"))
	if err != nil {
		return nil, fmt.Errorf("models: model metadata is malformed or unavailable")
	}
	defaults, err := config.LoadSettings(filepath.Join(agentDir, "settings.json"), "", false)
	if err != nil {
		return nil, fmt.Errorf("agent settings: unavailable")
	}
	authPath := filepath.Join(sharedDir, "auth.json")
	providerID := defaults.Settings.DefaultProvider
	if providerID == "" {
		return nil, fmt.Errorf("agent settings: defaultProvider is required")
	}
	catalogue, err := models.SelectModel(providerID, defaults.Settings.DefaultModel)
	if err != nil {
		return nil, fmt.Errorf("model: Psi default is unavailable")
	}
	agent.Model = providerID + "/" + catalogue.ID
	agent.Fallbacks = nil
	agent.Compaction = ""
	mainModel, mainCatalogue, providerID, err := resolveModel(models, agent.Model)
	if err != nil {
		return nil, err
	}
	client, err := buildProvider(models, authPath, providerID, mainModel, mainCatalogue, 1, "check")
	if err != nil {
		return nil, err
	}
	client.CloseIdle()
	if agent.Compaction != "" {
		compactionModel, compactionCatalogue, compactorID, err := resolveModel(models, agent.Compaction)
		if err != nil {
			return nil, fmt.Errorf("compaction model: %w", err)
		}
		compactor, err := buildProvider(models, authPath, compactorID, compactionModel, compactionCatalogue, 1, "check")
		if err != nil {
			return nil, fmt.Errorf("compaction model: %w", err)
		}
		compactor.CloseIdle()
	}
	direct, group := replyModes(settings)
	if direct == "message_tool" || group == "message_tool" {
		if !agent.Tools.allows("message") {
			return nil, fmt.Errorf("settings: visibleReplies message_tool requires the message tool")
		}
	}
	if group == "message_tool" {
		if err := checkGroupReplyTool(settings, agent); err != nil {
			return nil, err
		}
	}
	stateDirOption := opts.stateDir
	if stateDirOption == "" {
		stateDirOption = settings.StateDir
	}
	stateDir, err := resolveStateDir(stateDirOption)
	if err != nil {
		return nil, err
	}
	if err := checkStateDir(stateDir, agent.Workspace, agentDir, sharedDir); err != nil {
		return nil, err
	}
	if defaults.Settings.SessionDir != "" && overlaps(stateDir, defaults.Settings.SessionDir) {
		return nil, fmt.Errorf("state directory overlaps another agent's session directory")
	}
	token, err := botToken(settings, opts.secretStoreDB, opts.configPath)
	if err != nil {
		return nil, err
	}
	// Drop raw token settings now that resolution is complete. Go strings cannot
	// be wiped; dropping references shortens their application-owned lifetime.
	settings.Channels.Discord.Token = nil
	settings.Secrets = nil
	return &loaded{
		options: opts, settings: settings, agent: agent, models: models, agentDir: agentDir,
		authPath: authPath, sharedCatalogue: sharedDir != agentDir, token: token, defaults: defaults.Settings,
	}, nil
}

func botToken(settings *fileSettings, secretStoreDB, settingsPath string) (string, error) {
	if settings.Channels.Discord.Token != nil && settings.Channels.Discord.Token.store != "" {
		var err error
		secretStoreDB, err = resolveSecretStoreDatabase(secretStoreDB, settingsPath, os.LookupEnv)
		if err != nil {
			return "", err
		}
	}
	if settings.Channels.Discord.Token != nil {
		return settings.resolveDiscordToken(os.LookupEnv, secretStoreDB)
	}
	return resolveFallbackDiscordToken(os.LookupEnv)
}

func printCheck(w io.Writer, l *loaded) error {
	fmt.Fprintln(w, "agent: selected")
	fmt.Fprintln(w, "workspace: configured")
	fmt.Fprintln(w, "model: validated from Psi settings")
	if l.sharedCatalogue {
		fmt.Fprintln(w, "model catalogue: shared Psi agent directory")
	}
	if len(l.agent.Fallbacks) > 0 {
		fmt.Fprintf(w, "model fallbacks: ignored (%d configured)\n", len(l.agent.Fallbacks))
	}
	if l.agent.Compaction != "" {
		fmt.Fprintln(w, "compaction: configured")
	} else {
		fmt.Fprintln(w, "compaction: primary model")
	}
	discord := l.settings.Channels.Discord
	policy := newAccessPolicy(l.settings)
	fmt.Fprintf(w, "discord: dm=%s group=%s guilds=%d\n", policy.dmPolicy, policy.groupPolicy, len(discord.Guilds))
	fmt.Fprintf(w, "replies: %s\n", l.visibleReplies())
	if len(policy.ownerAllowFrom) == 0 {
		fmt.Fprintln(w, "private memory: unavailable (no owner is configured)")
	} else {
		fmt.Fprintln(w, "private memory: owner direct messages only")
	}
	if policy.dmPolicy == dmPolicyPairing {
		fmt.Fprintln(w, "direct messages: static allowlist only (pairing workflow is not supported)")
	}
	switch {
	case len(policy.ownerAllowFrom) == 0:
		fmt.Fprintln(w, "owner commands: unavailable (no owner is configured)")
	case policy.ownerBootstrapped:
		fmt.Fprintln(w, "owner commands: first direct-message allowlist entry (no owner is configured)")
	}
	if l.agent.Tools.Profile != "" || len(l.agent.Tools.Allow) > 0 || len(l.agent.Tools.AlsoAllow) > 0 {
		fmt.Fprintln(w, "tool filters: profile, allow, alsoAllow, and deny applied")
	}
	if len(l.agent.Tools.Deny) > 0 {
		fmt.Fprintf(w, "denied tools: %d configured\n", len(l.agent.Tools.Deny))
	}
	if discord != nil && discord.DM != nil && discord.DM.GroupEnabled != nil && *discord.DM.GroupEnabled {
		fmt.Fprintln(w, "group direct messages: enabled (each sender still needs the direct-message policy)")
	} else {
		fmt.Fprintln(w, "group direct messages: disabled")
	}
	if l.settings.Tools != nil && l.settings.Tools.Exec != nil && l.settings.Tools.Exec.enabled() {
		limits := resolveExecLimits(l.settings.Tools.Exec)
		fmt.Fprintf(w, "exec tool: owner direct messages only, timeout %s, output cap %d bytes\n", limits.timeout, limits.maxOutput)
	} else {
		fmt.Fprintln(w, "exec tool: disabled")
	}
	fmt.Fprintln(w, "attachments: memory only, Discord CDN only")
	return nil
}

// checkGroupReplyTool rejects a guild or channel filter that would make the
// configured group reply mode impossible on that route.
func checkGroupReplyTool(settings *fileSettings, agent selectedAgent) error {
	guilds := settings.Channels.Discord.Guilds
	for _, guild := range guilds {
		if guild == nil {
			continue
		}
		route := toolPolicy{}
		mergeTools(&route, guild.Tools)
		route = narrowTools(route, agent.Tools)
		if !route.allows("message") {
			return fmt.Errorf("settings: visibleReplies message_tool is disabled for a guild")
		}
		for _, channel := range guild.Channels {
			if channel == nil {
				continue
			}
			channelRoute := narrowPolicy(route, channel.Tools)
			channelRoute = narrowTools(channelRoute, agent.Tools)
			if !channelRoute.allows("message") {
				return fmt.Errorf("settings: visibleReplies message_tool is disabled for a channel")
			}
		}
	}
	return nil
}

func (l *loaded) visibleReplies() string {
	direct, group := replyModes(l.settings)
	if group != "" && group != direct {
		return direct + " (group: " + group + ")"
	}
	return direct
}

func serve(ctx context.Context, l *loaded, stderr io.Writer) error {
	stateDir, err := resolveStateDir(l.options.stateDir)
	if err != nil {
		return err
	}
	state, err := openState(stateDir)
	if err != nil {
		return fmt.Errorf("state: state directory is unavailable")
	}
	defer state.close()
	if err := state.pruneEvents(time.Now().Add(-eventRetention)); err != nil {
		return fmt.Errorf("state: storage is unavailable")
	}
	logger := log.New(stderr, "psionic: ", log.LstdFlags)
	transport, err := newDiscordTransport(l.token, l.settings, logger)
	l.token = ""
	if err != nil {
		return fmt.Errorf("discord: settings are invalid (%T)", err)
	}
	defer transport.close()
	rt := newRuntime(l.settings, l.agent, l.models, l.agentDir, l.authPath, state, l.defaults, transport, logger)
	// Install the transport before connecting so command acknowledgements use
	// the same path as ordinary replies.
	rt.transport = transport
	defer rt.close()
	if err := transport.run(ctx, rt); err != nil {
		return fmt.Errorf("discord: connection failed (%T)", err)
	}
	return nil
}

// checkStateDir refuses a location that overlaps the workspace or known Psi
// session roots. Psionic transcripts must stay independent from other hosts.
// expandHomePath expands a leading ~ in a path.
func expandHomePath(path string) string {
	expanded, err := expandUser(path)
	if err != nil {
		return path
	}
	return expanded
}

// resolveStateDir finds the transcript directory. Precedence: the --state-dir
// flag, then the settings file, then the standard state location.
func resolveStateDir(configured string) (string, error) {
	dir := configured
	if dir == "" {
		dir = psionicStateDir()
	}
	return filepath.Abs(expandHomePath(dir))
}

// checkStateDir refuses a location that overlaps the workspace or known Psi
// session roots. Psionic transcripts must stay independent from other hosts.
// Both the agent directory and the shared agent directory are refused, because
// either one may hold a Psi session root.
func checkStateDir(stateDir, workspace, agentDir, sharedAgentDir string) error {
	workspace = resolvePath(workspace)
	if overlaps(stateDir, workspace) {
		return fmt.Errorf("state directory overlaps the workspace")
	}
	roots := []string{session.AgentDir(), session.SessionsRoot(session.AgentDir())}
	if home, err := os.UserHomeDir(); err == nil {
		// Refuse the conventional location even when an environment override
		// currently points Psi somewhere else.
		roots = append(roots, filepath.Join(home, ".pi", "agent"), filepath.Join(home, ".pi", "agent", "sessions"))
	}
	for _, dir := range []string{agentDir, sharedAgentDir} {
		if dir == "" {
			continue
		}
		roots = append(roots, dir, session.SessionsRoot(dir))
	}
	if env := os.Getenv("PSI_CODING_AGENT_SESSION_DIR"); env != "" {
		roots = append(roots, resolvePath(env))
	}
	if env := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); env != "" {
		roots = append(roots, resolvePath(env))
	}
	for _, root := range roots {
		if overlaps(stateDir, resolvePath(root)) {
			return fmt.Errorf("state directory overlaps another agent's session directory")
		}
	}
	return checkStateContents(stateDir)
}

// checkStateContents refuses an existing non-empty directory unless Psionic
// created it. It is read-only so --check keeps its no-write guarantee.
func checkStateContents(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("state directory is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("state directory is not a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("state directory is unavailable")
	}
	if len(entries) == 0 {
		return nil
	}
	marker := filepath.Join(dir, stateMarker)
	info, err = os.Lstat(marker)
	if os.IsNotExist(err) {
		return fmt.Errorf("state directory is not a Psionic state directory")
	}
	if err != nil {
		return fmt.Errorf("state directory marker is invalid")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxStateMarkerBytes {
		return fmt.Errorf("state directory marker is invalid")
	}
	contents, err := os.ReadFile(marker)
	if err != nil {
		return fmt.Errorf("state directory marker is invalid")
	}
	if string(contents) != stateMarkerContents {
		return fmt.Errorf("state directory marker is invalid")
	}
	return nil
}

func resolvePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(parent, filepath.Base(abs))
	}
	return abs
}

func overlaps(a, b string) bool {
	a, b = resolvePath(a), resolvePath(b)
	if goruntime.GOOS == "windows" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}
