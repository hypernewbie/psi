// Package config loads the narrow supported subset (Phase 4).
//
// Supported keys (COMPATIBILITY.md): defaultProvider, defaultModel,
// defaultThinkingLevel, sessionDir, defaultTools, skills, enableSkillCommands,
// compaction{enabled,reserveTokens,keepRecentTokens}, shellPath,
// shellCommandPrefix, network timeouts/retries, instructions, models.json
// endpoint metadata, auth env refs, quietStartup. Everything else is
// ignored (harmless UI prefs) or rejected (active providers/credentials).
package config

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"psi/internal/modelmeta"
	"psi/internal/providerregistry"
)

// Thinking levels in the supported mapping.
var ValidThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func numericSetting(value any) (float64, bool) {
	f, ok := value.(float64)
	return f, ok && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func clampDisplaySetting(value float64, minValue, maxValue int) int {
	if value <= float64(minValue) {
		return minValue
	}
	if value >= float64(maxValue) {
		return maxValue
	}
	return int(math.Floor(value))
}

func validThinking(s string) bool {
	for _, v := range ValidThinkingLevels {
		if v == s {
			return true
		}
	}
	return false
}

// Compaction settings subset.
type CompactionConfig struct {
	Enabled          bool
	ReserveTokens    int
	KeepRecentTokens int
}

// Settings is the supported configuration subset with resolution bases.
type Settings struct {
	Theme                   string
	ThemePaths              []string
	UserThemePaths          []string
	ProjectThemePaths       []string
	TerminalTrueColor       string // "auto", "true", or "false"
	EditorPaddingX          int
	OutputPad               int
	AutocompleteMaxVisible  int
	MarkdownCodeBlockIndent string
	DefaultProvider         string
	DefaultModel            string
	DefaultThinking         string
	SessionDir              string
	SessionDirSource        string // "cli"|"env"|"settings"|"default"
	DefaultTools            []string
	Skills                  []string
	EnableSkills            bool
	Compaction              CompactionConfig
	ShellPath               string
	ShellPrefix             string
	HTTPIdleTimeoutMs       int64 // milliseconds; 0 or -1 disables the idle timer
	MaxRetries              int
	QuietStartup            bool
	// Diagnostics for skipped unsupported resources.
	Warnings []string
}

func defaultSettings() Settings {
	return Settings{
		Theme:                   "system",
		TerminalTrueColor:       "auto",
		OutputPad:               1,
		AutocompleteMaxVisible:  5,
		MarkdownCodeBlockIndent: "  ",
		DefaultThinking:         "off",
		DefaultTools:            []string{"read", "bash", "edit", "write"},
		EnableSkills:            true,
		Compaction:              CompactionConfig{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000},
		MaxRetries:              3,
		HTTPIdleTimeoutMs:       300000,
	}
}

// rawSettings mirrors the JSON shape for supported keys; unknown keys are ignored.
type rawSettings struct {
	Theme    *string  `json:"theme"`
	Themes   []string `json:"themes"`
	Terminal *struct {
		TrueColor any `json:"trueColor"`
	} `json:"terminal"`
	EditorPaddingX         any `json:"editorPaddingX"`
	OutputPad              any `json:"outputPad"`
	AutocompleteMaxVisible any `json:"autocompleteMaxVisible"`
	Markdown               *struct {
		CodeBlockIndent *string `json:"codeBlockIndent"`
	} `json:"markdown"`
	DefaultProvider     *string  `json:"defaultProvider"`
	DefaultModel        *string  `json:"defaultModel"`
	DefaultThinking     *string  `json:"defaultThinkingLevel"`
	SessionDir          *string  `json:"sessionDir"`
	DefaultTools        []string `json:"defaultTools"`
	Skills              any      `json:"skills"`
	EnableSkillCommands *bool    `json:"enableSkillCommands"`
	Compaction          *struct {
		Enabled          *bool `json:"enabled"`
		ReserveTokens    *int  `json:"reserveTokens"`
		KeepRecentTokens *int  `json:"keepRecentTokens"`
	} `json:"compaction"`
	ShellPath          *string `json:"shellPath"`
	ShellCommandPrefix *string `json:"shellCommandPrefix"`
	HTTPIdleTimeoutMs  any     `json:"httpIdleTimeoutMs"`
	QuietStartup       any     `json:"quietStartup"`
	MaxRetries         *int    `json:"maxRetries"`
	Retry              *struct {
		Enabled    *bool `json:"enabled"`
		MaxRetries *int  `json:"maxRetries"`
	} `json:"retry"`
}

// LoadResult carries merged settings plus per-file diagnostics.
type LoadResult struct {
	Settings Settings
	// Sources records which file provided each scalar (for precedence tests).
	Sources map[string]string
}

// LoadSettings reads user settings.json then project .pi/settings.json.
// Project overrides user for supported scalars; defaultTools uses Pi's
// replacement and +/- semantics; compaction merges per-field.
func LoadSettings(userPath, projectPath string, projectConsent bool) (LoadResult, error) {
	merged := defaultSettings()
	sources := map[string]string{}
	if data, err := readJSONFile(userPath); err == nil {
		if err := applyRaw(data, &merged, sources, "user:"+userPath); err != nil {
			return LoadResult{}, fmt.Errorf("%s: %w", userPath, err)
		}
	} else if !os.IsNotExist(err) {
		return LoadResult{}, fmt.Errorf("%s: %w", userPath, err)
	}
	if projectConsent {
		if data, err := readJSONFile(projectPath); err == nil {
			if err := applyRaw(data, &merged, sources, "project:"+projectPath); err != nil {
				return LoadResult{}, fmt.Errorf("%s: %w", projectPath, err)
			}
		} else if !os.IsNotExist(err) {
			return LoadResult{}, fmt.Errorf("%s: %w", projectPath, err)
		}
	}
	if merged.DefaultThinking != "" && !validThinking(merged.DefaultThinking) {
		return LoadResult{}, fmt.Errorf("unsupported defaultThinkingLevel %q", merged.DefaultThinking)
	}
	return LoadResult{Settings: merged, Sources: sources}, nil
}

func readJSONFile(path string) (map[string]any, error) {
	if path == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Strip BOM like Pi.
	b = stripBOM(b)
	if !json.Valid(b) {
		return nil, fmt.Errorf("malformed JSON")
	}
	var v map[string]any
	dec := json.NewDecoder(bytesReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("malformed JSON: %w", err)
	}
	return v, nil
}

func resolveConfiguredResourcePath(base, input string) string {
	prefix, path := "", input
	if len(path) > 0 && strings.ContainsRune("!+-", rune(path[0])) {
		prefix, path = path[:1], path[1:]
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			}
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return prefix + filepath.Clean(path)
}

func applyRaw(data map[string]any, out *Settings, sources map[string]string, src string) error {
	// Re-marshal through rawSettings for typed validation of supported keys.
	b, _ := json.Marshal(data)
	var raw rawSettings
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("malformed values: %w", err)
	}
	set := func(key, val string) {
		if val != "" {
			sources[key] = src
		}
	}
	if _, present := data["theme"]; present && raw.Theme == nil {
		return fmt.Errorf("theme must be a string")
	}
	if raw.Theme != nil {
		out.Theme = *raw.Theme
		sources["theme"] = src
	}
	if _, present := data["themes"]; present && raw.Themes == nil {
		return fmt.Errorf("themes must be an array of paths")
	}
	if raw.Themes != nil {
		base := filepath.Dir(strings.SplitN(src, ":", 2)[1])
		for _, path := range raw.Themes {
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("themes entries must not be empty")
			}
			resolved := resolveConfiguredResourcePath(base, path)
			out.ThemePaths = append(out.ThemePaths, resolved)
			if strings.HasPrefix(src, "project:") {
				out.ProjectThemePaths = append(out.ProjectThemePaths, resolved)
			} else {
				out.UserThemePaths = append(out.UserThemePaths, resolved)
			}
		}
		sources["themes"] = src
	}
	if _, present := data["terminal"]; present && raw.Terminal == nil {
		return fmt.Errorf("terminal must be an object")
	}
	if raw.Terminal != nil {
		terminalSettings, _ := data["terminal"].(map[string]any)
		_, hasTrueColor := terminalSettings["trueColor"]
		if hasTrueColor && raw.Terminal.TrueColor == nil {
			return fmt.Errorf("terminal.trueColor must be boolean or \"auto\"")
		}
	}
	if raw.Terminal != nil && raw.Terminal.TrueColor != nil {
		switch value := raw.Terminal.TrueColor.(type) {
		case bool:
			out.TerminalTrueColor = strconv.FormatBool(value)
		case string:
			if value != "auto" {
				return fmt.Errorf("terminal.trueColor must be boolean or \"auto\"")
			}
			out.TerminalTrueColor = value
		default:
			return fmt.Errorf("terminal.trueColor must be boolean or \"auto\"")
		}
		sources["terminal.trueColor"] = src
	}
	if _, present := data["editorPaddingX"]; present {
		value, ok := numericSetting(raw.EditorPaddingX)
		if !ok {
			return fmt.Errorf("editorPaddingX must be a number")
		}
		out.EditorPaddingX = clampDisplaySetting(value, 0, 3)
		sources["editorPaddingX"] = src
	}
	if _, present := data["outputPad"]; present {
		value, ok := numericSetting(raw.OutputPad)
		if !ok {
			return fmt.Errorf("outputPad must be a number")
		}
		out.OutputPad = 1
		if value == 0 {
			out.OutputPad = 0
		}
		sources["outputPad"] = src
	}
	if _, present := data["autocompleteMaxVisible"]; present {
		value, ok := numericSetting(raw.AutocompleteMaxVisible)
		if !ok {
			return fmt.Errorf("autocompleteMaxVisible must be a number")
		}
		out.AutocompleteMaxVisible = clampDisplaySetting(value, 3, 20)
		sources["autocompleteMaxVisible"] = src
	}
	if _, present := data["markdown"]; present && raw.Markdown == nil {
		return fmt.Errorf("markdown must be an object")
	}
	if raw.Markdown != nil {
		markdownSettings, _ := data["markdown"].(map[string]any)
		if _, present := markdownSettings["codeBlockIndent"]; present && raw.Markdown.CodeBlockIndent == nil {
			return fmt.Errorf("markdown.codeBlockIndent must be a string")
		}
		if raw.Markdown.CodeBlockIndent != nil {
			out.MarkdownCodeBlockIndent = *raw.Markdown.CodeBlockIndent
			sources["markdown.codeBlockIndent"] = src
		}
	}
	if raw.DefaultProvider != nil {
		out.DefaultProvider = *raw.DefaultProvider
		set("defaultProvider", *raw.DefaultProvider)
	}
	if raw.DefaultModel != nil {
		out.DefaultModel = *raw.DefaultModel
		set("defaultModel", *raw.DefaultModel)
	}
	if raw.DefaultThinking != nil {
		out.DefaultThinking = *raw.DefaultThinking
		set("defaultThinkingLevel", *raw.DefaultThinking)
	}
	if raw.SessionDir != nil {
		out.SessionDir = *raw.SessionDir
		out.SessionDirSource = "settings"
		set("sessionDir", *raw.SessionDir)
	}
	if raw.DefaultTools != nil {
		tools, err := ResolveTools(raw.DefaultTools, out.DefaultTools)
		if err != nil {
			return err
		}
		out.DefaultTools = tools
		set("defaultTools", strings.Join(tools, ","))
	}
	if raw.Skills != nil {
		skills, err := parseSkillsField(raw.Skills)
		if err != nil {
			return fmt.Errorf("skills: %w", err)
		}
		// Resolve each source before combining resource lists.
		base := filepath.Dir(strings.SplitN(src, ":", 2)[1])
		for _, path := range skills {
			prefix := ""
			if strings.HasPrefix(path, "+") || strings.HasPrefix(path, "-") || strings.HasPrefix(path, "!") {
				prefix, path = path[:1], path[1:]
			}
			if strings.HasPrefix(path, "~/") {
				home, _ := os.UserHomeDir()
				path = filepath.Join(home, path[2:])
			} else if !filepath.IsAbs(path) {
				path = filepath.Join(base, path)
			}
			out.Skills = append(out.Skills, prefix+path)
		}
		set("skills", strings.Join(skills, ","))
	}
	if raw.EnableSkillCommands != nil {
		out.EnableSkills = *raw.EnableSkillCommands
		set("enableSkillCommands", "set")
	}
	if raw.Compaction != nil {
		if raw.Compaction.Enabled != nil {
			out.Compaction.Enabled = *raw.Compaction.Enabled
			set("compaction.enabled", "set")
		}
		if raw.Compaction.ReserveTokens != nil {
			if *raw.Compaction.ReserveTokens < 0 {
				return fmt.Errorf("compaction.reserveTokens must be >= 0")
			}
			out.Compaction.ReserveTokens = *raw.Compaction.ReserveTokens
			set("compaction.reserveTokens", "set")
		}
		if raw.Compaction.KeepRecentTokens != nil {
			if *raw.Compaction.KeepRecentTokens < 0 {
				return fmt.Errorf("compaction.keepRecentTokens must be >= 0")
			}
			out.Compaction.KeepRecentTokens = *raw.Compaction.KeepRecentTokens
			set("compaction.keepRecentTokens", "set")
		}
	}
	if raw.ShellPath != nil {
		out.ShellPath = *raw.ShellPath
		set("shellPath", *raw.ShellPath)
	}
	if raw.ShellCommandPrefix != nil {
		out.ShellPrefix = *raw.ShellCommandPrefix
		set("shellCommandPrefix", *raw.ShellCommandPrefix)
	}
	if raw.HTTPIdleTimeoutMs != nil {
		switch v := raw.HTTPIdleTimeoutMs.(type) {
		case float64:
			if v < 0 || v > 86400000 || v != float64(int64(v)) {
				return fmt.Errorf("httpIdleTimeoutMs must be an integer from 0 to 86400000")
			}
			out.HTTPIdleTimeoutMs = int64(v)
		case string:
			if v == "disabled" {
				out.HTTPIdleTimeoutMs = -1
			} else {
				return fmt.Errorf("httpIdleTimeoutMs must be a number or \"disabled\"")
			}
		default:
			return fmt.Errorf("httpIdleTimeoutMs must be a number or \"disabled\"")
		}
		set("httpIdleTimeoutMs", "set")
	}
	if raw.QuietStartup != nil {
		switch v := raw.QuietStartup.(type) {
		case bool:
			out.QuietStartup = v
		case string:
			out.QuietStartup = (v == "header" || v == "true")
		default:
			return fmt.Errorf("quietStartup must be boolean or \"header\"")
		}
		set("quietStartup", "set")
	}
	if raw.MaxRetries != nil {
		out.MaxRetries = *raw.MaxRetries
	}
	if raw.Retry != nil {
		if raw.Retry.MaxRetries != nil {
			out.MaxRetries = *raw.Retry.MaxRetries
		}
		if raw.Retry.Enabled != nil && !*raw.Retry.Enabled {
			out.MaxRetries = 0
		}
	}
	if out.MaxRetries < 0 || out.MaxRetries > 10 {
		return fmt.Errorf("retry.maxRetries must be between 0 and 10")
	}
	return nil
}

