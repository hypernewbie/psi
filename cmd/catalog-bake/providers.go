package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hypernewbie/psi/internal/providerregistry"
)

// groqProvider is the smallest provider slice. It also serves as the
// one-provider live proof.
func groqProvider() *providerDef {
	return &providerDef{
		id:              "groq",
		source:          "groq",
		api:             "openai-completions",
		baseURL:         providerregistry.DefaultEndpoint("groq", "openai-completions"),
		recordReasoning: true,
		compatDefaults: func(*catalogModel) map[string]any {
			return map[string]any{"supportsStrictMode": true}
		},
		finalize: func(model *catalogModel) {
			// Groq reports a "default" effort for this model, so only "high"
			// sends the reported value; minimal, low, and medium are rejected.
			if model.ID == "qwen/qwen3.6-27b" {
				mergeThinking(model, map[string]*string{
					"minimal": nil,
					"low":     nil,
					"medium":  nil,
					"high":    stringPointer("default"),
				})
			}
		},
	}
}

// providerDefinitions lists every provider the public command publishes. The
// one-provider live proof uses a narrower list and never claims a complete
// catalog.
func providerDefinitions() []*providerDef {
	return []*providerDef{
		anthropicProvider(),
		openAIProvider(),
		openAICodexProvider(),
		minimaxProvider(),
		opencodeGoProvider(),
		deepSeekProvider(),
		zaiProvider(),
		googleProvider(),
		xiaomiProvider("xiaomi"),
		xiaomiProvider("xiaomi-token-plan-cn"),
		xiaomiProvider("xiaomi-token-plan-ams"),
		xiaomiProvider("xiaomi-token-plan-sgp"),
		moonshotProvider(),
		kimiCodingProvider(),
		qwenTokenPlanProvider(),
		openRouterProvider(),
		xaiProvider(),
		groqProvider(),
		mistralProvider(),
	}
}

// isKimiK3FamilyID matches Kimi K3 and its point releases, which share K3's
// request format (pinned /^kimi-k3(?:\.\d+)?(?:-|$)/).
func isKimiK3FamilyID(id string) bool {
	if !strings.HasPrefix(id, "kimi-k3") {
		return false
	}
	rest := strings.TrimPrefix(id, "kimi-k3")
	if rest == "" || strings.HasPrefix(rest, "-") {
		return true
	}
	if !strings.HasPrefix(rest, ".") {
		return false
	}
	rest = rest[1:]
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return false
	}
	rest = rest[digits:]
	return rest == "" || strings.HasPrefix(rest, "-")
}

// ---- Anthropic -----------------------------------------------------------

var (
	anthropicMidConvoEffortPattern = regexp.MustCompile(`^claude-(?:opus-(?:5|5[.-]5)|(?:sonnet|haiku)-5[.-]5|(?:fable|mythos)-5[.-][12])(?:-\d{8})?$`)
	anthropicMidConvoSystemPattern = regexp.MustCompile(`^claude-(?:opus-(?:4[.-]8|5(?:[.-]5)?)|(?:sonnet|haiku)-5[.-]5|(?:fable|mythos)-5(?:[.-][12])?)(?:-\d{8})?$`)
)

func stripAnthropicPrefix(id string) string {
	id = strings.ToLower(id)
	id = strings.TrimPrefix(id, "~anthropic/")
	id = strings.TrimPrefix(id, "anthropic/")
	return id
}

func supportsClaudeMidConvoEffort(id string) bool {
	return anthropicMidConvoEffortPattern.MatchString(stripAnthropicPrefix(id))
}

func supportsClaudeMidConvoSystemMessages(id string) bool {
	return anthropicMidConvoSystemPattern.MatchString(id)
}

var claudeDisabledThinkingModels = map[string]bool{
	"claude-opus-5":    true,
	"claude-haiku-5-5": true,
	"claude-haiku-5.5": true,
}

var claudeDateSuffix = regexp.MustCompile(`-\d{8}$`)

func enableClaudeMidConvoEffort(model *catalogModel) {
	mergeCompat(model, map[string]any{"supportsMidConvoEffort": true})
	stripped := claudeDateSuffix.ReplaceAllString(stripAnthropicPrefix(model.ID), "")
	if !claudeDisabledThinkingModels[stripped] {
		mergeThinking(model, map[string]*string{"off": nil})
	}
}

func claude55ThinkingLevels(id string) map[string]*string {
	levels := map[string]*string{
		"minimal": nil,
		"low":     stringPointer("low"),
		"medium":  stringPointer("medium"),
		"high":    stringPointer("high"),
		"xhigh":   stringPointer("xhigh"),
		"max":     stringPointer("max"),
	}
	if !strings.Contains(id, "haiku") {
		levels["off"] = nil
	}
	return levels
}

var (
	anthropicModelsWith1MContext = map[string]bool{"claude-opus-4-6": true, "claude-sonnet-4-6": true}
	anthropicModels55            = map[string]bool{"claude-opus-5-5": true, "claude-sonnet-5-5": true, "claude-haiku-5-5": true}
)

type fallbackModel struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Cost     modelCost `json:"cost"`
}

