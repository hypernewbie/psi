package provider

import (
	"encoding/json"
	"strings"
)

// Anthropic beta features. The endpoint rejects a request body that uses a
// beta feature without declaring it in the anthropic-beta header, so the
// header must describe exactly what the body sends. The pinned Pi rules
// decide the set.
const (
	anthropicBetaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14"
	anthropicBetaInterleavedThinking      = "interleaved-thinking-2025-05-14"
	anthropicBetaServerSideFallback       = "server-side-fallback-2026-07-01"
	anthropicBetaMidConversationOutput    = "mid-conversation-output-config-2026-07-01"
	anthropicBetaThinkingBindingControls  = "thinking-binding-controls-2026-08-01"
)

// anthropicBetaFeatures returns the anthropic-beta features for one request.
//
// A configured anthropic-beta header replaces the computed set entirely, and
// an empty configured value disables the header. That mirrors the pinned Pi
// rules, where the header is the only way to opt in or out.
func anthropicBetaFeatures(model ModelMeta, req Request, headers map[string]string) []string {
	if _, configured := headerValue(headers, "anthropic-beta"); configured {
		// The configured header replaces the computed set. The caller sends it.
		return nil
	}
	var features []string
	if len(req.Tools) > 0 && !boolCompat(model, "supportsEagerToolInputStreaming", true) {
		features = append(features, anthropicBetaFineGrainedToolStreaming)
	}
	// Replayed thinking blocks need the interleaved-thinking feature. Models
	// with adaptive thinking carry their effort differently and do not use it.
	if model.Reasoning && req.Thinking != "off" && !model.AdaptiveThinking {
		features = append(features, anthropicBetaInterleavedThinking)
	}
	if listCompat(model, "allowedFallbackModels") > 0 {
		features = append(features, anthropicBetaServerSideFallback)
	}
	// Managed effort sends a per-message effort marker and the thinking block
	// binding controls. Both need their feature declared.
	if model.AdaptiveThinking && model.MidConvoEffort {
		features = append(features, anthropicBetaMidConversationOutput, anthropicBetaThinkingBindingControls)
	}
	return features
}

// headerValue returns a header value by case-insensitive name.
func headerValue(headers map[string]string, name string) (string, bool) {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

// boolCompat reads a boolean compatibility flag, falling back to the
// provider default when the model does not set it.
func boolCompat(model ModelMeta, name string, fallback bool) bool {
	if value, hasValue, present := model.Compat.Bool(name); present && hasValue {
		return value
	}
	return fallback
}

// listCompat reads a compatibility list and returns its length.
func listCompat(model ModelMeta, name string) int {
	raw, ok := model.Compat.Raw(name)
	if !ok {
		return 0
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return 0
	}
	return len(items)
}
