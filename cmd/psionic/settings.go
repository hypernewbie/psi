package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hypernewbie/psi/internal/secretmem"
	"github.com/titanous/json5"
)

// fileSettings is the supported projection of an existing Psionic settings file.
// Pointer fields keep "absent" distinct from false or zero.
type fileSettings struct {
	Agents   *agentsSettings   `json:"agents"`
	Channels *channelsSettings `json:"channels"`
	Messages *messagesSettings `json:"messages"`
	Session  *sessionSettings  `json:"session"`
	Commands *commandsSettings `json:"commands"`
	Secrets  *secretsSettings  `json:"secrets"`
	Tools    *toolPolicy       `json:"tools"`
}

type secretsSettings struct {
	Defaults  secretDefaults             `json:"defaults"`
	Providers map[string]*secretProvider `json:"providers"`
}

type secretDefaults struct {
	Env   string `json:"env"`
	File  string `json:"file"`
	Exec  string `json:"exec"`
	Store string `json:"store"`
}

type secretProvider struct {
	Source            string            `json:"source"`
	Allowlist         []string          `json:"allowlist"`
	Path              string            `json:"path"`
	Mode              string            `json:"mode"`
	Command           string            `json:"command"`
	Args              []string          `json:"args"`
	TrustedDirs       []string          `json:"trustedDirs"`
	PassEnv           []string          `json:"passEnv"`
	Env               map[string]string `json:"env"`
	JSONOnly          *bool             `json:"jsonOnly"`
	TimeoutMs         *int              `json:"timeoutMs"`
	NoOutputTimeoutMs *int              `json:"noOutputTimeoutMs"`
	MaxBytes          *int              `json:"maxBytes"`
	MaxOutputBytes    *int              `json:"maxOutputBytes"`
}

type agentsSettings struct {
	Defaults *agentSettings            `json:"defaults"`
	Entries  map[string]*agentSettings `json:"entries"`
}

type agentSettings struct {
	Workspace string      `json:"workspace"`
	Identity  *identity   `json:"identity"`
	Tools     *toolPolicy `json:"tools"`
}

type identity struct {
	Name   string `json:"name"`
	Theme  string `json:"theme"`
	Emoji  string `json:"emoji"`
	Avatar string `json:"avatar"`
}

type toolPolicy struct {
	Profile   string   `json:"profile"`
	Allow     []string `json:"allow"`
	AlsoAllow []string `json:"alsoAllow"`
	Deny      []string `json:"deny"`
}

type channelsSettings struct {
	Discord *discordSettings `json:"discord"`
}

type discordSettings struct {
	Enabled        *bool                       `json:"enabled"`
	Token          *secretRef                  `json:"token"`
	DMPolicy       string                      `json:"dmPolicy"`
	AllowFrom      stringList                  `json:"allowFrom"`
	GroupPolicy    string                      `json:"groupPolicy"`
	Guilds         map[string]*guildSettings   `json:"guilds"`
	HistoryLimit   *int                        `json:"historyLimit"`
	DMHistoryLimit *int                        `json:"dmHistoryLimit"`
	TextChunkLimit *int                        `json:"textChunkLimit"`
	MaxLines       *int                        `json:"maxLinesPerMessage"`
	ReplyToMode    string                      `json:"replyToMode"`
	SuppressEmbeds *bool                       `json:"suppressEmbeds"`
	Accounts       map[string]*discordSettings `json:"accounts"`
	DefaultAccount string                      `json:"defaultAccount"`
	InboundWorker  *inboundWorker              `json:"inboundWorker"`
}

type inboundWorker struct {
	RunTimeoutMs *int64 `json:"runTimeoutMs"`
}

type guildSettings struct {
	Enabled        *bool                            `json:"enabled"`
	RequireMention *bool                            `json:"requireMention"`
	Users          stringList                       `json:"users"`
	Roles          stringList                       `json:"roles"`
	Tools          *toolPolicy                      `json:"tools"`
	Channels       map[string]*guildChannelSettings `json:"channels"`
	Slug           string                           `json:"slug"`
}