func anthropicProvider() *providerDef {
	return &providerDef{
		id:              "anthropic",
		source:          "anthropic",
		api:             "anthropic-messages",
		baseURL:         providerregistry.DefaultEndpoint("anthropic", "anthropic-messages"),
		recordReasoning: true,
		mapModel: func(_ *buildContext, model *catalogModel, _ *sourceModel) (*catalogModel, error) {
			if anthropicModels55[model.ID] {
				mergeThinking(model, claude55ThinkingLevels(model.ID))
			}
			if anthropicModelsWith1MContext[model.ID] {
				model.ContextWindow = 1000000
			}
			return model, nil
		},
		addMissing: func() []*catalogModel {
			return []*catalogModel{{
				ID:            "claude-fable-5-2",
				Name:          "Claude Fable 5.2",
				API:           "anthropic-messages",
				BaseURL:       providerregistry.DefaultEndpoint("anthropic", "anthropic-messages"),
				Reasoning:     true,
				Input:         []string{"text", "image"},
				ContextWindow: 1000000,
				MaxTokens:     128000,
				cost:          modelCost{Input: 8, Output: 40, CacheRead: 0.2, CacheWrite: 10},
			}}
		},
		finalize: func(model *catalogModel) {
			if model.API != "anthropic-messages" {
				return
			}
			if supportsClaudeMidConvoEffort(model.ID) {
				enableClaudeMidConvoEffort(model)
			}
			if supportsClaudeMidConvoSystemMessages(model.ID) {
				mergeCompat(model, map[string]any{
					"supportsMidConvoSystemMessages": true,
					"supportsMidConvoToolChanges":    true,
				})
			}
			mergeCompat(model, map[string]any{"supportsStrictTools": true})
		},
		finalizeAll: func(models []*catalogModel) {
			byID := make(map[string]*catalogModel, len(models))
			for _, model := range models {
				byID[model.ID] = model
			}
			fallbacks := []struct {
				id         string
				candidates []string
			}{
				{"claude-fable-5", []string{"claude-opus-4-8", "claude-opus-5"}},
				{"claude-fable-5-2", []string{"claude-opus-4-8", "claude-opus-5"}},
				{"claude-opus-5", []string{"claude-opus-4-8"}},
			}
			for _, entry := range fallbacks {
				model := byID[entry.id]
				if model == nil {
					continue
				}
				candidates := entry.candidates
				if midConvo, _ := compatBool(model, "supportsMidConvoEffort"); midConvo {
					filtered := candidates[:0]
					for _, candidate := range candidates {
						if supportsClaudeMidConvoEffort(candidate) {
							filtered = append(filtered, candidate)
						}
					}
					candidates = filtered
				}
				var allowed []fallbackModel
				for _, candidate := range candidates {
					fallback := byID[candidate]
					if fallback == nil {
						continue
					}
					allowed = append(allowed, fallbackModel{Provider: fallback.provider, Model: fallback.ID, Cost: fallback.cost})
				}
				if len(allowed) > 0 {
					mergeCompat(model, map[string]any{"allowedFallbackModels": allowed})
				}
			}
		},
	}
}

// ---- OpenAI --------------------------------------------------------------

var (
	openAIUnsupportedModelIDs = map[string]bool{"gpt-5.6": true}
	openAIShortContextCapped  = map[string]bool{
		"gpt-5.4": true, "gpt-5.5": true, "gpt-5.6-sol": true, "gpt-5.6-terra": true,
		"gpt-5.6-luna": true, "gpt-6-astra": true, "gpt-6-sol": true, "gpt-6-luna": true,
		"gpt-6.1-sol": true, "gpt-6.1-terra": true,
		"gpt-6.2-astra": true, "gpt-6.2-sol": true,
	}
	openAINoneReasoning = map[string]bool{
		"gpt-5.1": true, "gpt-5.2": true, "gpt-5.3-codex": true, "gpt-5.4": true,
		"gpt-5.4-mini": true, "gpt-5.4-nano": true, "gpt-5.5": true, "gpt-5.6-sol": true,
		"gpt-5.6-terra": true, "gpt-5.6-luna": true, "gpt-6-sol": true, "gpt-6-luna": true,
	}
	openAIMidConvoEffort = map[string]bool{
		"gpt-6-astra": true, "gpt-6-sol": true, "gpt-6-luna": true, "gpt-6.1-sol": true, "gpt-6.1-terra": true,
		"gpt-6.2-astra": true, "gpt-6.2-sol": true,
	}
	openAIToolSearchModelIDs = map[string]bool{
		"gpt-5.6-sol": true, "gpt-5.6-terra": true, "gpt-5.6-luna": true, "gpt-6-astra": true,
		"gpt-6-sol": true, "gpt-6-luna": true, "gpt-6.1-sol": true, "gpt-6.1-terra": true,
		"gpt-6.2-astra": true, "gpt-6.2-sol": true,
	}
	openAIMidConvoModelIDs = func() map[string]bool {
		ids := map[string]bool{"gpt-5.4": true, "gpt-5.4-mini": true, "gpt-5.4-pro": true, "gpt-5.5": true}
		for id := range openAIToolSearchModelIDs {
			ids[id] = true
		}
		return ids
	}()
)

