package provider

import (
	"fmt"
	"strings"
)

func SupportedThinking(model ModelMeta) []string {
	var levels []string
	for _, level := range ValidLevels {
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
