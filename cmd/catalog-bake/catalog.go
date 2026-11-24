package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/modelmeta"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/providerregistry"
)

// catalogModel is one emitted catalog row plus rule-only values that never
// reach the published files.
type catalogModel struct {
	ID               string             `json:"id"`
	API              string             `json:"api"`
	BaseURL          string             `json:"baseUrl"`
	Name             string             `json:"name"`
	ContextWindow    int                `json:"contextWindow"`
	MaxTokens        int                `json:"maxTokens"`
	Reasoning        bool               `json:"reasoning"`
	Input            []string           `json:"input"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap,omitempty"`
	Compat           map[string]any     `json:"compat,omitempty"`

	provider string
	cost     modelCost
}

type modelCost struct {
	Input      float64    `json:"input"`
	Output     float64    `json:"output"`
	CacheRead  float64    `json:"cacheRead"`
	CacheWrite float64    `json:"cacheWrite"`
	Tiers      []costTier `json:"tiers,omitempty"`
}

type costTier struct {
	InputTokensAbove int     `json:"inputTokensAbove"`
	Input            float64 `json:"input"`
	Output           float64 `json:"output"`
	CacheRead        float64 `json:"cacheRead"`
	CacheWrite       float64 `json:"cacheWrite"`

	hasInput, hasOutput, hasCacheRead, hasCacheWrite bool
}

func defaultLimit(value int) int {
	if value <= 0 {
		return 4096
	}
	return value
}

func stringPointer(value string) *string { return &value }

func mergeCompat(model *catalogModel, values map[string]any) {
	if len(values) == 0 {
		return
	}
	if model.Compat == nil {
		model.Compat = make(map[string]any, len(values))
	}
	for name, value := range values {
		model.Compat[name] = value
	}
}

func mergeThinking(model *catalogModel, values map[string]*string) {
	if model.ThinkingLevelMap == nil {
		model.ThinkingLevelMap = make(map[string]*string, len(values))
	}
	for name, value := range values {
		model.ThinkingLevelMap[name] = value
	}
}

func compatBool(model *catalogModel, name string) (value, present bool) {
	raw, ok := model.Compat[name]
	if !ok {
		return false, false
	}
	boolean, ok := raw.(bool)
	if !ok {
		return false, false
	}
	return boolean, true
}

