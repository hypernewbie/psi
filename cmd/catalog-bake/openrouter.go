package main

import (
	"fmt"
	"strings"

	"psi/internal/providerregistry"
)

// OpenRouter's default model listing. Only the fields the projection consumes
// are decoded; the response also carries prices, benchmarks, and descriptions.
type openRouterListing struct {
	Data []openRouterModel `json:"data"`
}

type openRouterModel struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	SupportedParameters []string `json:"supported_parameters"`
	Architecture        struct {
		Modality string `json:"modality"`
	} `json:"architecture"`
	TopProvider struct {
		ContextLength       int `json:"context_length"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	ContextLength int                  `json:"context_length"`
	Reasoning     *openRouterReasoning `json:"reasoning"`
}

type openRouterReasoning struct {
	Mandatory        bool      `json:"mandatory"`
	SupportedEfforts []*string `json:"supported_efforts"`
}

func (m openRouterModel) hasTools() bool {
	for _, parameter := range m.SupportedParameters {
		if parameter == "tools" {
			return true
		}
	}
	return false
}

func (m openRouterModel) hasReasoning() bool {
	for _, parameter := range m.SupportedParameters {
		if parameter == "reasoning" {
			return true
		}
	}
	return false
}

func (m openRouterModel) imageInput() bool {
	return strings.Contains(m.Architecture.Modality, "image")
}

func (m openRouterModel) isAnthropicMessages() bool {
	return (strings.HasPrefix(m.ID, "anthropic/") || strings.HasPrefix(m.ID, "~anthropic/")) && !strings.HasSuffix(m.ID, ":batch")
}

// openRouterThinkingLevelMap converts OpenRouter's reasoning metadata. A
// mandatory reasoner cannot disable thinking, so "off" maps to null.
func openRouterThinkingLevelMap(reasoning *openRouterReasoning) map[string]*string {
	if reasoning == nil {
		return nil
	}
	if len(reasoning.SupportedEfforts) == 0 {
		if reasoning.Mandatory {
			return map[string]*string{"off": nil}
		}
		return nil
	}
	levels := effortThinkingLevelMap(reasoning.SupportedEfforts)
	if levels == nil {
		if reasoning.Mandatory {
			return map[string]*string{"off": nil}
		}
		return nil
	}
	if reasoning.Mandatory {
		levels["off"] = nil
	} else {
		levels["off"] = stringPointer("none")
	}
	return levels
}

func normalizeOpenRouter(listing openRouterListing) ([]*catalogModel, error) {
	models := make([]*catalogModel, 0, len(listing.Data))
	for _, raw := range listing.Data {
		if !raw.hasTools() {
			continue
		}
		if raw.ID == "" {
			return nil, fmt.Errorf("OpenRouter listing has a model with an empty id")
		}
		// Pi classifies non-batch Anthropic entries for its Messages adapter.
		// psi does not register that route for OpenRouter, so those entries are
		// excluded instead of being converted to another protocol.
		if raw.isAnthropicMessages() {
			continue
		}
		switch {
		case raw.ContextLength < 0:
			return nil, fmt.Errorf("openrouter/%s: negative context_length", raw.ID)
		case raw.TopProvider.ContextLength < 0:
			return nil, fmt.Errorf("openrouter/%s: negative top_provider.context_length", raw.ID)
		case raw.TopProvider.MaxCompletionTokens < 0:
			return nil, fmt.Errorf("openrouter/%s: negative top_provider.max_completion_tokens", raw.ID)
		}
		input := []string{"text"}
		if raw.imageInput() {
			input = append(input, "image")
		}
		model := &catalogModel{
			ID:               raw.ID,
			Name:             raw.Name,
			API:              "openai-completions",
			BaseURL:          providerregistry.DefaultEndpoint("openrouter", "openai-completions"),
			ContextWindow:    firstPositive(raw.TopProvider.ContextLength, raw.ContextLength, 4096),
			MaxTokens:        firstPositive(raw.TopProvider.MaxCompletionTokens, 4096),
			Reasoning:        raw.hasReasoning(),
			Input:            input,
			ThinkingLevelMap: openRouterThinkingLevelMap(raw.Reasoning),
		}
		if model.Name == "" {
			model.Name = model.ID
		}
		model.provider = "openrouter"
		correctOpenRouterModel(model)
		models = append(models, model)
	}
	return models, nil
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// correctOpenRouterModel keeps the pinned gateway corrections. Cost changes are
// not projected, so only limits, compat, and thinking maps survive.
func correctOpenRouterModel(model *catalogModel) {
	if model.ID == "~moonshotai/kimi-latest" ||
		strings.HasPrefix(model.ID, "moonshotai/") && isKimiK3FamilyID(strings.TrimPrefix(model.ID, "moonshotai/")) {
		model.MaxTokens = 131072
	}
	if model.ID == "moonshotai/kimi-k2.5" {
		model.MaxTokens = 4096
	}
	if strings.HasPrefix(model.ID, "moonshotai/kimi-k2.6") {
		mergeCompat(model, map[string]any{
			"supportsDeveloperRole":                       false,
			"requiresReasoningContentOnAssistantMessages": true,
		})
	}
}

// openRouterCompatDefaults is the provider-level API default function. Model
// compat entries still win.
func openRouterCompatDefaults(model *catalogModel) map[string]any {
	compat := map[string]any{
		"thinkingFormat":             "openrouter",
		"supportsStrictMode":         true,
		"sendSessionAffinityHeaders": true,
	}
	if !strings.HasPrefix(model.ID, "anthropic/") && !strings.HasPrefix(model.ID, "openai/") {
		compat["supportsDeveloperRole"] = false
	}
	if strings.HasPrefix(model.ID, "anthropic/") || strings.HasPrefix(model.ID, "~anthropic/") {
		compat["cacheControlFormat"] = "anthropic"
	}
	return compat
}