type guildChannelSettings struct {
	Enabled        *bool       `json:"enabled"`
	RequireMention *bool       `json:"requireMention"`
	Users          stringList  `json:"users"`
	Roles          stringList  `json:"roles"`
	Tools          *toolPolicy `json:"tools"`
}

type messagesSettings struct {
	VisibleReplies visibleReplies `json:"visibleReplies"`
	GroupChat      *groupChat     `json:"groupChat"`
}

type visibleReplies string

// UnmarshalJSON accepts the current strings and the older boolean form.
func (v *visibleReplies) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch string(data) {
	case "true":
		*v = "automatic"
		return nil
	case "false":
		*v = "message_tool"
		return nil
	}
	var text string
	if err := json5.Unmarshal(data, &text); err != nil {
		return err
	}
	*v = visibleReplies(text)
	return nil
}

type groupChat struct {
	VisibleReplies visibleReplies `json:"visibleReplies"`
	HistoryLimit   *int           `json:"historyLimit"`
}

type sessionSettings struct {
	DMScope   string                    `json:"dmScope"`
	Reset     *resetSettings            `json:"reset"`
	ByType    *resetByType              `json:"resetByType"`
	ByChannel map[string]*resetSettings `json:"resetByChannel"`
}

type resetByType struct {
	Direct *resetSettings `json:"direct"`
	Group  *resetSettings `json:"group"`
	Thread *resetSettings `json:"thread"`
}

type resetSettings struct {
	Mode        string `json:"mode"`
	AtHour      *int   `json:"atHour"`
	IdleMinutes *int   `json:"idleMinutes"`
}

type commandsSettings struct {
	Native         *nativeSetting `json:"native"`
	OwnerAllowFrom stringList     `json:"ownerAllowFrom"`
}

type nativeSetting struct {
	enabled bool
}

func (n *nativeSetting) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch string(data) {
	case "true", `"auto"`:
		n.enabled = true
		return nil
	case "false", `"off"`:
		n.enabled = false
		return nil
	}
	return fmt.Errorf("commands.native must be true, false, or auto")
}

// stringList accepts the documented string-or-number ID list without rounding.
type stringList []string

func (l *stringList) UnmarshalJSON(data []byte) error {
	decoder := json5.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var values []any
	if err := decoder.Decode(&values); err != nil {
		return err
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		switch v := value.(type) {
		case string:
			out = append(out, v)
		case json5.Number:
			out = append(out, string(v))
		case json.Number:
			out = append(out, string(v))
		default:
			return fmt.Errorf("ID list entries must be strings or numbers")
		}
	}
	*l = out
	return nil
}

// The provider and environment-name grammars are shared by secret references.
var (
	secretProviderName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	secretEnvName      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
)

// secretRef is a literal credential or a provider-backed reference.
type secretRef struct {
	value    string
	env      string
	file     string
	exec     string
	store    string
	provider string
	present  bool
}

