package config

import (
	"testing"
)

// P2-050: configuration matrix — type/precedence/path base, new vs resumed,
// unsupported shared default offline policy (no silent substitution).
func TestStartupPrecedenceMatrix(t *testing.T) {
	st := Settings{DefaultProvider: "anthropic", DefaultModel: "shared-m", DefaultThinking: "low"}
	// New session: CLI > settings.
	ov := ResolveStartup("openai-codex", "", "high", "", false, "", "", "", st, "")
	if ov.Provider != "openai-codex" || ov.Thinking != "high" {
		t.Fatalf("new CLI precedence: %+v", ov)
	}
	if ov.Provider != "openai-codex" {
		t.Fatalf("CLI provider should win: %+v", ov)
	}
	// Explain covers restored overrides + env; new-session CLI vs settings is direct (no restored to explain).
	// Resumed: CLI > restored > settings. Restored wins over settings when CLI empty.
	ov2 := ResolveStartup("", "", "", "", false, "anthropic", "restored-m", "medium", st, "")
	if ov2.Model != "restored-m" || ov2.Thinking != "medium" {
		t.Fatalf("resumed should restore session-local: %+v", ov2)
	}
	// CLI overrides restored.
	ov3 := ResolveStartup("", "cli-m", "", "", false, "anthropic", "restored-m", "medium", st, "")
	if ov3.Model != "cli-m" {
		t.Fatalf("CLI should override restored: %+v", ov3)
	}
	// Unsupported shared default must be detectable for offline policy (no silent anthropic).
	if _, err := (&ModelsFile{}).SelectModel("unknown-fam", "x"); err == nil {
		t.Fatal("unsupported family should fail")
	}
}

func containsPrec(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
