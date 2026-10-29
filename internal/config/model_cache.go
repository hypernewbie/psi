package config

import (
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"time"
)

//go:embed data/catalog-source.json
var catalogSourceJSON []byte

// loadPiModelCache reads Pi's persisted public catalog overlay, without network
// access. Pi uses an overlay only when its Last-Modified date is newer than
// the bundled catalog's generation date. Invalid caches leave the bundle intact.
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
	for provider, api := range map[string]string{"anthropic": "anthropic-messages", "openai-codex": "openai-codex-responses"} {
		var entry struct {
			LastModified int64             `json:"lastModified"`
			Models       []json.RawMessage `json:"models"`
		}
		if json.Unmarshal(store[provider], &entry) != nil || entry.LastModified <= generated.UnixMilli() {
			continue
		}
		for _, raw := range entry.Models {
			var item struct {
				ModelMeta
				Provider string `json:"provider"`
				Type     string `json:"type"`
			}
			if json.Unmarshal(raw, &item) != nil || item.Provider != provider || (item.Type != "" && item.Type != "chat") || item.ID == "" || item.API != api || item.ContextWindow <= 0 || item.MaxTokens <= 0 {
				continue
			}
			// Catalog data describes capabilities. Endpoint and headers remain
			// under explicit models.json control, not the cached overlay.
			item.BaseURL, item.Headers, item.APIKey = "", nil, ""
			item.InputImage = containsString(item.Input, "image")
			if out[provider] == nil {
				out[provider] = make(map[string]ModelMeta)
			}
			out[provider][item.ID] = item.ModelMeta
		}
	}
	return out
}
