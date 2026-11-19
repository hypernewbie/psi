//go:build catalog_bake_live

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/providerregistry"
)

// TestRetainedCatalogBake replays the captured public inputs through the
// production normalizer, validator, serializer, and publisher. Fixed provenance
// values make byte-for-byte replay checks meaningful; it never turns capture
// mtimes into freshness claims.
func TestRetainedCatalogBake(t *testing.T) {
	dir := os.Getenv("PSI_CATALOG_BAKE_CAPTURE_DIR")
	if dir == "" {
		t.Fatal("PSI_CATALOG_BAKE_CAPTURE_DIR is required")
	}
	modelsDevBody, err := os.ReadFile(filepath.Join(dir, "models-dev-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	openRouterBody, err := os.ReadFile(filepath.Join(dir, "openrouter-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Date(2026, 11, 19, 13, 1, 0, 0, time.UTC)
	generatedAt := time.Date(2026, 11, 19, 13, 2, 0, 0, time.UTC)
	inputs := fetchedInputs{
		modelsDevBody:       modelsDevBody,
		modelsDevFetchedAt:  fetchedAt,
		openRouterBody:      openRouterBody,
		openRouterFetchedAt: fetchedAt.Add(2 * time.Second),
	}
	fixedNow := func() time.Time { return generatedAt }
	catalogBytes, sourceBytes, err := assembleArtifacts(inputs, providerDefinitions(), true, fixedNow)
	if err != nil {
		t.Fatalf("retained bake failed: %v", err)
	}

	replayedCatalog, replayedSource, err := assembleArtifacts(inputs, providerDefinitions(), true, fixedNow)
	if err != nil {
		t.Fatalf("retained replay failed: %v", err)
	}
	if !bytes.Equal(catalogBytes, replayedCatalog) || !bytes.Equal(sourceBytes, replayedSource) {
		t.Fatal("identical inputs and provenance produced different bytes")
	}

	out := filepath.Join(t.TempDir(), "retained")
	if err := publish(context.Background(), out, map[string][]byte{
		"catalog.json":        catalogBytes,
		"catalog-source.json": sourceBytes,
	}); err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile(filepath.Join(out, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, catalogBytes) {
		t.Fatal("published catalog differs from the assembled bytes")
	}

	var rows map[string][]*catalogModel
	if err := json.Unmarshal(published, &rows); err != nil {
		t.Fatal(err)
	}
	registryIDs := providerregistry.IDs()
	rowIDs := make([]string, 0, len(rows))
	for id := range rows {
		rowIDs = append(rowIDs, id)
	}
	sort.Strings(rowIDs)
	if strings.Join(rowIDs, ",") != strings.Join(registryIDs, ",") {
		t.Fatalf("providers = %v, want %v", rowIDs, registryIDs)
	}

	requestChecks := 0
	for _, providerID := range registryIDs {
		seen := map[string]bool{}
		for _, row := range rows[providerID] {
			if seen[row.ID] {
				t.Errorf("%s: duplicate id %q", providerID, row.ID)
			}
			seen[row.ID] = true
			row.provider = providerID
			if row.BaseURL == "" {
				t.Errorf("%s/%s: empty base URL", providerID, row.ID)
			}
			meta := providerMeta(row)
			levels := provider.SupportedThinking(meta)
			if len(levels) == 0 {
				t.Errorf("%s/%s: no thinking levels", providerID, row.ID)
				continue
			}
			for _, level := range levels {
				if _, err := provider.BuildRequest(validationRequest(meta, level)); err != nil {
					t.Errorf("%s/%s thinking %q: %v", providerID, row.ID, level, err)
					continue
				}
				requestChecks++
			}
		}
	}

	for _, expected := range []struct {
		provider string
		id       string
	}{
		{"anthropic", "claude-sonnet-4-5"},
		{"minimax", "MiniMax-M2.7"},
	} {
		if rowByID(rows[expected.provider], expected.id) == nil {
			t.Errorf("%s: captured deprecated default %q was filtered out", expected.provider, expected.id)
		}
	}

	openRouterCount := len(rows["openrouter"])
	if openRouterCount != 399 {
		t.Errorf("openrouter rows = %d, want 399 on the captured bytes", openRouterCount)
	}
	const opencodeID = "opencode-go"
	namedRoutes := map[string]struct {
		api     string
		baseURL string
		compat  map[string]any
	}{
		"gpt-5.6-luna":   {"openai-responses", "https://opencode.ai/inference/go/openai/v1", map[string]any{"sessionAffinityFormat": "openai-nosession"}},
		"qwen3.8-flash":  {"anthropic-messages", "https://opencode.ai/inference/go/anthropic", map[string]any{"allowEmptySignature": true}},
		"gemini-4-argon": {"google-generative-ai", "https://opencode.ai/inference/go/google/v1beta", nil},
		"hy3":            {"openai-completions", "https://opencode.ai/inference/go/openai/v1", map[string]any{"maxTokensField": "max_tokens"}},
	}
	for id, want := range namedRoutes {
		var found *catalogModel
		for _, row := range rows[opencodeID] {
			if row.ID == id {
				found = row
				break
			}
		}
		if found == nil {
			t.Errorf("%s: named example %q is missing", opencodeID, id)
			continue
		}
		if found.API != want.api || found.BaseURL != want.baseURL {
			t.Errorf("%s/%s: route = %s %s, want %s %s", opencodeID, id, found.API, found.BaseURL, want.api, want.baseURL)
		}
		for name, value := range want.compat {
			if found.Compat[name] != value {
				t.Errorf("%s/%s: compat[%s] = %v, want %v", opencodeID, id, name, found.Compat[name], value)
			}
		}
	}

	publishedSource, err := os.ReadFile(filepath.Join(out, "catalog-source.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publishedSource, sourceBytes) {
		t.Fatal("published provenance differs from the assembled bytes")
	}
	var provenance catalogProvenance
	if err := json.Unmarshal(publishedSource, &provenance); err != nil {
		t.Fatal(err)
	}
	if got, want := provenance.Inputs[modelsDevInputName].SHA256, sha256Hex(modelsDevBody); got != want {
		t.Fatalf("models.dev digest = %s, want %s", got, want)
	}
	if got, want := provenance.Inputs[openRouterInputName].SHA256, sha256Hex(openRouterBody); got != want {
		t.Fatalf("OpenRouter digest = %s, want %s", got, want)
	}
	if got, want := provenance.CatalogSHA256, sha256Hex(catalogBytes); got != want {
		t.Fatalf("catalog digest = %s, want %s", got, want)
	}
	generated, err := time.Parse(time.RFC3339Nano, provenance.GeneratedAt)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range provenance.Inputs {
		parsed, err := time.Parse(time.RFC3339Nano, input.FetchedAt)
		if err != nil {
			t.Fatalf("%s fetchedAt: %v", name, err)
		}
		if generated.Before(parsed) {
			t.Fatalf("generatedAt is earlier than %s fetchedAt", name)
		}
	}

	// Report, never repair, default availability against the staged rows.
	missingDefaults := 0
	for _, providerID := range registryIDs {
		meta, err := (&config.ModelsFile{}).SelectModel(providerID, "")
		if err != nil {
			t.Logf("default %s: unresolved: %v", providerID, err)
			missingDefaults++
			continue
		}
		present := false
		for _, row := range rows[providerID] {
			if row.ID == meta.ID {
				present = true
				break
			}
		}
		t.Logf("default %s/%s present=%v", providerID, meta.ID, present)
		if !present {
			missingDefaults++
		}
	}
	if missingDefaults > 0 {
		t.Logf("%d default model IDs are absent from the staged catalog", missingDefaults)
	}

	totalRows := 0
	for _, providerRows := range rows {
		totalRows += len(providerRows)
	}
	t.Logf("retained providers=%d rows=%d request_checks=%d openrouter=%d models_dev_sha256=%s catalog_sha256=%s",
		len(rows), totalRows, requestChecks, openRouterCount,
		provenance.Inputs[modelsDevInputName].SHA256, provenance.CatalogSHA256)
}
