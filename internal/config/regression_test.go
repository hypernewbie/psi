package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInheritedToolSelectionAndResourceBases(t *testing.T) {
	selected, err := ResolveTools([]string{"-write", "write", "read"}, nil)
	if err != nil || !reflect.DeepEqual(selected, []string{"read"}) {
		t.Fatalf("plain tool names must form the base before modifiers: %v %v", selected, err)
	}
	root := t.TempDir()
	user, project := filepath.Join(root, "user", "settings.json"), filepath.Join(root, "workspace", ".pi", "settings.json")
	writeJSON(t, user, `{"defaultTools":["read"],"skills":["user-skills"],"httpIdleTimeoutMs":0,"retry":{"maxRetries":2}}`)
	writeJSON(t, project, `{"defaultTools":["+bash"],"skills":["project-skills"]}`)
	loaded, err := LoadSettings(user, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Settings.DefaultTools, []string{"read", "bash"}) {
		t.Fatalf("inherited selection reset: %v", loaded.Settings.DefaultTools)
	}
	want := []string{filepath.Join(filepath.Dir(user), "user-skills"), filepath.Join(filepath.Dir(project), "project-skills")}
	if !reflect.DeepEqual(loaded.Settings.Skills, want) {
		t.Fatalf("skill paths/merge: %v", loaded.Settings.Skills)
	}
	if loaded.Settings.HTTPIdleTimeoutMs != 0 || loaded.Settings.MaxRetries != 2 {
		t.Fatal("network settings ignored")
	}
	writeJSON(t, project, `{"defaultTools":[]}`)
	loaded, err = LoadSettings(user, project, true)
	if err != nil || len(loaded.Settings.DefaultTools) != 0 {
		t.Fatal("empty tool list must disable tools")
	}
}

func TestPiModelsShapeAndEndpoint(t *testing.T) {
	p := filepath.Join(t.TempDir(), "models.json")
	writeJSON(t, p, `{"providers":{"openai":{"api":"openai-responses"},"anthropic":{"baseUrl":"http://localhost:9999/v1","models":[{"id":"custom","api":"anthropic-messages","contextWindow":12345,"maxTokens":1024,"reasoning":false}]}}}`)
	mf, err := LoadModels(p)
	if err != nil {
		t.Fatal(err)
	}
	m, err := mf.SelectModel("anthropic", "custom")
	if err != nil || m.ContextWindow != 12345 || m.MaxTokens != 1024 || m.BaseURL != "http://localhost:9999/v1" {
		t.Fatalf("Pi models entry ignored: %+v %v", m, err)
	}
	if _, err := mf.SelectModel("openai", "custom"); err == nil {
		t.Fatal("active unsupported provider accepted")
	}
}

func TestSystemReplacementAndAncestorOverride(t *testing.T) {
	root, agent := t.TempDir(), t.TempDir()
	ws := filepath.Join(root, "child")
	writeCfgFile(t, filepath.Join(root, "AGENTS.md"), "SHADOWED")
	writeCfgFile(t, filepath.Join(root, "AGENTS.override.md"), "OVERRIDE")
	writeCfgFile(t, filepath.Join(agent, "SYSTEM.md"), "CUSTOM_BASE")
	writeCfgFile(t, filepath.Join(ws, "AGENTS.md"), "WORKSPACE")
	files, err := DiscoverInstructions(ws, agent, false)
	if err != nil {
		t.Fatal(err)
	}
	prompt := AssemblePrompt(ws, files, nil, []string{"read"})
	if strings.Contains(prompt, "SHADOWED") || strings.Contains(prompt, "You are psi") || !strings.Contains(prompt, "OVERRIDE") || !strings.Contains(prompt, "CUSTOM_BASE") || !strings.Contains(prompt, "WORKSPACE") {
		t.Fatalf("instruction precedence: %s", prompt)
	}
}
