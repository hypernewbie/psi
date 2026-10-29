package providerregistry

import (
	"reflect"
	"testing"
)

func TestProviderRegistryIsExplicitAndTableDriven(t *testing.T) {
	want := []string{"anthropic", "deepseek", "google", "minimax", "openai-codex", "opencode-go", "zai"}
	if got := IDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider IDs = %v, want %v", got, want)
	}
	for _, tc := range []struct {
		provider, api, environment, endpoint string
	}{
		{"anthropic", "anthropic-messages", "ANTHROPIC_API_KEY", "https://api.anthropic.com"},
		{"openai-codex", "openai-codex-responses", "", "https://chatgpt.com/backend-api"},
		{"minimax", "anthropic-messages", "MINIMAX_API_KEY", "https://api.minimax.io/anthropic"},
		{"opencode-go", "anthropic-messages", "OPENCODE_API_KEY", "https://opencode.ai/inference/go"},
		{"opencode-go", "google-generative-ai", "OPENCODE_API_KEY", "https://opencode.ai/inference/go/v1"},
		{"opencode-go", "openai-completions", "OPENCODE_API_KEY", "https://opencode.ai/inference/go/v1"},
		{"opencode-go", "openai-responses", "OPENCODE_API_KEY", "https://opencode.ai/inference/go/v1"},
		{"deepseek", "openai-completions", "DEEPSEEK_API_KEY", "https://api.deepseek.com"},
		{"zai", "openai-completions", "ZAI_API_KEY", "https://api.z.ai/api/coding/paas/v4"},
		{"google", "google-generative-ai", "GEMINI_API_KEY", "https://generativelanguage.googleapis.com/v1beta"},
	} {
		spec, ok := Lookup(tc.provider)
		if !ok || !SupportsAPI(tc.provider, tc.api) || spec.Environment != tc.environment || DefaultEndpoint(tc.provider, tc.api) != tc.endpoint {
			t.Fatalf("registry entry %q = %+v", tc.provider, spec)
		}
	}
	wantAPIs := map[string][]string{
		"anthropic":    {"anthropic-messages"},
		"openai-codex": {"openai-codex-responses"},
		"minimax":      {"anthropic-messages"},
		"opencode-go":  {"anthropic-messages", "google-generative-ai", "openai-completions", "openai-responses"},
		"deepseek":     {"openai-completions"},
		"zai":          {"openai-completions"},
		"google":       {"google-generative-ai"},
	}
	for _, providerID := range want {
		spec, _ := Lookup(providerID)
		if len(spec.Endpoints) != len(wantAPIs[providerID]) {
			t.Fatalf("registry API count for %s = %d, want %v", providerID, len(spec.Endpoints), wantAPIs[providerID])
		}
		for _, api := range wantAPIs[providerID] {
			if !SupportsAPI(providerID, api) {
				t.Errorf("registry omitted %s/%s", providerID, api)
			}
		}
	}
	if SupportsAPI("opencode-go", "openai-codex-responses") || SupportsAPI("not-a-provider", "anthropic-messages") {
		t.Fatal("registry admitted an unlisted provider/API pair")
	}
}

func TestTrustedOpenCodeCacheEndpointIsExactAndAPIScoped(t *testing.T) {
	for _, tc := range []struct {
		api, endpoint string
		want          bool
	}{
		{"anthropic-messages", "https://opencode.ai/inference/go/anthropic", true},
		{"anthropic-messages", "https://opencode.ai/inference/go", true},
		{"google-generative-ai", "https://opencode.ai/inference/go/google/v1beta", true},
		{"openai-completions", "https://opencode.ai/inference/go/openai/v1", true},
		{"openai-responses", "https://opencode.ai/inference/go/v1", true},
		{"openai-responses", "http://opencode.ai/inference/go/v1", false},
		{"openai-responses", "https://user@opencode.ai/inference/go/v1", false},
		{"openai-responses", "https://opencode.ai.evil.invalid/inference/go/v1", false},
		{"openai-responses", "https://opencode.ai:443/inference/go/v1", false},
		{"openai-responses", "https://opencode.ai/inference/go/v1/extra", false},
		{"google-generative-ai", "https://opencode.ai/inference/go/openai/v1", false},
		{"openai-responses", "https://opencode.ai/inference/go/v1?next=https://attacker.invalid", false},
	} {
		if got := TrustedCachedEndpoint("opencode-go", tc.api, tc.endpoint); got != tc.want {
			t.Errorf("TrustedCachedEndpoint(%q,%q) = %t, want %t", tc.api, tc.endpoint, got, tc.want)
		}
	}
	if TrustedCachedEndpoint("deepseek", "openai-completions", "https://opencode.ai/inference/go/v1") {
		t.Fatal("trusted a cached endpoint for a non-OpenCode provider")
	}
}