func compatString(model *catalogModel, name string) (string, bool) {
	raw, ok := model.Compat[name]
	if !ok {
		return "", false
	}
	text, ok := raw.(string)
	if !ok {
		return "", false
	}
	return text, true
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// providerDef ports one pinned Pi provider definition. Rule order follows Pi's
// build: rows, API compat defaults, shared DeepSeek/effort/family rules,
// provider finalizer, then cross-model finalization.
type providerDef struct {
	id              string
	source          string // models.dev bucket; empty for authored providers
	api             string
	baseURL         string
	protocol        string // models.dev protocol the row must list
	skipDeprecated  bool
	recordReasoning bool
	include         func(*sourceModel) bool
	mapModel        func(*buildContext, *catalogModel, *sourceModel) (*catalogModel, error)
	custom          func(*buildContext) ([]*catalogModel, error)
	addMissing      func() []*catalogModel
	compatDefaults  func(*catalogModel) map[string]any
	finalize        func(*catalogModel)
	finalizeAll     func([]*catalogModel)
}

type buildContext struct {
	modelsDev  modelsDevCatalog
	openRouter []*catalogModel
	chatCache  map[string][]*sourceModel
}

// chatModels returns every chat row of a models.dev bucket. Provider maps use
// it for cross-row lookups, such as Google's rolling aliases.
func (ctx *buildContext) chatModels(provider string) ([]*sourceModel, error) {
	if cached, ok := ctx.chatCache[provider]; ok {
		return cached, nil
	}
	models, err := modelsDevChatModels(ctx.modelsDev, provider)
	if err != nil {
		return nil, err
	}
	ctx.chatCache[provider] = models
	return models, nil
}

// requiredModelsDevSources are the pinned input buckets. Authored fallback
// entries must not hide a missing source.
var requiredModelsDevSources = []string{
	"anthropic",
	"openai",
	"minimax",
	"opencode-go",
	"zai-coding-plan",
	"google",
	"xiaomi",
	"xiaomi-token-plan-cn",
	"xiaomi-token-plan-ams",
	"xiaomi-token-plan-sgp",
	"moonshotai",
	"kimi-code-plan-global",
	"alibaba-token-plan",
	"xai",
	"groq",
	"mistral",
}

// buildCatalog normalizes one complete catalog. When requireComplete is false
// only the requested provider definitions are checked; the opt-in one-provider
// live proof uses that mode and never publishes a partial public catalog.
func buildCatalog(modelsDev modelsDevCatalog, openRouter openRouterListing, defs []*providerDef, requireComplete bool, warnings io.Writer) (map[string][]*catalogModel, error) {
	if requireComplete {
		for _, source := range requiredModelsDevSources {
			bucket, ok := modelsDev[source]
			if !ok || len(bucket.Models) == 0 {
				return nil, fmt.Errorf("models.dev source %q is missing or empty", source)
			}
		}
	}
	openRouterModels, err := normalizeOpenRouter(openRouter)
	if err != nil {
		return nil, err
	}
	if requireComplete && len(openRouterModels) == 0 {
		return nil, fmt.Errorf("OpenRouter listing produced no tool-capable models")
	}
	ctx := &buildContext{modelsDev: modelsDev, openRouter: openRouterModels, chatCache: map[string][]*sourceModel{}}
	return buildDefinitions(ctx, defs, requireComplete, warnings)
}

func buildDefinitions(ctx *buildContext, defs []*providerDef, requireComplete bool, warnings io.Writer) (map[string][]*catalogModel, error) {
	rows := make(map[string][]*catalogModel, len(defs))
	seenDefs := map[string]bool{}
	for _, def := range defs {
		if seenDefs[def.id] {
			return nil, fmt.Errorf("provider %q is defined twice", def.id)
		}
		seenDefs[def.id] = true
		models, err := buildProvider(ctx, def, requireComplete)
		if err != nil {
			return nil, err
		}
		rows[def.id] = models
	}
	applyReviewedLimitOverrides(rows, requireComplete, warnings)
	if err := validateCatalogRows(rows, requireComplete); err != nil {
		return nil, err
	}
	for id := range rows {
		sort.Slice(rows[id], func(i, j int) bool { return rows[id][i].ID < rows[id][j].ID })
	}
	return rows, nil
}

func buildProvider(ctx *buildContext, def *providerDef, requireSource bool) ([]*catalogModel, error) {
	var models []*catalogModel
	seen := map[string]*catalogModel{}
	// Native effort is applied after API defaults, but it is part of the
	// candidate's identity and must agree before a duplicate can collapse.
	effortByID := map[string]map[string]*string{}
	add := func(model *catalogModel, effort map[string]*string) error {
		model.provider = def.id
		if previous, ok := seen[model.ID]; ok {
			// The final override replaces these two fields. Compare the reviewed
			// pair first so source drift in limits is not a duplicate conflict.
			previousForComparison, modelForComparison := previous, model
			if override, reviewed := reviewedLimitOverrides[def.id][model.ID]; reviewed {
				previousCopy, modelCopy := *previous, *model
				previousCopy.ContextWindow = override.contextWindow
				previousCopy.MaxTokens = override.maxTokens
				modelCopy.ContextWindow = override.contextWindow
				modelCopy.MaxTokens = override.maxTokens
				previousForComparison, modelForComparison = &previousCopy, &modelCopy
			}
			if !reflect.DeepEqual(previousForComparison, modelForComparison) || !reflect.DeepEqual(effortByID[model.ID], effort) {
				return fmt.Errorf("%s: conflicting duplicate model id %q", def.id, model.ID)
			}
			return nil
		}
		seen[model.ID] = model
		if effort != nil {
			effortByID[model.ID] = effort
		}
		models = append(models, model)
		return nil
	}
	if def.source != "" {
		sources, err := ctx.chatModels(def.source)
		if err != nil {
			return nil, err
		}
		for _, source := range sources {
			if !source.ToolCall {
				continue
			}
			if def.protocol != "" && !source.hasProtocol(def.protocol) {
				continue
			}
			if def.skipDeprecated && source.Status == "deprecated" {
				continue
			}
			if def.include != nil && !def.include(source) {
				continue
			}
			model := newCatalogModel(def, source)
			if def.mapModel != nil {
				mapped, err := def.mapModel(ctx, model, source)
				if err != nil {
					return nil, fmt.Errorf("%s/%s: %w", def.id, source.ID, err)
				}
				if mapped == nil {
					continue
				}
				model = mapped
			}
			var effort map[string]*string
			if def.recordReasoning {
				effort = effortThinkingLevelMap(sourceEfforts(source))
			}
			if err := add(model, effort); err != nil {
				return nil, err
			}
		}
		if requireSource && len(models) == 0 {
			return nil, fmt.Errorf("models.dev source %q has no eligible models", def.source)
		}
	}
	if def.custom != nil {
		custom, err := def.custom(ctx)
		if err != nil {
			return nil, err
		}
		for _, model := range custom {
			if err := add(model, nil); err != nil {
				return nil, err
			}
		}
	}
	if def.addMissing != nil {
		for _, model := range def.addMissing() {
			if _, present := seen[model.ID]; present {
				continue
			}
			if err := add(model, nil); err != nil {
				return nil, err
			}
		}
	}
	for _, model := range models {
		applyCompatDefaults(model, def.compatDefaults)
		applyDeepSeekCompatRules(model)
		if effort := effortByID[model.ID]; effort != nil && sendsNativeEffort(model) {
			mergeThinking(model, effort)
		}
		applyOpenAIFamilyRules(model)
		applyClaudeFamilyRules(model)
		applyDeepSeekFamilyRules(model)
	}
	for _, model := range models {
		if def.finalize != nil {
			def.finalize(model)
		}
		if len(model.Compat) == 0 {
			model.Compat = nil
		}
	}
	if def.finalizeAll != nil {
		def.finalizeAll(models)
	}
	return models, nil
}

func newCatalogModel(def *providerDef, source *sourceModel) *catalogModel {
	// Only reviewed rows opt into the source input limit. Compare against the
	// raw source context, not the default fallback, and ignore unusable input.
	contextWindow := defaultLimit(source.ContextWindow)
	if _, reviewed := reviewedLimitOverrides[def.id][source.ID]; reviewed {
		var sourceInput int
		if json.Unmarshal(source.LimitInput, &sourceInput) == nil && sourceInput > 0 && sourceInput < source.ContextWindow {
			contextWindow = sourceInput
		}
	}
	input := []string{"text"}
	if source.ImageInput {
		input = append(input, "image")
	}
	return &catalogModel{
		ID:            source.ID,
		Name:          source.Name,
		API:           def.api,
		BaseURL:       def.baseURL,
		ContextWindow: contextWindow,
		MaxTokens:     defaultLimit(source.MaxTokens),
		Reasoning:     source.Reasoning,
		Input:         input,
		cost:          source.Cost,
	}
}

func applyCompatDefaults(model *catalogModel, defaults func(*catalogModel) map[string]any) {
	if defaults == nil {
		return
	}
	merged := map[string]any{}
	for name, value := range defaults(model) {
		merged[name] = value
	}
	for name, value := range model.Compat {
		merged[name] = value
	}
	if len(merged) > 0 {
		model.Compat = merged
	}
}

var deepSeekCompat = map[string]any{
	"requiresReasoningContentOnAssistantMessages": true,
	"thinkingFormat": "deepseek",
}

var qwenFormatProviders = map[string]bool{
	"qwen-token-plan": true,
}

var nativeEffortProviders = map[string]bool{
	"openrouter": true,
	"opencode":   true,
}

func applyDeepSeekCompatRules(model *catalogModel) {
	if model.API != "openai-completions" || !strings.Contains(model.ID, "deepseek-v4") {
		return
	}
	if qwenFormatProviders[model.provider] {
		return
	}
	if nativeEffortProviders[model.provider] {
		mergeCompat(model, map[string]any{"requiresReasoningContentOnAssistantMessages": true})
		return
	}
	mergeCompat(model, deepSeekCompat)
}

// effortThinkingLevelMap converts models.dev verified effort values into psi's
// selectable levels. Values without a psi equivalent (JSON null and "default")
// are omitted. A valid map makes every unlisted level unsupported.
func effortThinkingLevelMap(values []*string) map[string]*string {
	if len(values) == 0 {
		return nil
	}
	supported := map[string]bool{}
	for _, value := range values {
		if value != nil {
			supported[*value] = true
		}
	}
	levels := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	anyLevel := false
	for _, level := range levels {
		if supported[level] {
			anyLevel = true
			break
		}
	}
	if !anyLevel && !supported["none"] {
		return nil
	}
	levelMap := map[string]*string{"off": nil}
	if supported["none"] {
		levelMap["off"] = stringPointer("none")
	}
	for _, level := range levels {
		if supported[level] {
			levelMap[level] = stringPointer(level)
		} else {
			levelMap[level] = nil
		}
	}
	return levelMap
}

func sendsNativeEffort(model *catalogModel) bool {
	switch model.API {
	case "anthropic-messages":
		adaptive, _ := compatBool(model, "forceAdaptiveThinking")
		return adaptive
	case "openai-responses", "openai-codex-responses":
		return true
	case "openai-completions":
		format, present := compatString(model, "thinkingFormat")
		if !present {
			format = "openai"
		}
		supported, present := compatBool(model, "supportsReasoningEffort")
		if !present {
			supported = true
		}
		return format == "openai" && supported
	default:
		return false
	}
}

var openAIXhighFragments = []string{"gpt-5.2", "gpt-5.3", "gpt-5.4", "gpt-5.5", "gpt-5.6", "gpt-6"}

func supportsOpenAIXhigh(id string) bool {
	for _, fragment := range openAIXhighFragments {
		if strings.Contains(id, fragment) {
			return true
		}
	}
	return false
}

var gpt6ModelIDs = map[string]bool{
	"gpt-6-astra":   true,
	"gpt-6-sol":     true,
	"gpt-6-luna":    true,
	"gpt-6.1-sol":   true,
	"gpt-6.1-terra": true,
	"gpt-6.2-astra": true,
	"gpt-6.2-sol":   true,
}

func isResponsesAPI(api string) bool {
	return api == "openai-responses" || api == "openai-codex-responses"
}

func applyOpenAIFamilyRules(model *catalogModel) {
	if model.API == "openai-responses" && strings.HasPrefix(model.ID, "gpt-5") {
		mergeThinking(model, map[string]*string{"off": nil})
	}
	if gpt6ModelIDs[model.ID] && isResponsesAPI(model.API) {
		off := stringPointer("none")
		if model.ID == "gpt-6-astra" || model.ID == "gpt-6.1-sol" || model.ID == "gpt-6.2-astra" || model.ID == "gpt-6.2-sol" {
			off = nil
		}
		mergeThinking(model, map[string]*string{
			"off":     off,
			"minimal": nil,
			"low":     stringPointer("low"),
			"medium":  stringPointer("medium"),
			"high":    stringPointer("high"),
			"xhigh":   stringPointer("xhigh"),
			"max":     stringPointer("max"),
		})
	}
	if supportsOpenAIXhigh(model.ID) {
		mergeThinking(model, map[string]*string{"xhigh": stringPointer("xhigh")})
	}
	if (strings.Contains(model.ID, "gpt-5.6") || strings.Contains(model.ID, "gpt-6")) && (isResponsesAPI(model.API) || model.API == "openai-completions") {
		mergeThinking(model, map[string]*string{"max": stringPointer("max")})
	}
	if strings.HasSuffix(model.ID, "gpt-5.5-pro") {
		mergeThinking(model, map[string]*string{"off": nil, "minimal": nil, "low": nil})
	}
}

func idIncludesAny(id string, fragments []string) bool {
	for _, fragment := range fragments {
		if strings.Contains(id, fragment) {
			return true
		}
	}
	return false
}

var claudeAdaptiveThinking = []string{
	"opus-4-6", "opus-4.6", "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8",
	"opus-5", "opus.5", "sonnet-4-6", "sonnet-4.6", "sonnet-5", "sonnet.5",
	"haiku-5", "haiku.5", "fable-5", "mythos-5",
}

var claudeTemperatureUnsupported = []string{
	"opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8", "opus-5", "opus.5",
	"sonnet-5", "sonnet.5", "haiku-5-5", "haiku-5.5",
}

var claudeMaxEffortOnly = []string{"opus-4-6", "opus-4.6", "sonnet-4-6", "sonnet-4.6"}

var claudeXHighAndMax = []string{
	"opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8", "opus-5", "opus.5",
	"sonnet-5", "sonnet.5", "haiku-5", "haiku.5",
}

func isClaudeLatestAlias(id string) bool {
	if !strings.HasPrefix(id, "~anthropic/claude-") || !strings.HasSuffix(id, "-latest") {
		return false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(id, "~anthropic/claude-"), "-latest")
	if middle == "" {
		return false
	}
	for _, char := range middle {
		if char < 'a' || char > 'z' {
			return false
		}
	}
	return true
}

func applyClaudeFamilyRules(model *catalogModel) {
	if idIncludesAny(model.ID, claudeMaxEffortOnly) {
		mergeThinking(model, map[string]*string{"max": stringPointer("max")})
	}
	if idIncludesAny(model.ID, claudeXHighAndMax) {
		mergeThinking(model, map[string]*string{"xhigh": stringPointer("xhigh"), "max": stringPointer("max")})
	}
	if strings.Contains(model.ID, "fable-5") {
		mergeThinking(model, map[string]*string{"off": nil, "xhigh": stringPointer("xhigh"), "max": stringPointer("max")})
	}
	if model.API != "anthropic-messages" {
		return
	}
	if isClaudeLatestAlias(model.ID) || idIncludesAny(model.ID, claudeAdaptiveThinking) {
		mergeCompat(model, map[string]any{"forceAdaptiveThinking": true})
	}
	if idIncludesAny(strings.ToLower(model.ID), claudeTemperatureUnsupported) {
		mergeCompat(model, map[string]any{"supportsTemperature": false})
	}
}

// The pinned DeepSeek V4 maps: Flash reports a distinct low level, Pro does
// not, and both reject minimal/medium while accepting high and max.
var deepSeekV4ThinkingLevelMap = map[string]*string{
	"minimal": nil, "low": nil, "medium": nil, "high": stringPointer("high"), "max": stringPointer("max"),
}

var deepSeekV4FlashThinkingLevelMap = map[string]*string{
	"minimal": nil, "low": stringPointer("low"), "medium": nil, "high": stringPointer("high"), "max": stringPointer("max"),
}

func applyDeepSeekFamilyRules(model *catalogModel) {
	if model.API != "openai-completions" || !strings.Contains(model.ID, "deepseek-v4") {
		return
	}
	if model.ThinkingLevelMap != nil {
		return
	}
	switch {
	case model.provider == "openrouter":
		mergeThinking(model, map[string]*string{
			"minimal": nil, "low": nil, "medium": nil, "high": stringPointer("high"), "xhigh": stringPointer("xhigh"), "max": nil,
		})
	case (model.provider == "deepseek" || model.provider == "opencode" || model.provider == "opencode-go") && strings.Contains(model.ID, "deepseek-v4-flash"):
		mergeThinking(model, deepSeekV4FlashThinkingLevelMap)
	default:
		mergeThinking(model, deepSeekV4ThinkingLevelMap)
	}
}

// validateCatalogRows enforces the publish contract for every registry
// provider: a nonempty set, a permitted API and endpoint, positive limits, and
// request metadata the local adapters accept at every advertised level.
func validateCatalogRows(rows map[string][]*catalogModel, requireComplete bool) error {
	ids := make([]string, 0, len(rows))
	for providerID := range rows {
		ids = append(ids, providerID)
	}
	sort.Strings(ids)
	if requireComplete {
		ids = providerregistry.IDs()
		for _, providerID := range ids {
			if _, ok := rows[providerID]; !ok {
				return fmt.Errorf("provider %q is missing from the catalog", providerID)
			}
		}
	}
	for _, providerID := range ids {
		models := rows[providerID]
		if len(models) == 0 {
			return fmt.Errorf("provider %q has no usable models", providerID)
		}
		seen := map[string]bool{}
		for _, model := range models {
			if seen[model.ID] {
				return fmt.Errorf("%s: duplicate model id %q", providerID, model.ID)
			}
			seen[model.ID] = true
			if err := validateRow(providerID, model); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRow(providerID string, model *catalogModel) error {
	label := providerID + "/" + model.ID
	if model.ID == "" {
		return fmt.Errorf("%s: empty model id", providerID)
	}
	if !providerregistry.SupportsAPI(providerID, model.API) {
		return fmt.Errorf("%s: provider does not support API %q", label, model.API)
	}
	if providerID == "opencode-go" {
		if !providerregistry.TrustedCachedEndpoint(providerID, model.API, model.BaseURL) {
			return fmt.Errorf("%s: endpoint %q is outside the reviewed OpenCode allowlist", label, model.BaseURL)
		}
	} else if model.BaseURL != providerregistry.DefaultEndpoint(providerID, model.API) {
		return fmt.Errorf("%s: endpoint %q is not the registered default", label, model.BaseURL)
	}
	if model.ContextWindow <= 0 || model.MaxTokens <= 0 {
		return fmt.Errorf("%s: non-positive limits (%d, %d)", label, model.ContextWindow, model.MaxTokens)
	}
	if len(model.Input) == 0 || !containsString(model.Input, "text") {
		return fmt.Errorf("%s: missing text input", label)
	}
	meta := providerMeta(model)
	levels := provider.SupportedThinking(meta)
	if len(levels) == 0 {
		_, buildErr := provider.BuildRequest(provider.Request{Model: meta, Thinking: "off"})
		return fmt.Errorf("%s: no usable thinking level: %v", label, buildErr)
	}
	for _, level := range levels {
		if _, err := provider.BuildRequest(validationRequest(meta, level)); err != nil {
			return fmt.Errorf("%s: thinking %q: %w", label, level, err)
		}
	}
	return nil
}

func validationRequest(meta provider.ModelMeta, level string) provider.Request {
	return provider.Request{
		Model:    meta,
		Thinking: level,
		System:   "synthetic instructions",
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "synthetic prompt"}}}},
		Tools: []provider.ToolDef{{
			Name:        "probe",
			Description: "synthetic tool",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}},
			},
		}},
	}
}