// resolveTools implements Pi defaultTools semantics: plain names replace the
// inherited selection; +name/-name add or remove.
func resolveTools(list []string) ([]string, error) {
	return ResolveTools(list, []string{"read", "bash", "edit", "write"})
}

func ResolveTools(list, inherited []string) ([]string, error) {
	if len(list) == 0 {
		return []string{}, nil
	}
	known := map[string]bool{"read": true, "bash": true, "edit": true, "write": true}
	hasPlain := false
	for _, t := range list {
		if t != "" && t[0] != '+' && t[0] != '-' {
			hasPlain = true
			break
		}
	}
	var base []string
	if hasPlain {
		for _, name := range list {
			if name != "" && name[0] != '+' && name[0] != '-' {
				base = append(base, name)
			}
		}
	} else {
		base = inherited
	}
	set := map[string]bool{}
	for _, t := range base {
		set[t] = true
	}
	for _, t := range list {
		switch {
		case t == "":
			return nil, fmt.Errorf("empty tool name")
		case t[0] == '+':
			name := t[1:]
			if !known[name] {
				return nil, fmt.Errorf("unknown tool %q", name)
			}
			set[name] = true
		case t[0] == '-':
			name := t[1:]
			if !known[name] {
				return nil, fmt.Errorf("unknown tool %q", name)
			}
			delete(set, name)
		default:
			if !known[t] {
				return nil, fmt.Errorf("unknown tool %q", t)
			}
			// Plain names formed the base before modifiers were applied.
		}
	}
	var out []string
	for _, k := range []string{"read", "bash", "edit", "write"} {
		if set[k] {
			out = append(out, k)
		}
	}
	// Preserve canonical order; empty means --no-tools.
	if hasPlain && len(out) == 0 {
		return []string{}, nil
	}
	return out, nil
}