func (s *secretRef) UnmarshalJSON(data []byte) error {
	s.present = true
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		if err := json5.Unmarshal(data, &s.value); err != nil {
			return err
		}
		// Whole-value environment shorthand is a secret reference, so it must
		// pass the selected provider's allowlist like the structured form.
		candidate := strings.TrimSpace(s.value)
		if strings.HasPrefix(candidate, "${") && strings.HasSuffix(candidate, "}") {
			candidate = candidate[2 : len(candidate)-1]
		} else if strings.HasPrefix(candidate, "$") {
			candidate = candidate[1:]
		} else {
			return nil
		}
		if secretEnvName.MatchString(candidate) {
			s.env = candidate
			s.value = ""
		}
		return nil
	}
	var object struct {
		Source   string `json:"source"`
		Provider string `json:"provider"`
		ID       string `json:"id"`
	}
	if err := json5.Unmarshal(data, &object); err != nil {
		return err
	}
	switch object.Source {
	case "env":
		if strings.TrimSpace(object.ID) == "" {
			return fmt.Errorf("environment secret needs an id")
		}
		s.env = strings.TrimSpace(object.ID)
		s.provider = object.Provider
	case "file":
		if strings.TrimSpace(object.ID) == "" {
			return fmt.Errorf("file secret needs an id")
		}
		s.file = strings.TrimSpace(object.ID)
		s.provider = object.Provider
	case "exec":
		if strings.TrimSpace(object.ID) == "" {
			return fmt.Errorf("exec secret needs an id")
		}
		s.exec = strings.TrimSpace(object.ID)
		s.provider = object.Provider
	case "store":
		if strings.TrimSpace(object.ID) == "" {
			return fmt.Errorf("store secret needs an id")
		}
		s.store = strings.TrimSpace(object.ID)
		s.provider = object.Provider
	default:
		return fmt.Errorf("unsupported secret source %q", object.Source)
	}
	return nil
}

func (s *secretRef) load(lookupEnv func(string) (string, bool)) (string, error) {
	if s == nil {
		return "", nil
	}
	if s.env != "" {
		value, ok := lookupEnv(s.env)
		if !ok || value == "" {
			return "", fmt.Errorf("environment secret is unavailable")
		}
		return value, nil
	}
	if strings.TrimSpace(s.value) == "" {
		return "", nil
	}
	return substituteConfigEnvironment(s.value, lookupEnv)
}

func (s *secretRef) resolve(lookupEnv func(string) (string, bool)) (string, error) {
	value, err := s.load(lookupEnv)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("empty bot token")
	}
	return value, nil
}

// loadFileSettings reads one bounded settings file with the JSON5 grammar used
// by the existing installation.
func loadFileSettings(path string, lookupEnv func(string) (string, bool)) (*fileSettings, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("settings: settings file is unavailable")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("settings: not a regular file")
	}
	if info.Size() > 4<<20 {
		return nil, fmt.Errorf("settings: file is larger than 4 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("settings: file cannot be read")
	}
	defer secretmem.Wipe(data)
	return decodeSettings(data, lookupEnv)
}

func decodeSettings(data []byte, lookupEnv func(string) (string, bool)) (*fileSettings, error) {
	var included any
	if err := json5.Unmarshal(data, &included); err != nil {
		// Never echo the decoder's input fragment: it can contain credentials.
		return nil, fmt.Errorf("settings: malformed JSON5")
	}
	if containsInclude(included, 0) {
		return nil, fmt.Errorf("settings: $include is not supported")
	}
	var raw fileSettings
	if err := json5.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("settings: malformed JSON5")
	}
	if err := raw.validate(lookupEnv); err != nil {
		return nil, err
	}
	return &raw, nil
}

func containsInclude(value any, depth int) bool {
	if depth > 16 {
		// Fail closed: an include nested beyond the scan depth must not
		// silently disappear into the struct decoder.
		return true
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$include" || containsInclude(child, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsInclude(child, depth+1) {
				return true
			}
		}
	}
	return false
}

func (s *fileSettings) validateEnvTokenProvider() error {
	ref := s.Channels.Discord.Token
	if ref == nil || ref.env == "" {
		return nil
	}
	defaultProvider := "default"
	if s.Secrets != nil && strings.TrimSpace(s.Secrets.Defaults.Env) != "" {
		defaultProvider = strings.TrimSpace(s.Secrets.Defaults.Env)
	}
	provider := ref.provider
	if provider == "" {
		provider = defaultProvider
	}
	if !secretProviderName.MatchString(provider) || !secretEnvName.MatchString(ref.env) {
		return fmt.Errorf("settings: invalid environment secret reference")
	}
	var configured *secretProvider
	if s.Secrets != nil {
		configured = s.Secrets.Providers[provider]
	}
	if configured == nil || configured.Source != "env" {
		if provider == defaultProvider {
			return nil // The default environment provider is built in.
		}
		return fmt.Errorf("settings: environment secret provider is unavailable")
	}
	if configured.Allowlist != nil {
		for _, name := range configured.Allowlist {
			if name == ref.env {
				return nil
			}
		}
		return fmt.Errorf("settings: environment secret is not allowed by its provider")
	}
	return nil
}

