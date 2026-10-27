package config

// Bundled subset of Pi's openai-codex catalog. Keep this local: startup must not
// depend on a Node installation, a catalog download, or the temporary checkout.
func codexModel(id string) (ModelMeta, bool) {
	if id == "" {
		id = "gpt-5.6-luna"
	}
	m := ModelMeta{ID: id, API: "openai-codex-responses", ContextWindow: 272000, MaxTokens: 128000, Reasoning: true, InputImage: true,
		ThinkingLevelMap: map[string]*string{"minimal": stringPtr("low"), "xhigh": stringPtr("xhigh"), "max": stringPtr("max")}}
	switch id {
	case "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-luna", "gpt-6-sol":
		return m, true
	case "gpt-6-astra":
		m.ThinkingLevelMap["off"] = nil
		return m, true
	case "gpt-6.1-sol":
		m.ContextWindow = 500000
		m.ThinkingLevelMap["off"] = nil
		return m, true
	default:
		return ModelMeta{ID: id, API: "openai-codex-responses"}, false
	}
}

func stringPtr(s string) *string { return &s }