func parseSkillsField(v any) ([]string, error) {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("skills must be strings")
			}
			out = append(out, s)
		}
		return out, nil
	case []string:
		return t, nil
	default:
		return nil, fmt.Errorf("skills must be an array of strings")
	}
}

// StartupOverrides resolves CLI > session-restored > settings > defaults.
// Returns the effective provider/model/thinking/sessionDir and a concise
// explanation when an explicit value conflicts.
type StartupOverrides struct {
	Provider   string
	Model      string
	Thinking   string
	SessionDir string
	NoSession  bool
	Explain    string
}

// ResolveStartup implements precedence with conflict explanation.
func ResolveStartup(cliProvider, cliModel, cliThinking, cliSessionDir string, cliNoSession bool, restoredProvider, restoredModel, restoredThinking string, settings Settings, envSessionDir string) StartupOverrides {
	var notes []string
	provider := settings.DefaultProvider
	if restoredProvider != "" {
		provider = restoredProvider
	}
	if cliProvider != "" {
		if restoredProvider != "" && restoredProvider != cliProvider {
			notes = append(notes, fmt.Sprintf("CLI --provider %q overrides restored %q", cliProvider, restoredProvider))
		}
		provider = cliProvider
	}
	model := settings.DefaultModel
	if restoredModel != "" {
		model = restoredModel
	}
	if cliModel != "" {
		if restoredModel != "" && restoredModel != cliModel {
			notes = append(notes, fmt.Sprintf("CLI --model %q overrides restored %q", cliModel, restoredModel))
		}
		model = cliModel
	}
	thinking := settings.DefaultThinking
	if restoredThinking != "" {
		thinking = restoredThinking
	}
	if cliThinking != "" {
		if restoredThinking != "" && restoredThinking != cliThinking {
			notes = append(notes, fmt.Sprintf("CLI --thinking %q overrides restored %q", cliThinking, restoredThinking))
		}
		thinking = cliThinking
	}
	sessionDir := settings.SessionDir
	if settings.SessionDir != "" {
		sessionDir = settings.SessionDir
	}
	if envSessionDir != "" {
		sessionDir = envSessionDir
		notes = append(notes, "session-dir from environment")
	}
	if cliSessionDir != "" {
		if sessionDir != "" && sessionDir != cliSessionDir {
			notes = append(notes, fmt.Sprintf("CLI --session-dir %q overrides %q", cliSessionDir, sessionDir))
		}
		sessionDir = cliSessionDir
	}
	sort.Strings(notes)
	return StartupOverrides{Provider: provider, Model: model, Thinking: thinking, SessionDir: sessionDir, NoSession: cliNoSession, Explain: strings.Join(notes, "; ")}
}

