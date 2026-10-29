package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"psi/internal/providerregistry"
)

//go:embed data/catalog-source.json
var catalogSourceJSON []byte

// loadPiModelCache reads Pi's persisted public catalog overlay without network
// access. Pi uses an overlay only when Last-Modified is newer than the bundled
// catalog generation date. Invalid cache entries leave the bundle intact.
func loadPiModelCache(path string) map[string]map[string]ModelMeta {
	var source struct {
		GeneratedAt string `json:"generatedAt"`
	}
	if json.Unmarshal(catalogSourceJSON, &source) != nil {
		return nil
	}
	generated, err := time.Parse(time.RFC3339Nano, source.GeneratedAt)
	if err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const maxCacheBytes = 32 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxCacheBytes+1))
	if err != nil || len(data) > maxCacheBytes {
		return nil
	}
	var store map[string]json.RawMessage
	if json.Unmarshal(stripBOM(data), &store) != nil {
		return nil
	}
	out := make(map[string]map[string]ModelMeta)
	for _, providerID := range providerregistry.IDs() {
		var entry struct {
			LastModified int64             `json:"lastModified"`
			Models       []json.RawMessage `json:"models"`
		}
		if json.Unmarshal(store[providerID], &entry) != nil || entry.LastModified <= generated.UnixMilli() {
			continue
		}
		for _, raw := range entry.Models {
			var item struct {
				ModelMeta
				Provider string `json:"provider"`
				Type     string `json:"type"`
			}
			if json.Unmarshal(raw, &item) != nil || item.Provider != providerID || (item.Type != "" && item.Type != "chat") || item.ID == "" {
				continue
			}
			item.Headers = nil
			item.APIKey = ""
			item.InputImage = containsString(item.Input, "image")
			if !providerregistry.SupportsAPI(providerID, item.API) {
				item.UnavailableReason = fmt.Sprintf("cached model API %q is not supported for provider %q", item.API, providerID)
				item.BaseURL = ""
			} else if providerID == "opencode-go" {
				if item.BaseURL == "" {
					item.UnavailableReason = "cached OpenCode model is missing its per-model endpoint"
					item.CacheEndpointRejected = true
				} else if !providerregistry.TrustedCachedEndpoint(providerID, item.API, item.BaseURL) {
					item.UnavailableReason = "cached OpenCode endpoint is outside the reviewed origin and API path allowlist"
					item.BaseURL = ""
					item.CacheEndpointRejected = true
				}
			} else {
				// Public model cache endpoints are not trusted outside the reviewed
				// OpenCode path policy. Registry defaults or explicit user config win.
				item.BaseURL = ""
			}
			if item.ContextWindow <= 0 || item.MaxTokens <= 0 {
				item.UnavailableReason = "cached model is missing a positive contextWindow or maxTokens"
				item.CacheEndpointRejected = false
			}
			if out[providerID] == nil {
				out[providerID] = make(map[string]ModelMeta)
			}
			out[providerID][item.ID] = item.ModelMeta
		}
	}
	return out
}