func providerMeta(model *catalogModel) provider.ModelMeta {
	compat := make(modelmeta.Compat, len(model.Compat))
	for name, value := range model.Compat {
		raw, err := json.Marshal(value)
		if err == nil {
			compat[name] = raw
		}
	}
	adaptive, _, _ := compat.Bool("forceAdaptiveThinking")
	midConvoEffort, _, _ := compat.Bool("supportsMidConvoEffort")
	allowEmptySignature, _, _ := compat.Bool("allowEmptySignature")
	return provider.ModelMeta{
		ID:                  model.ID,
		API:                 model.API,
		Provider:            model.provider,
		BaseURL:             model.BaseURL,
		MaxTokens:           model.MaxTokens,
		ContextWindow:       model.ContextWindow,
		Reasoning:           model.Reasoning,
		InputImage:          containsString(model.Input, "image"),
		ThinkingLevelMap:    model.ThinkingLevelMap,
		AdaptiveThinking:    adaptive,
		MidConvoEffort:      midConvoEffort,
		AllowEmptySignature: allowEmptySignature,
		Compat:              compat,
	}
}

func encodeCatalog(rows map[string][]*catalogModel) ([]byte, error) {
	encoded := make(map[string][]catalogModel, len(rows))
	for providerID, models := range rows {
		copied := make([]catalogModel, len(models))
		for index, model := range models {
			copied[index] = *model
		}
		encoded[providerID] = copied
	}
	data, err := json.MarshalIndent(encoded, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

type inputProvenance struct {
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	FetchedAt string `json:"fetchedAt"`
}

type catalogProvenance struct {
	Generator           string                     `json:"generator"`
	CatalogRuleRevision string                     `json:"catalogRuleRevision"`
	ProtocolReference   string                     `json:"protocolReference"`
	GeneratedAt         string                     `json:"generatedAt"`
	Inputs              map[string]inputProvenance `json:"inputs"`
	ProviderSources     map[string]string          `json:"providerSources"`
	CatalogSHA256       string                     `json:"catalogSHA256"`
}

const (
	catalogGenerator    = "catalog-bake-go-v1"
	catalogRuleRevision = "b9e94bb8d08d06c1028b0e3baab55bb1c5c51d53"
	protocolReference   = "c3cc91632d0a1950fb122605b190233f2aa0006e"
	modelsDevInputName  = "models-dev"
	openRouterInputName = "openrouter"
)

func encodeProvenance(provenance catalogProvenance) ([]byte, error) {
	data, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// assembleArtifacts decodes the exact input bytes, builds and validates the
// catalog, and returns both published documents. Provenance values are passed
// in so a replay with identical bytes and times is byte-identical.
func assembleArtifacts(inputs fetchedInputs, defs []*providerDef, requireComplete bool, now func() time.Time, warnings io.Writer) (catalogBytes, sourceBytes []byte, err error) {
	var modelsDev modelsDevCatalog
	if err := json.Unmarshal(inputs.modelsDevBody, &modelsDev); err != nil {
		return nil, nil, fmt.Errorf("models.dev input: %w", err)
	}
	var openRouter openRouterListing
	if err := json.Unmarshal(inputs.openRouterBody, &openRouter); err != nil {
		return nil, nil, fmt.Errorf("OpenRouter input: %w", err)
	}
	rows, err := buildCatalog(modelsDev, openRouter, defs, requireComplete, warnings)
	if err != nil {
		return nil, nil, err
	}
	// generatedAt is generation time: take it only after both inputs have been
	// normalized and the complete catalog has passed validation.
	generatedAt := now().UTC()
	for _, fetchedAt := range []time.Time{inputs.modelsDevFetchedAt, inputs.openRouterFetchedAt} {
		if generatedAt.Before(fetchedAt) {
			return nil, nil, fmt.Errorf("generatedAt %s is earlier than input fetchedAt %s", generatedAt.Format(time.RFC3339Nano), fetchedAt.Format(time.RFC3339Nano))
		}
	}
	catalogBytes, err = encodeCatalog(rows)
	if err != nil {
		return nil, nil, err
	}
	providerSources := make(map[string]string, len(defs))
	for _, def := range defs {
		switch {
		case def.source != "":
			providerSources[def.id] = "models-dev"
		case def.custom != nil && def.id == "openrouter":
			providerSources[def.id] = "openrouter"
		default:
			providerSources[def.id] = "authored"
		}
	}
	provenance := catalogProvenance{
		Generator:           catalogGenerator,
		CatalogRuleRevision: catalogRuleRevision,
		ProtocolReference:   protocolReference,
		GeneratedAt:         generatedAt.UTC().Format(time.RFC3339Nano),
		Inputs: map[string]inputProvenance{
			modelsDevInputName: {
				URL:       modelsDevURL,
				SHA256:    sha256Hex(inputs.modelsDevBody),
				FetchedAt: inputs.modelsDevFetchedAt.UTC().Format(time.RFC3339Nano),
			},
			openRouterInputName: {
				URL:       openRouterURL,
				SHA256:    sha256Hex(inputs.openRouterBody),
				FetchedAt: inputs.openRouterFetchedAt.UTC().Format(time.RFC3339Nano),
			},
		},
		ProviderSources: providerSources,
		CatalogSHA256:   sha256Hex(catalogBytes),
	}
	sourceBytes, err = encodeProvenance(provenance)
	if err != nil {
		return nil, nil, err
	}
	return catalogBytes, sourceBytes, nil
}
