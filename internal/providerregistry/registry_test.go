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
		{"minimax", "anthropic-messages", "MINIMAX_API_KEY", "https://api.minimax.io/anthropic"},
		{"deepseek", "openai-completions", "DEEPSEEK_API_KEY", "https://api.deepseek.com"},
		{"zai", "openai-completions", "ZAI_API_KEY", "https://api.z.ai/api/coding/paas/v4"},
		{"google", "google-generative-ai", "GEMINI_API_KEY", "https://generativelanguage.googleapis.com/v1beta"},
		{"opencode-go", "openai-responses", "OPENCODE_API_KEY", "https://opencode.ai/inference/go/v1"},
	} {
		spec, ok := Lookup(tc.provider)
		if !ok || !SupportsAPI(tc.provider, tc.api) || spec.Environment != tc.environment || DefaultEndpoint(tc.provider, tc.api) != tc.endpoint {
			t.Fatalf("registry entry %q = %+v", tc.provider, spec)
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
