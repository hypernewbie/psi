package provider

import (
	"fmt"
	"strings"
)

func SupportedThinking(model ModelMeta) []string {
	var levels []string
	for _, level := range ValidLevels {
		if (model.API == "google-generative-ai" || model.API == "openai-responses" || model.API == "openai-completions") && model.Reasoning {
			mapped, exists := model.ThinkingLevelMap[level]
			if exists && mapped == nil {
				continue
			}
			if (level == "xhigh" || level == "max") && !exists {
				continue
			}
		}
		if _, err := BuildRequest(Request{Model: model, Thinking: level}); err == nil {
			levels = append(levels, level)
		}
	}
	return levels
}
func ThinkingDescription(model ModelMeta, level string) string {
	request, err := BuildRequest(Request{Model: model, Thinking: level})
	if err != nil {
		return "unavailable: " + err.Error()
	}
	if model.API == "openai-codex-responses" {
		if r, ok := request["reasoning"].(map[string]any); ok {
			return fmt.Sprintf("%s (effective effort %s)", level, r["effort"])
		}
		return level + " (provider default; disabling reasoning unavailable)"
	}
	if model.API == "google-generative-ai" {
		if level == "off" {
			config, _ := request["generationConfig"].(map[string]any)
			thinking, _ := config["thinkingConfig"].(map[string]any)
			if budget, ok := thinking["thinkingBudget"].(int); ok && budget == 0 {
				return "off"
			}
			if mapped, ok := thinking["thinkingLevel"].(string); ok {
				return fmt.Sprintf("off (effective fallback level %s; disabling reasoning unavailable)", mapped)
			}
			return "off"
		}
		config, _ := request["generationConfig"].(map[string]any)
		thinking, _ := config["thinkingConfig"].(map[string]any)
		if budget, ok := thinking["thinkingBudget"].(int); ok {
			if budget < 0 {
				return level + " (provider-managed thinking budget)"
			}
			return fmt.Sprintf("%s (budget %d tokens)", level, budget)
		}
		if mapped, ok := thinking["thinkingLevel"].(string); ok {
			return fmt.Sprintf("%s (effective Google thinking level %s)", level, mapped)
		}
		return level + " (provider default)"
	}
	if model.API == "openai-responses" {
		if request["reasoning"] == nil {
			return level + " (provider default)"
		}
		if config, ok := request["reasoning"].(map[string]any); ok {
			if level == "off" && config["effort"] == "none" {
				return "off (effective effort none)"
			}
			return fmt.Sprintf("%s (effective effort %v)", level, config["effort"])
		}
	}
	if model.API == "openai-completions" {
		effective, ok := clampModelThinkingLevel(model, level)
		if !ok {
			return "unavailable: model has no supported thinking level"
		}
		if effective != level {
			return fmt.Sprintf("%s (effective fallback %s)", level, ThinkingDescription(model, effective))
		}
		compat, err := resolveCompletionsCompat(model)
		if err != nil {
			return "unavailable: " + err.Error()
		}
		if level == "off" {
			switch compat.thinkingFormat {
			case "deepseek":
				if mapped, exists := model.ThinkingLevelMap["off"]; exists && mapped == nil {
					return "off (provider default; disabling reasoning unavailable)"
				}
				return "off"
			case "zai":
				return "off"
			default:
				if mapped, exists := model.ThinkingLevelMap["off"]; exists && mapped != nil {
					return fmt.Sprintf("off (effective effort %s)", *mapped)
				}
				return "off (provider default; disabling reasoning unavailable)"
			}
		}
		if compat.supportsReasoningEffort {
			if effort, ok := mappedEffort(model, level); ok {
				return fmt.Sprintf("%s (effective effort %s)", level, effort)
			}
		}
		if compat.thinkingFormat == "deepseek" || compat.thinkingFormat == "zai" {
			return level + " (thinking enabled; provider effort default)"
		}
		return level + " (provider effort default; effort control unavailable)"
	}
	if model.AdaptiveThinking {
		if level == "off" {
			if thinking, ok := request["thinking"].(map[string]any); ok {
				if thinking["type"] == "disabled" {
					return "off"
				}
				effort, _ := anthropicEffort(model, level)
				return fmt.Sprintf("off (effective default effort %s; disabling reasoning unavailable)", effort)
			}
			return level + " (provider default; disabling reasoning unavailable)"
		}
		effort, _ := anthropicEffort(model, level)
		return fmt.Sprintf("%s (effective effort %s)", level, effort)
	}
	if level == "off" {
		return level
	}
	budget, _ := ThinkingBudget(level)
	return fmt.Sprintf("%s (budget %d tokens)", level, budget)
}
func ThinkingChoices(model ModelMeta) string { return strings.Join(SupportedThinking(model), ", ") }

// Pi clamps to the nearest supported level, preferring a higher level before
// searching lower levels. A null mapping excludes a level; xhigh and max need
// explicit mappings. This is shared by the three PLAN6 protocol adapters.
func clampModelThinkingLevel(model ModelMeta, requested string) (string, bool) {
	if !model.Reasoning {
		return "off", true
	}
	available := func(level string) bool {
		mapped, exists := model.ThinkingLevelMap[level]
		return (!exists || mapped != nil) && (exists || level != "xhigh" && level != "max")
	}
	for i, level := range ValidLevels {
		if level != requested {
			continue
		}
		for j := i; j < len(ValidLevels); j++ {
			if available(ValidLevels[j]) {
				return ValidLevels[j], true
			}
		}
		for j := i - 1; j >= 0; j-- {
			if available(ValidLevels[j]) {
				return ValidLevels[j], true
			}
		}
		break
	}
	return "", false
}