// Only the corrected base cache-write price affects this projection: a paid
// write enables explicit cache mode. No OpenAI prices or tiers are emitted.
var openAICacheWritePrices = map[string]float64{
	"gpt-5.6-luna": 0.25, "gpt-5.6-sol": 5, "gpt-5.6-terra": 2.5,
	"gpt-6-astra": 12.5, "gpt-6-luna": 0.125, "gpt-6-sol": 2.5,
	"gpt-6.1-sol": 2.5, "gpt-6.1-terra": 1.25,
	"gpt-6.2-astra": 10, "gpt-6.2-sol": 2.5,
}

func correctOpenAIUpstream(model *catalogModel) {
	if openAIShortContextCapped[model.ID] {
		model.ContextWindow = 272000
		model.MaxTokens = 128000
	}
	if price, ok := openAICacheWritePrices[model.ID]; ok {
		model.cost.CacheWrite = price
	}
	if model.ID == "gpt-5-pro" {
		model.MaxTokens = 128000
	}
}

var openAIGrammarPrefix = regexp.MustCompile(`^gpt-(\d+)`)

func enableOpenAIGrammarTools(model *catalogModel) {
	if !isResponsesAPI(model.API) {
		return
	}
	match := openAIGrammarPrefix.FindStringSubmatch(model.ID)
	if len(match) != 2 {
		return
	}
	generation := 0
	for _, digit := range match[1] {
		generation = generation*10 + int(digit-'0')
	}
	if generation >= 5 {
		mergeCompat(model, map[string]any{"supportsOpenAIGrammarTools": true})
	}
}

func enableOpenAIMidConvoSystemMessages(model *catalogModel, proxy bool) {
	if model.API != "openai-responses" && model.API != "openai-codex-responses" {
		return
	}
	if !openAIMidConvoModelIDs[model.ID] {
		return
	}
	compat := map[string]any{"supportsMidConvoSystemMessages": true}
	if proxy && openAIToolSearchModelIDs[model.ID] {
		compat["supportsAdditionalTools"] = true
	}
	mergeCompat(model, compat)
}

func openAIProvider() *providerDef {
	return &providerDef{
		id:              "openai",
		source:          "openai",
		api:             "openai-responses",
		baseURL:         providerregistry.DefaultEndpoint("openai", "openai-responses"),
		protocol:        "responses",
		recordReasoning: true,
		include:         func(source *sourceModel) bool { return !openAIUnsupportedModelIDs[source.ID] },
		mapModel: func(_ *buildContext, model *catalogModel, _ *sourceModel) (*catalogModel, error) {
			correctOpenAIUpstream(model)
			return model, nil
		},
		addMissing: func() []*catalogModel {
			return []*catalogModel{{
				ID:            "gpt-5-chat-latest",
				Name:          "GPT-5 Chat Latest",
				API:           "openai-responses",
				BaseURL:       providerregistry.DefaultEndpoint("openai", "openai-responses"),
				Reasoning:     false,
				Input:         []string{"text", "image"},
				ContextWindow: 128000,
				MaxTokens:     16384,
			}}
		},
		finalize: func(model *catalogModel) {
			if model.API != "openai-responses" {
				return
			}
			if openAINoneReasoning[model.ID] {
				mergeThinking(model, map[string]*string{"off": stringPointer("none")})
			}
			if model.ID == "gpt-5.5" {
				mergeThinking(model, map[string]*string{"minimal": nil})
			}
			mergeCompat(model, map[string]any{"supportsStrictMode": true})
			enableOpenAIGrammarTools(model)
			if openAIToolSearchModelIDs[model.ID] {
				mergeCompat(model, map[string]any{"supportsAdditionalTools": true, "supportsToolSearch": true})
			}
			enableOpenAIMidConvoSystemMessages(model, false)
			if model.cost.CacheWrite > 0 {
				mergeCompat(model, map[string]any{"supportsExplicitPromptCacheMode": true})
			}
			if openAIMidConvoEffort[model.ID] {
				mergeCompat(model, map[string]any{"supportsMidConvoEffort": true})
			}
		},
	}
}

