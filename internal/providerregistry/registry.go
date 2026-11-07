// Package providerregistry contains the explicit provider, protocol, and
// credential allowlist shared by configuration and request construction.
package providerregistry

import (
	"net/url"
	"sort"
	"strings"
)

type AuthKind string

const (
	AuthAPIKey     AuthKind = "api-key"
	AuthCodexOAuth AuthKind = "codex-oauth"
)

type Spec struct {
	ID          string
	Name        string
	Auth        AuthKind
	Environment string
	DefaultAPI  string
	Endpoints   map[string]string
}

var registry = map[string]Spec{
	"anthropic": {
		ID: "anthropic", Name: "Anthropic", Auth: AuthAPIKey,
		Environment: "ANTHROPIC_API_KEY", DefaultAPI: "anthropic-messages",
		Endpoints: map[string]string{"anthropic-messages": "https://api.anthropic.com"},
	},
	"openai-codex": {
		ID: "openai-codex", Name: "OpenAI Codex", Auth: AuthCodexOAuth,
		DefaultAPI: "openai-codex-responses",
		Endpoints:  map[string]string{"openai-codex-responses": "https://chatgpt.com/backend-api"},
	},
	"minimax": {
		ID: "minimax", Name: "MiniMax", Auth: AuthAPIKey,
		Environment: "MINIMAX_API_KEY", DefaultAPI: "anthropic-messages",
		Endpoints: map[string]string{"anthropic-messages": "https://api.minimax.io/anthropic"},
	},
	"opencode-go": {
		ID: "opencode-go", Name: "OpenCode Go", Auth: AuthAPIKey,
		Environment: "OPENCODE_API_KEY", DefaultAPI: "",
		Endpoints: map[string]string{
			"anthropic-messages":   "https://opencode.ai/inference/go",
			"google-generative-ai": "https://opencode.ai/inference/go/v1",
			"openai-completions":   "https://opencode.ai/inference/go/v1",
			"openai-responses":     "https://opencode.ai/inference/go/v1",
		},
	},
	"deepseek": {
		ID: "deepseek", Name: "DeepSeek", Auth: AuthAPIKey,
		Environment: "DEEPSEEK_API_KEY", DefaultAPI: "openai-completions",
		Endpoints: map[string]string{"openai-completions": "https://api.deepseek.com"},
	},
	"zai": {
		ID: "zai", Name: "Z.AI", Auth: AuthAPIKey,
		Environment: "ZAI_API_KEY", DefaultAPI: "openai-completions",
		Endpoints: map[string]string{"openai-completions": "https://api.z.ai/api/coding/paas/v4"},
	},
	"xiaomi": {
		ID: "xiaomi", Name: "Xiaomi MiMo", Auth: AuthAPIKey,
		Environment: "XIAOMI_API_KEY", DefaultAPI: "openai-completions",
		Endpoints: map[string]string{"openai-completions": "https://api.xiaomimimo.com/v1"},
	},
	"moonshotai": {
		ID: "moonshotai", Name: "Moonshot AI", Auth: AuthAPIKey,
		Environment: "MOONSHOT_API_KEY", DefaultAPI: "openai-completions",
		Endpoints: map[string]string{"openai-completions": "https://api.moonshot.ai/v1"},
	},
	"kimi-coding": {
		ID: "kimi-coding", Name: "Kimi For Coding", Auth: AuthAPIKey,
		Environment: "KIMI_API_KEY", DefaultAPI: "openai-completions",
		Endpoints: map[string]string{"openai-completions": "https://api.kimi.com/coding/v1"},
	},
	"qwen-token-plan": {
		ID: "qwen-token-plan", Name: "Qwen Token Plan", Auth: AuthAPIKey,
		Environment: "QWEN_TOKEN_PLAN_API_KEY", DefaultAPI: "openai-completions",
		Endpoints: map[string]string{"openai-completions": "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"},
	},
	"openrouter": {
		ID: "openrouter", Name: "OpenRouter", Auth: AuthAPIKey,
		Environment: "OPENROUTER_API_KEY", DefaultAPI: "openai-completions",
		Endpoints: map[string]string{"openai-completions": "https://openrouter.ai/api/v1"},
	},
	"xai": {
		ID: "xai", Name: "xAI", Auth: AuthAPIKey,
		Environment: "XAI_API_KEY", DefaultAPI: "openai-responses",
		Endpoints: map[string]string{"openai-responses": "https://api.x.ai/v1"},
	},
	"groq": {
		ID: "groq", Name: "Groq", Auth: AuthAPIKey,
		Environment: "GROQ_API_KEY", DefaultAPI: "openai-completions",
		Endpoints: map[string]string{"openai-completions": "https://api.groq.com/openai/v1"},
	},
	"mistral": {
		ID: "mistral", Name: "Mistral", Auth: AuthAPIKey,
		Environment: "MISTRAL_API_KEY", DefaultAPI: "mistral-conversations",
		Endpoints: map[string]string{"mistral-conversations": "https://api.mistral.ai"},
	},
	"google": {
		ID: "google", Name: "Google", Auth: AuthAPIKey,
		Environment: "GEMINI_API_KEY", DefaultAPI: "google-generative-ai",
		Endpoints: map[string]string{"google-generative-ai": "https://generativelanguage.googleapis.com/v1beta"},
	},
}