// ResolveCredential supports literal and environment-reference forms.
// Supported: "text", "$VAR", "${VAR}", mixed "prefix-$VAR-suffix" with $$
// escaping. Rejects command values ("!cmd") unless explicitly enabled (never
// in the first release): returns an error instead of executing.
func ResolveCredential(raw string) (string, error) {
	return ResolveCredentialWithEnv(raw, os.LookupEnv)
}

func ResolveCredentialWithEnv(raw string, lookupEnv func(string) (string, bool)) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("empty credential")
	}
	if strings.HasPrefix(raw, "!") {
		return "", fmt.Errorf("credential commands are not supported (value starts with '!'); use env references")
	}
	var sb strings.Builder
	i := 0
	for i < len(raw) {
		c := raw[i]
		if c == '$' {
			if i+1 < len(raw) && raw[i+1] == '!' {
				sb.WriteByte('!')
				i += 2
				continue
			}
			if i+1 < len(raw) && raw[i+1] == '$' {
				sb.WriteByte('$')
				i += 2
				continue
			}
			if i+1 < len(raw) && raw[i+1] == '{' {
				end := strings.IndexByte(raw[i+2:], '}')
				if end < 0 {
					return "", fmt.Errorf("unterminated ${ in credential")
				}
				name := raw[i+2 : i+2+end]
				if !validEnvName(name) {
					return "", fmt.Errorf("invalid env name %q", name)
				}
				v, ok := lookupEnv(name)
				if !ok {
					return "", fmt.Errorf("env %s is not set", name)
				}
				sb.WriteString(v)
				i += 2 + end + 1
				continue
			}
			// $VAR form.
			j := i + 1
			for j < len(raw) && isEnvChar(raw[j], j == i+1) {
				j++
			}
			if j == i+1 {
				sb.WriteByte('$')
				i++
				continue
			}
			name := raw[i+1 : j]
			v, ok := lookupEnv(name)
			if !ok {
				return "", fmt.Errorf("env %s is not set", name)
			}
			sb.WriteString(v)
			i = j
			continue
		}
		sb.WriteByte(c)
		i++
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("resolved credential is empty")
	}
	return sb.String(), nil
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isEnvChar(s[i], i == 0) {
			return false
		}
	}
	return true
}