func openAICodexProvider() *providerDef {
	type codexModel struct {
		id            string
		name          string
		contextWindow int
	}
	authored := []codexModel{
		{"gpt-6.2-astra", "GPT-6.2 Astra", 272000},
		{"gpt-6.2-sol", "GPT-6.2 Sol", 500000},
		{"gpt-6.1-sol", "GPT-6.1 Sol", 500000},
		{"gpt-6.1-terra", "GPT-6.1 Terra", 500000},
		{"gpt-6-astra", "GPT-6 Astra", 272000},
		{"gpt-6-sol", "GPT-6 Sol", 272000},
		{"gpt-6-luna", "GPT-6 Luna", 272000},
		{"gpt-5.6-luna", "GPT-5.6 Luna", 272000},
		{"gpt-5.6-sol", "GPT-5.6 Sol", 272000},
		{"gpt-5.6-terra", "GPT-5.6 Terra", 272000},
	}
	return &providerDef{
		id:      "openai-codex",
		api:     "openai-codex-responses",
		baseURL: providerregistry.DefaultEndpoint("openai-codex", "openai-codex-responses"),
		custom: func(*buildContext) ([]*catalogModel, error) {
			models := make([]*catalogModel, 0, len(authored))
			for _, entry := range authored {
				models = append(models, &catalogModel{
					ID:            entry.id,
					Name:          entry.name,
					API:           "openai-codex-responses",
					BaseURL:       providerregistry.DefaultEndpoint("openai-codex", "openai-codex-responses"),
					Reasoning:     true,
					Input:         []string{"text", "image"},
					ContextWindow: entry.contextWindow,
					MaxTokens:     128000,
				})
			}
			return models, nil
		},
		finalize: func(model *catalogModel) {
			if supportsOpenAIXhigh(model.ID) {
				mergeThinking(model, map[string]*string{"minimal": stringPointer("low")})
			}
			enableOpenAIGrammarTools(model)
			if openAIToolSearchModelIDs[model.ID] {
				mergeCompat(model, map[string]any{
					"supportsAdditionalTools": true,
					"supportsToolSearch":      true,
				})
			}
			enableOpenAIMidConvoSystemMessages(model, false)
			if openAIMidConvoEffort[model.ID] {
				mergeCompat(model, map[string]any{"supportsMidConvoEffort": true})
			}
		},
	}
}

// ---- MiniMax -------------------------------------------------------------

var minimaxHiddenModelIDs = map[string]bool{
	"MiniMax-M2": true, "MiniMax-M2.1": true, "MiniMax-M2.5": true, "MiniMax-M2.5-highspeed": true,
}

func minimaxProvider() *providerDef {
	return &providerDef{
		id:              "minimax",
		source:          "minimax",
		api:             "anthropic-messages",
		baseURL:         providerregistry.DefaultEndpoint("minimax", "anthropic-messages"),
		recordReasoning: false,
		include:         func(source *sourceModel) bool { return !minimaxHiddenModelIDs[source.ID] },
		mapModel: func(ctx *buildContext, model *catalogModel, source *sourceModel) (*catalogModel, error) {
			efforts := sourceEfforts(source)
			if levelMap := effortThinkingLevelMap(efforts); levelMap != nil {
				model.ThinkingLevelMap = levelMap
				mergeCompat(model, map[string]any{"forceAdaptiveThinking": true})
			}
			return model, nil
		},
	}
}

// ---- OpenCode Go ---------------------------------------------------------

var openCodeProtocols = []string{"responses", "messages", "generate-content", "chat-completions"}

var openCodeLongCacheRetentionUnsupported = map[string]bool{
	"opencode-go:kimi-k2.6": true,
}

type openCodeRoute struct {
	api     string
	baseURL string
	compat  map[string]any
}

func findOpenCodeRoute(source *sourceModel) (openCodeRoute, error) {
	for _, protocol := range openCodeProtocols {
		for _, api := range source.APIs {
			if api.Protocol != protocol {
				continue
			}
			switch protocol {
			case "responses":
				return openCodeRoute{api: "openai-responses", baseURL: api.BaseURL, compat: map[string]any{"sessionAffinityFormat": "openai-nosession"}}, nil
			case "messages":
				return openCodeRoute{api: "anthropic-messages", baseURL: stripMessagesV1Suffix(api.BaseURL)}, nil
			case "generate-content":
				return openCodeRoute{api: "google-generative-ai", baseURL: api.BaseURL}, nil
			case "chat-completions":
				route := openCodeRoute{api: "openai-completions", baseURL: api.BaseURL}
				if api.AISDK == "@ai-sdk/alibaba" {
					route.compat = map[string]any{"cacheControlFormat": "anthropic"}
				}
				return route, nil
			}
		}
	}
	protocols := make([]string, 0, len(source.APIs))
	for _, api := range source.APIs {
		protocols = append(protocols, api.Protocol)
	}
	if len(protocols) == 0 {
		protocols = append(protocols, "none")
	}
	return openCodeRoute{}, fmt.Errorf("no supported API among %s", strings.Join(protocols, ", "))
}

func stripMessagesV1Suffix(baseURL string) string {
	if strings.HasSuffix(baseURL, "/v1/") {
		return strings.TrimSuffix(baseURL, "/v1/")
	}
	return strings.TrimSuffix(baseURL, "/v1")
}