// opencodeCachePaths contains only base paths observed in the pinned public
// catalog and the Pi models-store schema used by this implementation. Matching
// is exact; a cache entry cannot choose an arbitrary origin or subpath.
var opencodeCachePaths = map[string]map[string]bool{
	"anthropic-messages": {
		"/inference/go":           true,
		"/inference/go/anthropic": true,
	},
	"google-generative-ai": {
		"/inference/go/v1":            true,
		"/inference/go/google/v1beta": true,
	},
	"openai-completions": {
		"/inference/go/v1":        true,
		"/inference/go/openai/v1": true,
	},
	"openai-responses": {
		"/inference/go/v1":        true,
		"/inference/go/openai/v1": true,
	},
}

func Lookup(id string) (Spec, bool) {
	spec, ok := registry[id]
	if !ok {
		return Spec{}, false
	}
	copy := spec
	copy.Endpoints = make(map[string]string, len(spec.Endpoints))
	for api, endpoint := range spec.Endpoints {
		copy.Endpoints[api] = endpoint
	}
	return copy, true
}

func IDs() []string {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func SupportsAPI(provider, api string) bool {
	spec, ok := registry[provider]
	if !ok || api == "" {
		return false
	}
	_, ok = spec.Endpoints[api]
	return ok
}

// genericAPIs lists the protocol adapters a provider declared in models.json
// can use. The Codex adapter is not part of it: that protocol needs the ChatGPT
// OAuth session instead of a generic API key.
var genericAPIs = map[string]bool{
	"anthropic-messages":    true,
	"google-generative-ai":  true,
	"mistral-conversations": true,
	"openai-completions":    true,
	"openai-responses":      true,
}

// SupportsGenericAPI reports whether a configured provider can use the given
// protocol adapter.
func SupportsGenericAPI(api string) bool {
	return genericAPIs[api]
}

func DefaultAPI(provider string) string {
	return registry[provider].DefaultAPI
}

func DefaultEndpoint(provider, api string) string {
	return registry[provider].Endpoints[api]
}

// TrustedCachedEndpoint permits only the exact OpenCode Go base paths observed
// in Pi's public model metadata. Cached URLs are never trusted for other
// providers; their endpoints come from the registry or explicit user config.
func TrustedCachedEndpoint(provider, api, raw string) bool {
	if provider != "opencode-go" || !SupportsAPI(provider, api) || raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "opencode.ai" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if u.RawPath != "" && u.RawPath != u.Path || strings.HasSuffix(u.Path, "/") {
		return false
	}
	return opencodeCachePaths[api][u.Path]
}