func isEnvChar(c byte, first bool) bool {
	if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
		return true
	}
	if !first && c >= '0' && c <= '9' {
		return true
	}
	return false
}

// RedactCredential redacts secrets in errors (keys, tokens, headers).
func RedactCredential(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}

// ModelMeta follows Pi's provider.models[] shape, not a private top-level catalog.
type ModelCompat = modelmeta.Compat

type ModelMeta struct {
	Compat                modelmeta.Compat   `json:"compat"`
	ID                    string             `json:"id"`
	Name                  string             `json:"name,omitempty"`
	API                   string             `json:"api"`
	BaseURL               string             `json:"baseUrl"`
	ContextWindow         int                `json:"contextWindow"`
	MaxTokens             int                `json:"maxTokens"`
	Reasoning             bool               `json:"reasoning"`
	Input                 []string           `json:"input"`
	Headers               map[string]string  `json:"headers"`
	InputImage            bool               `json:"-"`
	APIKey                string             `json:"-"`
	UnavailableReason     string             `json:"-"`
	CacheEndpointRejected bool               `json:"-"`
	ThinkingLevelMap      map[string]*string `json:"thinkingLevelMap"`
}

type ModelsFile struct {
	Providers    map[string]ProviderMeta `json:"providers"`
	cachedModels map[string]map[string]ModelMeta
}