func (s *fileSettings) validate(lookupEnv func(string) (string, bool)) error {
	if s.Channels == nil || s.Channels.Discord == nil {
		return fmt.Errorf("settings: channels.discord is required")
	}
	discord := s.Channels.Discord
	if discord.Enabled != nil && !*discord.Enabled {
		return fmt.Errorf("settings: channels.discord is disabled")
	}
	if len(discord.Accounts) > 0 {
		return fmt.Errorf("settings: multiple Discord accounts are not supported")
	}
	if discord.GroupPolicy != "" && discord.GroupPolicy != "allowlist" && discord.GroupPolicy != "open" && discord.GroupPolicy != "disabled" {
		return fmt.Errorf("settings: channels.discord.groupPolicy %q is not supported", discord.GroupPolicy)
	}
	if discord.DMPolicy != "" && discord.DMPolicy != "allowlist" && discord.DMPolicy != "open" && discord.DMPolicy != "disabled" && discord.DMPolicy != dmPolicyPairing {
		return fmt.Errorf("settings: channels.discord.dmPolicy %q is not supported; use allowlist, pairing, open, or disabled", discord.DMPolicy)
	}
	switch discord.DMPolicy {
	case "open":
		if !containsID(discord.AllowFrom, "*") {
			return fmt.Errorf("settings: dmPolicy open requires allowFrom to include *")
		}
	case "allowlist":
		if len(discord.AllowFrom) == 0 {
			return fmt.Errorf("settings: dmPolicy allowlist requires at least one sender ID")
		}
	}
	if discord.Token != nil {
		if err := s.validateSecretTokenReference(lookupEnv); err != nil {
			return fmt.Errorf("settings: channels.discord.token: %w", err)
		}
	}
	switch discord.ReplyToMode {
	case "", "off", "first", "all", "batched":
	default:
		return fmt.Errorf("settings: channels.discord.replyToMode %q is not supported", discord.ReplyToMode)
	}
	if discord.TextChunkLimit != nil && *discord.TextChunkLimit <= 0 {
		return fmt.Errorf("settings: channels.discord.textChunkLimit must be positive")
	}
	if discord.MaxLines != nil && *discord.MaxLines <= 0 {
		return fmt.Errorf("settings: channels.discord.maxLinesPerMessage must be positive")
	}
	if discord.HistoryLimit != nil && *discord.HistoryLimit < 0 {
		return fmt.Errorf("settings: channels.discord.historyLimit must not be negative")
	}
	if discord.DMHistoryLimit != nil && *discord.DMHistoryLimit < 0 {
		return fmt.Errorf("settings: channels.discord.dmHistoryLimit must not be negative")
	}
	if worker := discord.InboundWorker; worker != nil && worker.RunTimeoutMs != nil && *worker.RunTimeoutMs < 0 {
		return fmt.Errorf("settings: channels.discord.inboundWorker.runTimeoutMs must not be negative")
	}
	for _, guild := range discord.Guilds {
		if guild == nil {
			continue
		}
		if err := validatePolicyTools(guild.Tools, "channels.discord.guilds[].tools"); err != nil {
			return err
		}
		for _, channel := range guild.Channels {
			if channel == nil {
				continue
			}
			if err := validatePolicyTools(channel.Tools, "channels.discord.guilds[].channels[].tools"); err != nil {
				return err
			}
		}
	}
	if s.Messages != nil {
		if err := validVisibleReplies(s.Messages.VisibleReplies); err != nil {
			return err
		}
		if s.Messages.GroupChat != nil {
			if err := validVisibleReplies(s.Messages.GroupChat.VisibleReplies); err != nil {
				return err
			}
			if s.Messages.GroupChat.HistoryLimit != nil && *s.Messages.GroupChat.HistoryLimit < 0 {
				return fmt.Errorf("settings: messages.groupChat.historyLimit must not be negative")
			}
		}
	}
	if s.Commands != nil && containsID(s.Commands.OwnerAllowFrom, "*") {
		return fmt.Errorf("settings: commands.ownerAllowFrom must name explicit owners")
	}
	if s.Session != nil {
		switch s.Session.DMScope {
		case "", "main", "per-peer", "per-channel-peer", "per-account-channel-peer":
		default:
			return fmt.Errorf("settings: session.dmScope %q is not supported", s.Session.DMScope)
		}
		if s.Session.DMScope == "main" && discord.DMPolicy != "disabled" {
			if len(discord.AllowFrom) != 1 || normalizeUserID(discord.AllowFrom[0]) == "*" {
				return fmt.Errorf("settings: session.dmScope main needs exactly one direct-message peer")
			}
			peer := normalizeUserID(discord.AllowFrom[0])
			if s.Commands == nil || !containsID(s.Commands.OwnerAllowFrom, peer) {
				return fmt.Errorf("settings: session.dmScope main needs the direct-message peer as an owner")
			}
		}
		if err := validateResetSettings(s.Session.Reset); err != nil {
			return err
		}
		if s.Session.ByType != nil {
			for name, reset := range map[string]*resetSettings{
				"direct": s.Session.ByType.Direct, "group": s.Session.ByType.Group, "thread": s.Session.ByType.Thread,
			} {
				if err := validateResetSettings(reset); err != nil {
					return fmt.Errorf("settings: session.resetByType.%s: %w", name, err)
				}
			}
		}
		for _, reset := range s.Session.ByChannel {
			if err := validateResetSettings(reset); err != nil {
				return fmt.Errorf("settings: session.resetByChannel[]: %w", err)
			}
		}
	}
	if err := validatePolicyTools(s.Tools, "tools"); err != nil {
		return err
	}
	if s.Agents != nil {
		if err := validateTools(s.Agents.Defaults, "agents.defaults.tools"); err != nil {
			return err
		}
		for id, entry := range s.Agents.Entries {
			if err := validateTools(entry, "agents.entries."+id+".tools"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateResetSettings(reset *resetSettings) error {
	if reset == nil {
		return nil
	}
	switch reset.Mode {
	case "", "none", "daily", "idle":
	default:
		return fmt.Errorf("mode %q is not supported", reset.Mode)
	}
	if reset.IdleMinutes != nil && *reset.IdleMinutes < 0 {
		return fmt.Errorf("idleMinutes must not be negative")
	}
	return nil
}

func validateTools(agent *agentSettings, field string) error {
	if agent == nil {
		return nil
	}
	return validatePolicyTools(agent.Tools, field)
}

func validatePolicyTools(tools *toolPolicy, field string) error {
	if tools == nil || tools.Profile == "" {
		return nil
	}
	switch tools.Profile {
	case "minimal", "coding", "messaging", "full":
		return nil
	default:
		return fmt.Errorf("settings: %s.profile %q is not supported", field, tools.Profile)
	}
}

// replyModes returns the direct-message default and the group override.
func replyModes(settings *fileSettings) (direct, group string) {
	direct = "automatic"
	if settings.Messages == nil {
		return direct, ""
	}
	if settings.Messages.VisibleReplies != "" {
		direct = string(settings.Messages.VisibleReplies)
	}
	if settings.Messages.GroupChat != nil {
		group = string(settings.Messages.GroupChat.VisibleReplies)
	}
	return direct, group
}

func validVisibleReplies(value visibleReplies) error {
	switch value {
	case "", "automatic", "message_tool":
		return nil
	default:
		return fmt.Errorf("settings: messages.visibleReplies %q is not supported", value)
	}
}

// selectedAgent is one agent profile with inherited defaults applied.
type selectedAgent struct {
	ID         string
	Workspace  string
	Model      string
	Fallbacks  []string
	Compaction string
	Identity   identity
	Tools      toolPolicy
}

func resolveAgent(s *fileSettings, id string) (selectedAgent, error) {
	var chosen selectedAgent
	chosen.ID = id
	if chosen.ID == "" {
		chosen.ID = "main"
	}
	defaults := (*agentSettings)(nil)
	if s.Agents != nil {
		defaults = s.Agents.Defaults
	}
	entry := (*agentSettings)(nil)
	if id != "" {
		if s.Agents == nil || s.Agents.Entries[id] == nil {
			return selectedAgent{}, fmt.Errorf("agent %q is not configured", id)
		}
		entry = s.Agents.Entries[id]
	} else if s.Agents != nil && len(s.Agents.Entries) == 1 {
		for key, value := range s.Agents.Entries {
			chosen.ID, entry = key, value
		}
	} else if s.Agents != nil && len(s.Agents.Entries) > 1 {
		return selectedAgent{}, fmt.Errorf("multiple agents are configured; select one with --agent")
	}
	mergeTools(&chosen.Tools, s.Tools)
	if defaults != nil {
		chosen.Workspace = defaults.Workspace
		if defaults.Identity != nil {
			chosen.Identity = *defaults.Identity
		}
		mergeTools(&chosen.Tools, defaults.Tools)
	}
	if entry != nil {
		if entry.Workspace != "" {
			chosen.Workspace = entry.Workspace
		}
		if entry.Identity != nil {
			mergeIdentity(&chosen.Identity, *entry.Identity)
		}
		mergeTools(&chosen.Tools, entry.Tools)
	}
	if chosen.ID == "" {
		chosen.ID = "main"
	}
	if chosen.Workspace == "" {
		return chosen, nil
	}
	workspace, err := expandUser(chosen.Workspace)
	if err != nil {
		return selectedAgent{}, fmt.Errorf("workspace: invalid path")
	}
	if workspace, err = filepath.Abs(workspace); err != nil {
		return selectedAgent{}, fmt.Errorf("workspace: invalid path")
	}
	chosen.Workspace = workspace
	return chosen, nil
}

func mergeTools(dst *toolPolicy, src *toolPolicy) {
	if src == nil {
		return
	}
	if src.Profile != "" {
		dst.Profile = src.Profile
	}
	if src.Allow != nil {
		dst.Allow = append([]string(nil), src.Allow...)
	}
	if len(src.AlsoAllow) > 0 {
		dst.AlsoAllow = append(dst.AlsoAllow, src.AlsoAllow...)
	}
	if len(src.Deny) > 0 {
		dst.Deny = append(dst.Deny, src.Deny...)
	}
}

func mergeIdentity(dst *identity, src identity) {
	if src.Name != "" {
		dst.Name = src.Name
	}
	if src.Theme != "" {
		dst.Theme = src.Theme
	}
	if src.Emoji != "" {
		dst.Emoji = src.Emoji
	}
	if src.Avatar != "" {
		dst.Avatar = src.Avatar
	}
}

func expandUser(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// splitModelRef separates provider and model at the first slash, as a model ID
// may itself contain slashes.
func splitModelRef(ref string) (provider, model string, err error) {
	provider, model, ok := strings.Cut(ref, "/")
	if !ok || provider == "" || model == "" {
		return "", "", fmt.Errorf("model %q needs a provider/model reference", ref)
	}
	return provider, model, nil
}
