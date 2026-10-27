package config

func codexModel(id string) (ModelMeta, bool) {
	if id == "" {
		id = "gpt-5.6-luna"
	}
	if m, ok := bundledModel("openai-codex", id); ok {
		return m, true
	}
	return ModelMeta{ID: id, API: "openai-codex-responses"}, false
}
func stringPtr(s string) *string { return &s }
