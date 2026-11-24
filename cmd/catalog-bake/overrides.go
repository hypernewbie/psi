package main

import (
	"fmt"
	"io"
	"sort"
)

// Reviewed limits come from the catalog review. They define psi's safe input
// context and output budget; they do not claim independent provider enforcement
// verification.
var reviewedLimitOverrides = map[string]map[string]limitOverride{
	"openai": {
		"gpt-5.4":                  {contextWindow: 922000, maxTokens: 128000},
		"gpt-5.4-pro":              {contextWindow: 922000, maxTokens: 128000},
		"gpt-5.5":                  {contextWindow: 922000, maxTokens: 128000},
		"gpt-5.5-pro":              {contextWindow: 922000, maxTokens: 128000},
		"gpt-5.6-luna":             {contextWindow: 922000, maxTokens: 128000},
		"gpt-5.6-sol":              {contextWindow: 922000, maxTokens: 128000},
		"gpt-5.6-terra":            {contextWindow: 922000, maxTokens: 128000},
		"gpt-6-astra":              {contextWindow: 922000, maxTokens: 128000},
		"gpt-6-cyber":              {contextWindow: 922000, maxTokens: 128000},
		"gpt-6-luna":               {contextWindow: 922000, maxTokens: 128000},
		"gpt-6-sol":                {contextWindow: 922000, maxTokens: 128000},
		"gpt-6.1-sol":              {contextWindow: 922000, maxTokens: 128000},
		"gpt-6.1-terra":            {contextWindow: 922000, maxTokens: 128000},
		"gpt-6.2-astra":            {contextWindow: 922000, maxTokens: 128000},
		"gpt-6.2-sol":              {contextWindow: 922000, maxTokens: 128000},
		"gpt-daybreak-blue-latest": {contextWindow: 922000, maxTokens: 128000},
	},
	"opencode-go": {
		"gpt-5.6-luna": {contextWindow: 922000, maxTokens: 128000},
		"gpt-6-luna":   {contextWindow: 922000, maxTokens: 128000},
		"hy3":          {contextWindow: 192000, maxTokens: 128000},
	},
}

type limitOverride struct {
	contextWindow int
	maxTokens     int
}

// applyReviewedLimitOverrides replaces only reviewed limit pairs on existing
// rows. Missing targets are warnings only for complete catalog candidates.
func applyReviewedLimitOverrides(rows map[string][]*catalogModel, requireComplete bool, warnings io.Writer) {
	var missing []string
	for providerID, providerOverrides := range reviewedLimitOverrides {
		models := rows[providerID]
		present := make(map[string]bool, len(providerOverrides))
		for _, model := range models {
			override, reviewed := providerOverrides[model.ID]
			if !reviewed {
				continue
			}
			model.ContextWindow = override.contextWindow
			model.MaxTokens = override.maxTokens
			present[model.ID] = true
		}
		if requireComplete {
			for modelID := range providerOverrides {
				if !present[modelID] {
					missing = append(missing, providerID+"/"+modelID)
				}
			}
		}
	}
	sort.Strings(missing)
	if warnings == nil {
		return
	}
	for _, key := range missing {
		_, _ = fmt.Fprintf(warnings, "catalog-bake: warning: limit override skipped for %s: model not in candidate catalog\n", key)
	}
}