type ProviderMeta struct {
	API            string                     `json:"api"`
	BaseURL        string                     `json:"baseUrl"`
	APIKey         string                     `json:"apiKey"`
	Headers        map[string]string          `json:"headers"`
	Compat         modelmeta.Compat           `json:"compat"`
	Models         []ModelMeta                `json:"models"`
	ModelOverrides map[string]json.RawMessage `json:"modelOverrides"`
}

func LoadModels(path string) (*ModelsFile, error) {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var mf ModelsFile
	if err == nil {
		if err := json.Unmarshal(stripBOM(b), &mf); err != nil {
			return nil, fmt.Errorf("%s: malformed models.json", path)
		}
	}
	mf.cachedModels = loadPiModelCache(filepath.Join(filepath.Dir(path), "models-store.json"))
	return &mf, nil
}

// SelectModel resolves only the active provider. Unrelated Pi provider entries
// can remain in the shared file without preventing supported providers' use.
func (mf *ModelsFile) SelectModel(providerID, id string) (ModelMeta, error) {
	spec, ok := providerregistry.Lookup(providerID)
	if !ok {
		return ModelMeta{}, fmt.Errorf("unsupported provider %q: available providers are %s", providerID, strings.Join(providerregistry.IDs(), ", "))
	}
	if id == "" {
		switch providerID {
		case "anthropic":
			id = "claude-sonnet-4-5"
		case "openai-codex":
			id = "gpt-5.6-luna"
		default:
			return ModelMeta{}, fmt.Errorf("provider %q needs an explicit model selection", providerID)
		}
	}

	m, known := bundledModel(providerID, id)
	if providerID == "anthropic" && !known {
		m = ModelMeta{ID: id, API: "anthropic-messages", ContextWindow: 200000, MaxTokens: 16384, Reasoning: true, Input: []string{"text", "image"}, InputImage: true}
		known = id == "claude-opus-4-1" || id == "claude-sonnet-4-0" || id == "claude-opus-4-0"
	}
	if providerID == "openai-codex" && !known {
		m = ModelMeta{ID: id, API: "openai-codex-responses"}
	}
	if cached, ok := mf.cachedModels[providerID][id]; ok {
		m, known = cached, true
	}

	p := mf.Providers[providerID]
	configuredModelBase := ""
	for _, configured := range p.Models {
		if configured.ID != id {
			continue
		}
		m, known = configured, true
		m.InputImage = containsString(configured.Input, "image")
		configuredModelBase = configured.BaseURL
		break
	}
	if !known {
		return ModelMeta{}, fmt.Errorf("model %q needs explicit metadata in models.json providers.%s.models", id, providerID)
	}
	cacheEndpointRejected := m.CacheEndpointRejected
	catalogBaseURL := m.BaseURL
	modelCompat := m.Compat
	if override, exists := p.ModelOverrides[id]; exists {
		var overrideObject map[string]json.RawMessage
		if err := json.Unmarshal(override, &overrideObject); err != nil || overrideObject == nil {
			return ModelMeta{}, fmt.Errorf("invalid metadata override for model %q", id)
		}
		var overrideCompat modelmeta.Compat
		if raw, hasCompat := overrideObject["compat"]; hasCompat {
			if err := json.Unmarshal(raw, &overrideCompat); err != nil {
				return ModelMeta{}, fmt.Errorf("invalid compatibility override for model %q", id)
			}
		}
		if err := json.Unmarshal(override, &m); err != nil {
			return ModelMeta{}, fmt.Errorf("invalid metadata override for model %q", id)
		}
		m.Compat = mergeCompat(modelCompat, overrideCompat)
		if m.Input != nil {
			m.InputImage = containsString(m.Input, "image")
		}
	}
	m.Compat = mergeCompat(p.Compat, m.Compat)
	if m.API == "" {
		m.API = p.API
	}
	if m.API == "" {
		m.API = spec.DefaultAPI
	}
	if !providerregistry.SupportsAPI(providerID, m.API) {
		return ModelMeta{}, fmt.Errorf("model %q unavailable: provider %q does not support API %q", id, providerID, m.API)
	}
	if m.UnavailableReason != "" {
		if !cacheEndpointRejected || (p.BaseURL == "" && configuredModelBase == "" && m.BaseURL == catalogBaseURL) {
			return ModelMeta{}, fmt.Errorf("model %q unavailable: %s", id, m.UnavailableReason)
		}
		m.UnavailableReason = ""
		m.CacheEndpointRejected = false
	}
	if m.ContextWindow <= 0 {
		m.ContextWindow = 128000
	}
	if m.MaxTokens <= 0 {
		m.MaxTokens = 16384
	}
	m.ID = id
	if m.Name == "" {
		m.Name = id
	}

	m.BaseURL = catalogBaseURL
	explicitModelOverrideBase := false
	if p.BaseURL != "" {
		m.BaseURL = p.BaseURL
	}
	if configuredModelBase != "" {
		m.BaseURL = configuredModelBase
	}
	if override, exists := p.ModelOverrides[id]; exists {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(override, &fields)
		if raw, present := fields["baseUrl"]; present {
			var explicit string
			if err := json.Unmarshal(raw, &explicit); err != nil {
				return ModelMeta{}, fmt.Errorf("invalid baseUrl override for model %q", id)
			}
			if explicit != "" {
				m.BaseURL = explicit
				explicitModelOverrideBase = true
			}
		}
	}
	if m.BaseURL == "" {
		m.BaseURL = spec.Endpoints[m.API]
	}
	if providerID == "opencode-go" && p.BaseURL == "" && configuredModelBase == "" && !explicitModelOverrideBase {
		if !providerregistry.TrustedCachedEndpoint(providerID, m.API, m.BaseURL) {
			return ModelMeta{}, fmt.Errorf("model %q unavailable: public OpenCode endpoint is not in the reviewed API path allowlist", id)
		}
	}
	m.APIKey = p.APIKey
	headers := make(map[string]string)
	for k, v := range p.Headers {
		headers[k] = v
	}
	for k, v := range m.Headers {
		headers[k] = v
	}
	m.Headers = headers
	return m, nil
}

