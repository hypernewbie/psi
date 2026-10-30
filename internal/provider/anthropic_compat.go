package provider

import "fmt"

// Existing Anthropic behavior stays unchanged. Additional Anthropic-compatible
// providers must not silently accept catalog features the adapter cannot replay.
func resolveAdditionalAnthropicCompat(model ModelMeta) (ModelMeta, error) {
	if model.ProviderID() != "minimax" && model.ProviderID() != "opencode-go" {
		return model, nil
	}
	for field := range model.Compat {
		switch field {
		case "forceAdaptiveThinking", "allowEmptySignature", "supportsMidConvoEffort",
			"supportsTemperature", "supportsStrictTools", "supportsEagerToolInputStreaming",
			"supportsLongCacheRetention", "supportsCacheControlOnTools",
			"supportsMidConvoSystemMessages", "supportsMidConvoToolChanges", "sendSessionAffinityHeaders":
		default:
			return model, fmt.Errorf("model %q uses unsupported anthropic-messages compatibility field %q", model.ID, field)
		}
		if model.Compat.IsNull(field) {
			continue
		}
		value, hasValue, _ := model.Compat.Bool(field)
		if !hasValue {
			return model, fmt.Errorf("model %q has invalid anthropic-messages compatibility field %q", model.ID, field)
		}
		switch field {
		case "forceAdaptiveThinking":
			model.AdaptiveThinking = value
		case "allowEmptySignature":
			model.AllowEmptySignature = value
		case "supportsMidConvoEffort":
			model.MidConvoEffort = value
		case "supportsMidConvoSystemMessages", "supportsMidConvoToolChanges", "sendSessionAffinityHeaders":
			if value {
				return model, fmt.Errorf("model %q requires unsupported anthropic-messages compatibility feature %q", model.ID, field)
			}
		}
	}
	return model, nil
}
