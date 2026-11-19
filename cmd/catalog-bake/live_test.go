//go:build catalog_bake_live

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"psi/internal/provider"
	"psi/internal/providerregistry"
)

// TestLiveGroqBake proves the fetch, normalization, validation, serialization,
// and staging path against the real public catalogs. It writes a one-provider
// artifact into a test directory as proof only; the public command is not
// wired to accept a partial catalog.
func TestLiveGroqBake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	inputs, err := fetchCatalogInputs(ctx)
	if err != nil {
		t.Fatalf("live fetch failed: %v", err)
	}
	generatedAt := time.Now().UTC()
	catalogBytes, sourceBytes, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, func() time.Time { return generatedAt })
	if err != nil {
		t.Fatalf("bake failed: %v", err)
	}

	parent := t.TempDir()
	out := filepath.Join(parent, "groq-proof")
	if err := publish(ctx, out, map[string][]byte{
		"catalog.json":        catalogBytes,
		"catalog-source.json": sourceBytes,
	}); err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	publishedCatalog, err := os.ReadFile(filepath.Join(out, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	publishedSource, err := os.ReadFile(filepath.Join(out, "catalog-source.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publishedCatalog, catalogBytes) || !bytes.Equal(publishedSource, sourceBytes) {
		t.Fatal("published bytes differ from the assembled artifacts")
	}

	var rows map[string][]*catalogModel
	if err := json.Unmarshal(publishedCatalog, &rows); err != nil {
		t.Fatal(err)
	}
	groq := rows["groq"]
	if len(groq) == 0 {
		t.Fatal("live Groq catalog has no rows")
	}
	endpoint := providerregistry.DefaultEndpoint("groq", "openai-completions")
	toolRequests := 0
	for _, row := range groq {
		// The provider is the published map key, not a row field.
		row.provider = "groq"
		if row.BaseURL != endpoint {
			t.Errorf("%s: base URL %q is not the registered endpoint", row.ID, row.BaseURL)
		}
		if row.ContextWindow <= 0 || row.MaxTokens <= 0 {
			t.Errorf("%s: non-positive limits (%d, %d)", row.ID, row.ContextWindow, row.MaxTokens)
		}
		meta := providerMeta(row)
		for _, level := range provider.SupportedThinking(meta) {
			body, err := provider.BuildRequest(validationRequest(meta, level))
			if err != nil {
				t.Errorf("%s: thinking %q: %v", row.ID, level, err)
				continue
			}
			if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
				toolRequests++
			}
		}
	}
	if toolRequests == 0 {
		t.Fatal("no Groq row accepted a tool-bearing request")
	}

	var provenance catalogProvenance
	if err := json.Unmarshal(publishedSource, &provenance); err != nil {
		t.Fatal(err)
	}
	if got, want := provenance.Inputs[modelsDevInputName].SHA256, sha256Hex(inputs.modelsDevBody); got != want {
		t.Fatalf("models.dev digest = %s, want %s", got, want)
	}
	if got, want := provenance.Inputs[openRouterInputName].SHA256, sha256Hex(inputs.openRouterBody); got != want {
		t.Fatalf("OpenRouter digest = %s, want %s", got, want)
	}
	if got, want := provenance.CatalogSHA256, sha256Hex(publishedCatalog); got != want {
		t.Fatalf("catalog digest = %s, want %s", got, want)
	}
	generated, err := time.Parse(time.RFC3339Nano, provenance.GeneratedAt)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range provenance.Inputs {
		fetchedAt, err := time.Parse(time.RFC3339Nano, input.FetchedAt)
		if err != nil {
			t.Fatalf("%s fetchedAt: %v", name, err)
		}
		if generated.Before(fetchedAt) {
			t.Fatalf("generatedAt %s is earlier than %s fetchedAt %s", generated, name, fetchedAt)
		}
	}
	t.Logf("live Groq rows=%d models.dev_sha256=%s catalog_sha256=%s tool_requests=%d",
		len(groq), provenance.Inputs[modelsDevInputName].SHA256, provenance.CatalogSHA256, toolRequests)
}