func opencodeGoProvider() *providerDef {
	return &providerDef{
		id:              "opencode-go",
		source:          "opencode-go",
		api:             "openai-completions",
		baseURL:         "",
		skipDeprecated:  true,
		recordReasoning: true,
		include:         func(source *sourceModel) bool { return source.ID != "gpt-5.3-codex-spark" },
		mapModel: func(ctx *buildContext, model *catalogModel, source *sourceModel) (*catalogModel, error) {
			route, err := findOpenCodeRoute(source)
			if err != nil {
				return nil, err
			}
			api, baseURL, compat := route.api, route.baseURL, route.compat
			if model.ID == "kimi-k2.6" {
				compat = mergeCompatValues(compat, map[string]any{
					"thinkingFormat":          "deepseek",
					"supportsReasoningEffort": false,
				})
			}
			// models.dev lists these as Anthropic, but OpenCode Go serves them
			// through its OpenAI-compatible Chat Completions path.
			if api == "anthropic-messages" && (model.ID == "qwen3.5-plus" || model.ID == "qwen3.6-plus") {
				api = "openai-completions"
				if prefix, ok := strings.CutSuffix(baseURL, "/anthropic"); ok {
					baseURL = prefix + "/openai/v1"
				}
				compat = mergeCompatValues(compat, map[string]any{"thinkingFormat": "qwen"})
			}
			if api == "openai-completions" {
				compat = mergeCompatValues(compat, map[string]any{"maxTokensField": "max_tokens"})
				if openCodeLongCacheRetentionUnsupported["opencode-go:"+model.ID] {
					compat = mergeCompatValues(compat, map[string]any{"supportsLongCacheRetention": false})
				}
			}
			model.API = api
			model.BaseURL = baseURL
			if compat != nil {
				mergeCompat(model, compat)
			}
			if api == "google-generative-ai" {
				if levelMap := googleThinkingLevels(model.ID, source); levelMap != nil {
					model.ThinkingLevelMap = levelMap
				}
			}
			if model.ID == "deepseek-v4.1-flash" {
				if levelMap := effortThinkingLevelMap(sourceEfforts(source)); levelMap != nil {
					model.ThinkingLevelMap = levelMap
				}
			}
			if model.ID == "claude-sonnet-4-5" || model.ID == "claude-sonnet-4" {
				model.ContextWindow = 200000
			}
			if model.ID == "claude-opus-4-6" || model.ID == "claude-sonnet-4-6" ||
				model.ID == "claude-opus-4.6" || model.ID == "claude-sonnet-4.6" {
				model.ContextWindow = 1000000
			}
			if model.ID == "gpt-5.4" {
				model.ContextWindow = 272000
				model.MaxTokens = 128000
			}
			return model, nil
		},
		compatDefaults: func(model *catalogModel) map[string]any {
			if model.API != "openai-completions" {
				return nil
			}
			return map[string]any{
				"supportsStore":         false,
				"supportsDeveloperRole": false,
				"supportsStrictMode":    true,
			}
		},
		finalize: func(model *catalogModel) {
			if model.ID == "glm-5.2" {
				mergeThinking(model, map[string]*string{
					"off": nil, "minimal": nil, "low": nil, "medium": nil,
					"high": stringPointer("high"), "max": stringPointer("max"),
				})
			}
			if model.ID == "kimi-k2.6" {
				mergeThinking(model, map[string]*string{"minimal": nil, "low": nil, "medium": nil})
			}
			if model.API == "anthropic-messages" {
				if supportsClaudeMidConvoEffort(model.ID) {
					enableClaudeMidConvoEffort(model)
				}
				if model.ID == "qwen3.8-flash" {
					mergeCompat(model, map[string]any{"allowEmptySignature": true})
				}
			}
			if model.API == "openai-completions" && isKimiK3FamilyID(model.ID) {
				mergeCompat(model, map[string]any{
					"supportsMidConvoSystemMessages": true,
					"supportsMidConvoToolAdditions":  true,
				})
			}
			enableOpenAIMidConvoSystemMessages(model, true)
		},
	}
}

func mergeCompatValues(base map[string]any, extra map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(extra))
	for name, value := range base {
		merged[name] = value
	}
	for name, value := range extra {
		merged[name] = value
	}
	return merged
}

// ---- DeepSeek ------------------------------------------------------------

func deepSeekProvider() *providerDef {
	return &providerDef{
		id:      "deepseek",
		api:     "openai-completions",
		baseURL: providerregistry.DefaultEndpoint("deepseek", "openai-completions"),
		custom: func(*buildContext) ([]*catalogModel, error) {
			return []*catalogModel{
				{
					ID:            "deepseek-flash",
					Name:          "DeepSeek V4.1 Flash",
					API:           "openai-completions",
					BaseURL:       providerregistry.DefaultEndpoint("deepseek", "openai-completions"),
					Reasoning:     true,
					Input:         []string{"text", "image"},
					ContextWindow: 1000000,
					MaxTokens:     384000,
					ThinkingLevelMap: map[string]*string{
						"minimal": nil, "low": stringPointer("low"), "medium": nil,
						"high": stringPointer("high"), "max": stringPointer("max"),
					},
					Compat: map[string]any{
						"requiresReasoningContentOnAssistantMessages": true,
						"thinkingFormat": "deepseek",
					},
				},
				{
					ID:            "deepseek-v4-pro",
					Name:          "DeepSeek V4 Pro",
					API:           "openai-completions",
					BaseURL:       providerregistry.DefaultEndpoint("deepseek", "openai-completions"),
					Reasoning:     true,
					Input:         []string{"text"},
					ContextWindow: 1000000,
					MaxTokens:     384000,
					Compat: map[string]any{
						"requiresReasoningContentOnAssistantMessages": true,
						"thinkingFormat": "deepseek",
					},
				},
			}, nil
		},
		compatDefaults: func(*catalogModel) map[string]any {
			return map[string]any{
				"supportsStore":         false,
				"supportsDeveloperRole": false,
				"maxTokensField":        "max_tokens",
				"supportsStrictMode":    true,
			}
		},
		finalize: func(model *catalogModel) {
			if model.ID == "deepseek-v4-pro" {
				mergeCompat(model, map[string]any{"supportsMidConvoSystemMessages": true})
			}
		},
	}
}

