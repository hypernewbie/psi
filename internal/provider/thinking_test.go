package provider

import (
	"strings"
	"testing"
)

func TestThinkingChoices(t *testing.T) {
	tests := []struct {
		name          string
		model         ModelMeta
		expectChoices []string
	}{
		{
			name: "non-reasoning model",
			model: ModelMeta{
				ID:        "claude-3-5-sonnet",
				API:       "anthropic-messages",
				Reasoning: false,
			},
			expectChoices: []string{"off"},
		},
		{
			name: "anthropic reasoning model",
			model: ModelMeta{
				ID:        "claude-3-7-sonnet",
				API:       "anthropic-messages",
				Reasoning: true,
				MaxTokens: 64000,
			},
			expectChoices: []string{"off", "low", "medium", "high"},
		},
		{
			name: "google reasoning model with level map",
			model: ModelMeta{
				ID:               "gemini-2.5-flash",
				Provider:         "google",
				API:              "google-generative-ai",
				Reasoning:        true,
				ThinkingLevelMap: map[string]*string{"off": nil, "low": completionString("low"), "high": completionString("high")},
			},
			expectChoices: []string{"low", "high"},
		},
		{
			name: "openai-responses reasoning model",
			model: ModelMeta{
				ID:               "o3-mini",
				Provider:         "opencode-go",
				API:              "openai-responses",
				Reasoning:        true,
				ThinkingLevelMap: map[string]*string{"low": completionString("low"), "medium": completionString("medium"), "high": completionString("high")},
			},
			expectChoices: []string{"low", "medium", "high"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ThinkingChoices(tc.model)
			for _, exp := range tc.expectChoices {
				if !strings.Contains(got, exp) {
					t.Errorf("ThinkingChoices(%v) = %q, want containing %q", tc.model.ID, got, exp)
				}
			}
		})
	}
}
