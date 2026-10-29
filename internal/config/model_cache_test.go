package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiCatalogCacheSelectionAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	cache := `{"unrelated-provider":{"models":"incompatible"},"openai-codex":{"lastModified":4102444800000,"models":[
		{"id":"malformed-model","contextWindow":"not a number"},
		{"id":"synthetic-new","provider":"openai-codex","api":"openai-codex-responses","type":"chat","contextWindow":500000,"maxTokens":128000,"reasoning":true,"input":["text","image"],"thinkingLevelMap":{"off":"none","xhigh":"xhigh"},"baseUrl":"https://untrusted.invalid","headers":{"Secret":"bad"}},
		{"id":"gpt-5.6-luna","provider":"openai-codex","api":"openai-codex-responses","contextWindow":333333,"maxTokens":12345},
		{"id":"wrong-provider","provider":"anthropic","api":"openai-codex-responses","contextWindow":1,"maxTokens":1},
		{"id":"wrong-api","provider":"openai-codex","api":"openai-responses","contextWindow":1,"maxTokens":1},
		{"id":"image-model","provider":"openai-codex","api":"openai-codex-responses","type":"image","contextWindow":1,"maxTokens":1}
	]}}`
	if err := os.WriteFile(filepath.Join(dir, "models-store.json"), []byte(cache), 0600); err != nil {
		t.Fatal(err)
	}
	mf, err := LoadModels(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mf.SelectModel("openai-codex", "synthetic-new")
	if err != nil || m.ContextWindow != 500000 || m.MaxTokens != 128000 || !m.Reasoning || !m.InputImage || m.ThinkingLevelMap["off"] == nil || *m.ThinkingLevelMap["off"] != "none" || m.BaseURL != "" || len(m.Headers) != 0 {
		t.Fatalf("cached model: %+v, %v", m, err)
	}
	m, err = mf.SelectModel("openai-codex", "gpt-5.6-luna")
	if err != nil || m.ContextWindow != 333333 {
		t.Fatalf("overlay did not replace bundle: %+v %v", m, err)
	}
	for _, id := range []string{"wrong-provider", "wrong-api", "image-model"} {
		if _, err := mf.SelectModel("openai-codex", id); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	found := false
	for _, candidate := range mf.LocalModels("openai-codex") {
		if candidate.ID == "synthetic-new" {
			found = true
		}
	}
	if !found {
		t.Fatal("model picker omitted cached model")
	}
	mf.Providers = map[string]ProviderMeta{"openai-codex": {Models: []ModelMeta{{ID: "synthetic-new", ContextWindow: 42, MaxTokens: 20}}}}
	m, err = mf.SelectModel("openai-codex", "synthetic-new")
	if err != nil || m.ContextWindow != 42 {
		t.Fatalf("explicit model did not win: %+v %v", m, err)
	}
}

func TestPiCatalogCacheOldOrMalformedIsIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models-store.json")
	for _, data := range []string{`{`, `{"openai-codex":{"lastModified":1,"models":[{"id":"old","provider":"openai-codex","api":"openai-codex-responses","contextWindow":1,"maxTokens":1}]}}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		mf, err := LoadModels(filepath.Join(dir, "models.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mf.SelectModel("openai-codex", "old"); err == nil || !strings.Contains(err.Error(), "needs explicit metadata") {
			t.Fatalf("old cache accepted: %v", err)
		}
		if _, err := mf.SelectModel("openai-codex", "gpt-5.6-luna"); err != nil {
			t.Fatal(err)
		}
	}
}