// ---- Z.AI ----------------------------------------------------------------

var zaiToolStreamUnsupported = map[string]bool{
	"glm-4.5": true, "glm-4.5-air": true, "glm-4.5-flash": true, "glm-4.5v": true,
}

func zaiProvider() *providerDef {
	return &providerDef{
		id:              "zai",
		source:          "zai-coding-plan",
		api:             "openai-completions",
		baseURL:         providerregistry.DefaultEndpoint("zai", "openai-completions"),
		recordReasoning: true,
		mapModel: func(ctx *buildContext, model *catalogModel, source *sourceModel) (*catalogModel, error) {
			levelMap := effortThinkingLevelMap(sourceEfforts(source))
			if levelMap != nil && (model.ID == "glm-5.2" || model.ID == "glm-5.2-highspeed") {
				levelMap["off"] = stringPointer("none")
			}
			compat := map[string]any{
				"supportsDeveloperRole": false,
				"thinkingFormat":        "zai",
			}
			if levelMap != nil {
				compat["supportsReasoningEffort"] = true
				model.ThinkingLevelMap = levelMap
			}
			if !zaiToolStreamUnsupported[model.ID] {
				compat["zaiToolStream"] = true
			}
			mergeCompat(model, compat)
			return model, nil
		},
		compatDefaults: func(*catalogModel) map[string]any {
			return map[string]any{
				"supportsStore":           false,
				"supportsDeveloperRole":   false,
				"supportsReasoningEffort": false,
				"maxTokensField":          "max_tokens",
				"thinkingFormat":          "zai",
				"supportsStrictMode":      true,
			}
		},
	}
}

// ---- Google --------------------------------------------------------------

var googleAliasTargets = map[string]string{
	"gemini-flash-latest":      "gemini-3.5-flash",
	"gemini-flash-lite-latest": "gemini-3.1-flash-lite",
}

func isGemma4Model(id string) bool {
	lower := strings.ToLower(id)
	return strings.Contains(lower, "gemma4") || strings.Contains(lower, "gemma-4")
}

func googleThinkingLevels(id string, source *sourceModel) map[string]*string {
	if levelMap := effortThinkingLevelMap(sourceEfforts(source)); levelMap != nil {
		return levelMap
	}
	if isGemma4Model(id) {
		return map[string]*string{
			"off": nil, "minimal": stringPointer("MINIMAL"), "low": nil, "medium": nil, "high": stringPointer("HIGH"),
		}
	}
	return nil
}

func googleProvider() *providerDef {
	return &providerDef{
		id:              "google",
		source:          "google",
		api:             "google-generative-ai",
		baseURL:         providerregistry.DefaultEndpoint("google", "google-generative-ai"),
		recordReasoning: false,
		mapModel: func(ctx *buildContext, model *catalogModel, aliasSource *sourceModel) (*catalogModel, error) {
			source := aliasSource
			if target, ok := googleAliasTargets[model.ID]; ok {
				rows, err := ctx.chatModels("google")
				if err != nil {
					return nil, err
				}
				for _, row := range rows {
					if row.ID == target {
						source = row
						break
					}
				}
			}
			model.Reasoning = source.Reasoning
			if levelMap := googleThinkingLevels(model.ID, source); levelMap != nil {
				model.ThinkingLevelMap = levelMap
			}
			if source.ImageInput {
				model.Input = []string{"text", "image"}
			} else {
				model.Input = []string{"text"}
			}
			model.ContextWindow = defaultLimit(source.ContextWindow)
			model.MaxTokens = defaultLimit(source.MaxTokens)
			return model, nil
		},
	}
}

// ---- Xiaomi --------------------------------------------------------------

func xiaomiProvider(id string) *providerDef {
	return &providerDef{
		id:              id,
		source:          id,
		api:             "openai-completions",
		baseURL:         providerregistry.DefaultEndpoint(id, "openai-completions"),
		skipDeprecated:  true,
		recordReasoning: false,
		compatDefaults: func(*catalogModel) map[string]any {
			return map[string]any{"supportsStrictMode": true}
		},
		mapModel: func(_ *buildContext, model *catalogModel, _ *sourceModel) (*catalogModel, error) {
			mergeCompat(model, map[string]any{
				"supportsReasoningEffort":                     false,
				"requiresReasoningContentOnAssistantMessages": true,
				"thinkingFormat":                              "deepseek",
			})
			if model.Reasoning {
				// Xiaomi documents all effort values as identical and its API
				// expects the literal "off" string, not "none".
				model.ThinkingLevelMap = map[string]*string{
					"off": stringPointer("off"), "minimal": nil, "low": nil,
					"medium": nil, "high": stringPointer("high"), "xhigh": nil, "max": nil,
				}
			}
			return model, nil
		},
	}
}

// ---- Moonshot ------------------------------------------------------------

var kimiOpenAICompat = map[string]any{
	"supportsStore":           false,
	"supportsDeveloperRole":   false,
	"supportsReasoningEffort": false,
	"maxTokensField":          "max_tokens",
	"supportsStrictMode":      false,
	"thinkingFormat":          "deepseek",
}

