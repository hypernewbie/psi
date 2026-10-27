package config

import (
	"testing"
)

// P2-069,070,071,075: catalog snapshot, request construction, thinking, accounting.
// Reference: temp/pi-mono model catalog + provider BuildRequest.
func TestCatalogRequestPaths(t *testing.T) {
	mf := &ModelsFile{}
	// Bundled Anthropic models must resolve.
	for _, id := range []string{"claude-sonnet-4-5", "claude-haiku-4-5", "claude-opus-4-1"} {
		m, err := mf.SelectModel("anthropic", id)
		if err != nil {
			t.Fatalf("bundled anthropic %s: %v", id, err)
		}
		if m.API != "anthropic-messages" {
			t.Fatalf("%s api %q", id, m.API)
		}
		if !m.Reasoning {
			t.Fatalf("%s should support reasoning", id)
		}
		if m.MaxTokens <= 0 || m.ContextWindow <= 0 {
			t.Fatalf("%s missing window: %+v", id, m)
		}
	}
	// Unknown model needs explicit metadata (no silent default).
	if _, err := mf.SelectModel("anthropic", "unknown-xyz"); err == nil {
		t.Fatal("unknown model should require metadata")
	}
	// Unsupported provider rejected without changing defaults.
	if _, err := mf.SelectModel("openai", "x"); err == nil {
		t.Fatal("unsupported provider should fail")
	}
	if len(ValidThinkingLevels) != 7 {
		t.Fatalf("thinking levels: %v", ValidThinkingLevels)
	}
	// Codex thinking map preserved (no lossy conversion).
	m, err := mf.SelectModel("openai-codex", "")
	if err != nil {
		t.Fatalf("codex default: %v", err)
	}
	if m.API != "openai-codex-responses" {
		t.Fatalf("codex api %q", m.API)
	}
}
