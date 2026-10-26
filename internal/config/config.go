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
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Thinking levels in the supported mapping.
var ValidThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

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
	DefaultProvider   string
	DefaultModel      string
	DefaultThinking   string
	SessionDir        string
	SessionDirSource  string // "cli"|"env"|"settings"|"default"
	DefaultTools      []string
	Skills            []string
	EnableSkills      bool
	Compaction        CompactionConfig
	ShellPath         string
	ShellPrefix       string
	HTTPIdleTimeoutMs int64 // 0 = default, -1 = disabled
	MaxRetries        int
	QuietStartup      bool
	// Diagnostics for skipped unsupported resources.
	Warnings []string
}

func defaultSettings() Settings {
	return Settings{
		DefaultThinking: "off",
		DefaultTools:    []string{"read", "bash", "edit", "write"},
		EnableSkills:    true,
		Compaction:      CompactionConfig{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000},
		MaxRetries:      3,
	}
}

// rawSettings mirrors the JSON shape for supported keys; unknown keys are ignored.
type rawSettings struct {
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
	if merged.DefaultProvider != "" && merged.DefaultProvider != "anthropic" {
		return LoadResult{}, fmt.Errorf("unsupported provider %q: first release supports anthropic only", merged.DefaultProvider)
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
	var v map[string]any
	dec := json.NewDecoder(bytesReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("malformed JSON: %w", err)
	}
	return v, nil
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
		tools, err := resolveTools(raw.DefaultTools)
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
		// Project skills replace user skills when project consent is on and
		// the project file declares the key (Pi merges selected resource lists;
		// for skills the explicit project list wins).
		if strings.HasPrefix(src, "project:") || out.Skills == nil {
			out.Skills = skills
		} else {
			out.Skills = skills
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
		if *raw.MaxRetries < 0 {
			return fmt.Errorf("maxRetries must be >= 0")
		}
		out.MaxRetries = *raw.MaxRetries
		set("maxRetries", "set")
	}
	return nil
}

// resolveTools implements Pi defaultTools semantics: plain names replace the
// inherited selection; +name/-name add or remove.
func resolveTools(list []string) ([]string, error) {
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
		base = nil
	} else {
		base = []string{"read", "bash", "edit", "write"}
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
			set[t] = true
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

func lookupEnv(name string) (string, bool) { return os.LookupEnv(name) }

// RedactCredential redacts secrets in errors (keys, tokens, headers).
func RedactCredential(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}

// ModelMeta is the selected models.json subset for one model.
type ModelMeta struct {
	ID            string
	API           string
	BaseURL       string
	ContextWindow int
	MaxTokens     int
	Reasoning     bool
	InputImage    bool
}

// ModelsFile is the endpoint/model subset (no catalog refresh).
type ModelsFile struct {
	Providers map[string]ProviderMeta `json:"providers"`
	Models    map[string]ModelMeta    `json:"models"`
}

type ProviderMeta struct {
	API     string `json:"api"`
	BaseURL string `json:"baseUrl"`
}

// LoadModels loads local metadata without network. Rejects unsupported APIs.
func LoadModels(path string) (*ModelsFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ModelsFile{}, nil
		}
		return nil, err
	}
	b = stripBOM(b)
	var mf ModelsFile
	if err := json.Unmarshal(b, &mf); err != nil {
		return nil, fmt.Errorf("%s: malformed models.json: %w", path, err)
	}
	for id, m := range mf.Models {
		if m.API != "" && m.API != "anthropic-messages" {
			return nil, fmt.Errorf("model %q uses unsupported API %q", id, m.API)
		}
	}
	return &mf, nil
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
func DiscoverInstructions(cwd, home string, projectConsent bool) ([]InstructionFile, error) {
	var out []InstructionFile
	seen := map[string]bool{}
	add := func(path, kind string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		out = append(out, InstructionFile{Path: path, Content: string(stripBOM(b)), Kind: kind})
	}
	// User scope first (broad), then ancestors up to cwd, then workspace.
	if home != "" {
		add(filepath.Join(home, "AGENTS.md"), "fallback")
	}
	// Ancestor walk from filesystem root down to cwd for AGENTS.md.
	ancestors := ancestorDirs(cwd)
	for _, dir := range ancestors {
		add(filepath.Join(dir, "AGENTS.md"), "agents")
	}
	// Workspace override wins: AGENTS.override.md replaces AGENTS.md at same dir.
	wsOverride := filepath.Join(cwd, "AGENTS.override.md")
	if _, err := os.Stat(wsOverride); err == nil {
		// Drop workspace AGENTS.md if override exists.
		filtered := out[:0]
		for _, f := range out {
			if !(f.Path == filepath.Join(cwd, "AGENTS.md")) {
				filtered = append(filtered, f)
			}
		}
		out = filtered
		seen[filepath.Join(cwd, "AGENTS.md")] = true
		add(wsOverride, "agents-override")
	}
	// Supported fallbacks at workspace (explicit precedence).
	for _, name := range []string{"CLAUDE.md", "AGENT.md"} {
		add(filepath.Join(cwd, name), "fallback")
	}
	if projectConsent {
		// SYSTEM.md replaces the base prompt; APPEND_SYSTEM.md adds.
		if _, err := os.Stat(filepath.Join(cwd, ".pi", "SYSTEM.md")); err == nil {
			add(filepath.Join(cwd, ".pi", "SYSTEM.md"), "system")
		}
		if _, err := os.Stat(filepath.Join(cwd, ".pi", "APPEND_SYSTEM.md")); err == nil {
			add(filepath.Join(cwd, ".pi", "APPEND_SYSTEM.md"), "append-system")
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
	sb.WriteString("You are psi, a minimal Go coding agent. Use read to examine files instead of cat or sed. Show file paths clearly when working with files.\n")
	sb.WriteString("Tools: " + strings.Join(tools, ", ") + ". Use read, write, edit, bash only.\n")
	for _, ins := range instructions {
		if ins.Kind == "system" {
			// SYSTEM.md replaces the base prompt prefix.
			rebuilt := "Custom system:\n" + ins.Content + "\n"
			rest := sb.String()
			sb.Reset()
			sb.WriteString(rebuilt + rest)
			continue
		}
		sb.WriteString("\n--- " + ins.Path + " ---\n" + ins.Content + "\n")
	}
	if len(skillDescs) > 0 {
		sb.WriteString("\n<available_skills>\n")
		for _, d := range skillDescs {
			sb.WriteString(d + "\n")
		}
		sb.WriteString("</available_skills>\n")
	}
	sb.WriteString("\nWorkspace: " + cwd + "\n")
	return sb.String()
}