var kimiK3OpenAICompat = map[string]any{
	"supportsStore":                               false,
	"supportsDeveloperRole":                       false,
	"supportsReasoningEffort":                     true,
	"maxTokensField":                              "max_tokens",
	"supportsStrictMode":                          false,
	"thinkingFormat":                              "openai",
	"requiresReasoningContentOnAssistantMessages": true,
}

var moonshotAlwaysThinking = map[string]bool{"kimi-k2.7-code": true, "kimi-k2.7-code-highspeed": true}

var moonshotTextOnlyMidConvo = map[string]bool{
	"kimi-k2.6": true, "kimi-k2.7-code": true, "kimi-k2.7-code-highspeed": true,
}

func moonshotProvider() *providerDef {
	return &providerDef{
		id:              "moonshotai",
		source:          "moonshotai",
		api:             "openai-completions",
		baseURL:         providerregistry.DefaultEndpoint("moonshotai", "openai-completions"),
		recordReasoning: true,
		mapModel: func(ctx *buildContext, model *catalogModel, source *sourceModel) (*catalogModel, error) {
			isK3 := isKimiK3FamilyID(model.ID)
			model.Reasoning = isK3 || source.Reasoning
			if isK3 {
				mergeCompat(model, kimiK3OpenAICompat)
			} else {
				mergeCompat(model, kimiOpenAICompat)
			}
			return model, nil
		},
		compatDefaults: func(*catalogModel) map[string]any {
			return map[string]any{
				"supportsStore":           false,
				"supportsDeveloperRole":   false,
				"supportsReasoningEffort": false,
				"maxTokensField":          "max_tokens",
			}
		},
		finalize: func(model *catalogModel) {
			if moonshotAlwaysThinking[model.ID] {
				mergeThinking(model, map[string]*string{"off": nil})
			}
			isK3 := isKimiK3FamilyID(model.ID)
			if isK3 || moonshotTextOnlyMidConvo[model.ID] {
				compat := map[string]any{"supportsMidConvoSystemMessages": true}
				if isK3 {
					compat["supportsMidConvoToolAdditions"] = true
				}
				mergeCompat(model, compat)
			}
		},
	}
}

// ---- Kimi Coding ---------------------------------------------------------

func kimiCodingProvider() *providerDef {
	return &providerDef{
		id:              "kimi-coding",
		source:          "kimi-code-plan-global",
		api:             "openai-completions",
		baseURL:         providerregistry.DefaultEndpoint("kimi-coding", "openai-completions"),
		recordReasoning: false,
		mapModel: func(ctx *buildContext, model *catalogModel, source *sourceModel) (*catalogModel, error) {
			isK3Style := false
			for _, option := range source.ReasoningOptions {
				if option.isEffort() {
					isK3Style = true
					break
				}
			}
			if isK3Style {
				mergeCompat(model, kimiK3OpenAICompat)
			} else {
				mergeCompat(model, kimiOpenAICompat)
			}
			mergeCompat(model, map[string]any{"supportsMidConvoSystemMessages": true})
			if isK3Style {
				mergeCompat(model, map[string]any{"supportsMidConvoToolAdditions": true})
			}
			levels := map[string]*string{"off": nil}
			if isK3Style {
				for name, value := range effortThinkingLevelMap(sourceEfforts(source)) {
					levels[name] = value
				}
			}
			// Kimi Code reports every model as always-thinking, so an explicit
			// null "off" wins over a reported "none" effort value.
			levels["off"] = nil
			model.ThinkingLevelMap = levels
			return model, nil
		},
		compatDefaults: func(*catalogModel) map[string]any {
			return map[string]any{"supportsStrictMode": true}
		},
	}
}

// ---- Qwen Token Plan -----------------------------------------------------

var qwenExcludedModelIDs = map[string]bool{
	"qwen3.8-max-preview": true,
	// Retired upstream; models.dev lists them again but the Qwen Token Plan
	// endpoint no longer serves them.
	"deepseek-v3.2":     true,
	"deepseek-v4-flash": true,
}

var qwenFallbackThinkingLevels = map[string]*string{
	"minimal": nil, "low": nil, "medium": nil, "high": stringPointer("high"), "xhigh": nil, "max": stringPointer("max"),
}

var qwenReasoningEffortFallback = map[string]bool{"glm-5": true, "glm-5.1": true}

func qwenTokenPlanProvider() *providerDef {
	return &providerDef{
		id:              "qwen-token-plan",
		source:          "alibaba-token-plan",
		api:             "openai-completions",
		baseURL:         providerregistry.DefaultEndpoint("qwen-token-plan", "openai-completions"),
		recordReasoning: false,
		include:         func(source *sourceModel) bool { return !qwenExcludedModelIDs[source.ID] },
		mapModel: func(ctx *buildContext, model *catalogModel, source *sourceModel) (*catalogModel, error) {
			levelMap := effortThinkingLevelMap(sourceEfforts(source))
			if levelMap == nil && qwenReasoningEffortFallback[model.ID] {
				levelMap = qwenFallbackThinkingLevels
			}
			compat := map[string]any{
				"thinkingFormat":          "qwen",
				"supportsDeveloperRole":   false,
				"supportsStore":           false,
				"supportsReasoningEffort": true,
			}
			if levelMap != nil {
				model.ThinkingLevelMap = levelMap
			} else {
				compat["supportsReasoningEffort"] = false
			}
			mergeCompat(model, compat)
			return model, nil
		},
		compatDefaults: func(*catalogModel) map[string]any {
			return map[string]any{"supportsStrictMode": true}
		},
	}
}