func mergeCompat(base, override modelmeta.Compat) modelmeta.Compat {
	if len(base)+len(override) == 0 {
		return nil
	}
	merged := make(modelmeta.Compat, len(base)+len(override))
	for key, value := range base {
		merged[key] = append(json.RawMessage(nil), value...)
	}
	for key, value := range override {
		merged[key] = append(json.RawMessage(nil), value...)
	}
	return merged
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Instruction discovery (Phase 4 Step 37).
// Order: broad scope -> workspace scope; AGENTS.override.md wins over
// AGENTS.md; supported fallbacks; SYSTEM.md replaces, APPEND_SYSTEM.md adds.
// Project files require consent.

// InstructionFile is one discovered instruction file.
type InstructionFile struct {
	Path    string
	Content string
	Kind    string // "agents"|"agents-override"|"system"|"append-system"|"fallback"
}

// DiscoverInstructions returns the deterministic sequence without duplicates.
func DiscoverInstructions(cwd, agentDir string, projectConsent bool) ([]InstructionFile, error) {
	var out []InstructionFile
	seen := map[string]bool{}
	add := func(path, kind string) (bool, error) {
		if seen[path] {
			return true, nil
		}
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("instruction file %s: %w", path, err)
		}
		seen[path] = true
		out = append(out, InstructionFile{Path: path, Content: string(stripBOM(b)), Kind: kind})
		return true, nil
	}
	// One winning context file per directory, including each ancestor.
	for _, dir := range append([]string{agentDir}, ancestorDirs(cwd)...) {
		if dir == "" {
			continue
		}
		for _, name := range []string{"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"} {
			kind := "agents"
			if name == "AGENTS.override.md" {
				kind = "agents-override"
			}
			found, err := add(filepath.Join(dir, name), kind)
			if err != nil {
				return nil, err
			}
			if found {
				break
			}
		}
	}
	for _, spec := range []struct{ name, kind string }{{"SYSTEM.md", "system"}, {"APPEND_SYSTEM.md", "append-system"}} {
		path := filepath.Join(agentDir, spec.name)
		if projectConsent {
			project := filepath.Join(cwd, ".pi", spec.name)
			if _, err := os.Stat(project); err == nil {
				path = project
			}
		}
		if _, err := add(path, spec.kind); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func ancestorDirs(cwd string) []string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	var chain []string
	cur := abs
	for {
		chain = append(chain, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	// Reverse to broad->specific, excluding duplicates.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// AssemblePrompt builds the effective system prompt: short psi base for the
// four tools + instructions + workspace + skill descriptions. Deterministic.
func AssemblePrompt(cwd string, instructions []InstructionFile, skillDescs []string, tools []string) string {
	var sb strings.Builder
	base := "You are psi, a minimal Go coding agent. Use the declared tools to inspect and change the workspace. Show file paths clearly."
	for _, ins := range instructions {
		if ins.Kind == "system" {
			base = ins.Content
		}
	}
	sb.WriteString(base + "\n")
	sb.WriteString("Available tools: " + strings.Join(tools, ", ") + ".\n")
	for _, ins := range instructions {
		if ins.Kind != "system" {
			sb.WriteString("\n--- " + ins.Path + " ---\n" + ins.Content + "\n")
		}
	}
	for _, d := range skillDescs {
		if d != "" {
			sb.WriteString("\n" + d + "\n")
		}
	}
	sb.WriteString("\nWorkspace: " + cwd + "\n")
	return sb.String()
}
