package agent

import (
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
)

func TestReviewNewProviderReplayRefusesArgumentsError(t *testing.T) {
	argumentError := "synthetic malformed arguments"
	for _, selection := range []struct{ provider, api string }{
		{"minimax", "anthropic-messages"}, {"opencode-go", "anthropic-messages"},
		{"deepseek", "openai-completions"}, {"zai", "openai-completions"},
		{"google", "google-generative-ai"}, {"opencode-go", "openai-responses"},
	} {
		t.Run(selection.provider+"/"+selection.api, func(t *testing.T) {
			cfg := Config{Model: provider.ModelMeta{Provider: selection.provider, API: selection.api, ID: "synthetic"}}
			message := session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Content: []session.ContentBlock{
				{Type: "toolCall", Call: &session.ToolCall{ID: "call", Name: "write", Arguments: map[string]any{}, ArgumentsError: &argumentError}},
			}}}
			if _, err := (&Loop{}).buildRequest([]session.AgentMessage{message}, nil, cfg); err == nil {
				t.Fatal("unsafe argumentsError was discarded before provider replay")
			}
		})
	}
}