// ---- OpenRouter ----------------------------------------------------------

func openRouterProvider() *providerDef {
	return &providerDef{
		id:      "openrouter",
		api:     "openai-completions",
		baseURL: providerregistry.DefaultEndpoint("openrouter", "openai-completions"),
		custom: func(ctx *buildContext) ([]*catalogModel, error) {
			models := make([]*catalogModel, 0, len(ctx.openRouter)+2)
			models = append(models, ctx.openRouter...)
			return models, nil
		},
		addMissing: func() []*catalogModel {
			return []*catalogModel{
				{
					ID:            "auto",
					Name:          "Auto",
					API:           "openai-completions",
					BaseURL:       providerregistry.DefaultEndpoint("openrouter", "openai-completions"),
					Reasoning:     true,
					Input:         []string{"text", "image"},
					ContextWindow: 2000000,
					MaxTokens:     30000,
				},
				{
					ID:            "openrouter/fusion",
					Name:          "OpenRouter: Fusion",
					API:           "openai-completions",
					BaseURL:       providerregistry.DefaultEndpoint("openrouter", "openai-completions"),
					Reasoning:     true,
					Input:         []string{"text"},
					ContextWindow: 1000000,
					MaxTokens:     30000,
				},
			}
		},
		compatDefaults: openRouterCompatDefaults,
		finalize: func(model *catalogModel) {
			if strings.HasPrefix(model.ID, "inception/mercury-2") {
				mergeThinking(model, map[string]*string{"off": nil})
			}
			if model.ID == "z-ai/glm-5.2" {
				mergeThinking(model, map[string]*string{"xhigh": stringPointer("xhigh")})
			}
			if model.API == "openai-completions" && strings.HasPrefix(model.ID, "openai/") {
				if openAIMidConvoModelIDs[strings.TrimPrefix(model.ID, "openai/")] {
					mergeCompat(model, map[string]any{"supportsMidConvoSystemMessages": true})
				}
			}
		},
	}
}

// ---- xAI -----------------------------------------------------------------

var xaiExcludedModelIDs = map[string]bool{
	"grok-3": true, "grok-3-fast": true, "grok-4.20-0309-non-reasoning": true,
	"grok-4.20-0309-reasoning": true, "grok-build-0.1": true, "grok-code-fast-1": true,
}

func xaiProvider() *providerDef {
	return &providerDef{
		id:              "xai",
		source:          "xai",
		api:             "openai-responses",
		baseURL:         providerregistry.DefaultEndpoint("xai", "openai-responses"),
		recordReasoning: true,
		include:         func(source *sourceModel) bool { return !xaiExcludedModelIDs[source.ID] },
		mapModel: func(_ *buildContext, model *catalogModel, _ *sourceModel) (*catalogModel, error) {
			mergeCompat(model, map[string]any{"supportsLongCacheRetention": false})
			return model, nil
		},
		finalize: func(model *catalogModel) {
			if model.API == "openai-responses" && model.ThinkingLevelMap == nil {
				mergeThinking(model, map[string]*string{"off": nil, "minimal": nil})
			}
		},
	}
}

// ---- Mistral -------------------------------------------------------------

func mistralProvider() *providerDef {
	return &providerDef{
		id:              "mistral",
		source:          "mistral",
		api:             "mistral-conversations",
		baseURL:         providerregistry.DefaultEndpoint("mistral", "mistral-conversations"),
		recordReasoning: false,
		mapModel: func(ctx *buildContext, model *catalogModel, source *sourceModel) (*catalogModel, error) {
			if levelMap := effortThinkingLevelMap(sourceEfforts(source)); levelMap != nil {
				model.ThinkingLevelMap = levelMap
			}
			return model, nil
		},
		addMissing: func() []*catalogModel {
			return []*catalogModel{{
				ID:            "mistral-medium-3.5",
				Name:          "Mistral Medium 3.5",
				API:           "mistral-conversations",
				BaseURL:       providerregistry.DefaultEndpoint("mistral", "mistral-conversations"),
				Reasoning:     true,
				Input:         []string{"text", "image"},
				ContextWindow: 262144,
				MaxTokens:     262144,
				ThinkingLevelMap: map[string]*string{
					"off": stringPointer("none"), "minimal": nil, "low": nil, "medium": nil,
					"high": stringPointer("high"), "xhigh": nil, "max": nil,
				},
			}}
		},
	}
}

func sourceEfforts(source *sourceModel) []*string {
	if source == nil {
		return nil
	}
	efforts := make([]*string, 0)
	for _, option := range source.ReasoningOptions {
		if option.isEffort() {
			efforts = append(efforts, option.values...)
		}
	}
	return efforts
}
