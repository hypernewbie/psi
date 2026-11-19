package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// models.dev publishes an experimental v2 catalog. Only the fields the catalog
// projection consumes are decoded; unrelated fields are ignored. A wrong JSON
// type fails the whole bake instead of silently dropping a capability.
type modelsDevCatalog map[string]modelsDevProvider

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Name         string                  `json:"name"`
	Status       string                  `json:"status"`
	Modalities   modelsDevModalities     `json:"modalities"`
	Capabilities modelsDevCapabilities   `json:"capabilities"`
	Limit        modelsDevLimits         `json:"limit"`
	Cost         json.RawMessage         `json:"cost"`
	API          map[string]modelsDevAPI `json:"api"`
}

type modelsDevModalities struct {
	Input []string `json:"input"`
}

type modelsDevCapabilities struct {
	Tools     modelsDevToolCapability `json:"tools"`
	Reasoning modelsDevReasoning      `json:"reasoning"`
}

type modelsDevToolCapability struct {
	Supported bool `json:"supported"`
}

type modelsDevReasoning struct {
	Supported bool `json:"supported"`
	Toggle    bool `json:"toggle"`
	// Effort is present when the upstream states effort values. A JSON null
	// value carries no level and is ignored later, but its presence still makes
	// this an effort-capable model.
	Effort []*string `json:"effort"`
}

type modelsDevLimits struct {
	Context *int `json:"context"`
	Output  *int `json:"output"`
}

type modelsDevAPI struct {
	BaseURL string `json:"base_url"`
	SDK     *struct {
		AISDK string `json:"aisdk"`
	} `json:"sdk"`
}

type modelsDevCost struct {
	Text *struct {
		Input  *modelsDevModalityCost `json:"input"`
		Output *modelsDevModalityCost `json:"output"`
	} `json:"text"`
}

type modelsDevModalityCost struct {
	Unit      *string                 `json:"unit"`
	Price     *float64                `json:"price"`
	Cache     *modelsDevCachePrices   `json:"cache"`
	Overrides []modelsDevCostOverride `json:"overrides"`
}

type modelsDevCachePrices struct {
	Read     *float64                `json:"read"`
	Write    orderedPrice            `json:"write"`
	Explicit *modelsDevExplicitCache `json:"explicit"`
}

type modelsDevExplicitCache struct {
	Write orderedPrice `json:"write"`
}

type modelsDevCostOverride struct {
	When  map[string]json.RawMessage `json:"when"`
	Price *float64                   `json:"price"`
	Cache *modelsDevCachePrices      `json:"cache"`
}

// orderedPrice decodes a models.dev cache-write price. The schema allows one
// price per cache TTL, so the value is either a number or an object. The first
// object value in document order is the host's default TTL price; Go maps lose
// that order, so the object is walked token by token.
type orderedPrice struct {
	Value float64
	Set   bool
}

func (p *orderedPrice) UnmarshalJSON(data []byte) error {
	*p = orderedPrice{}
	var number *float64
	if err := json.Unmarshal(data, &number); err == nil {
		if number != nil {
			*p = orderedPrice{Value: *number, Set: true}
		}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return fmt.Errorf("cache write price must be a number or a TTL price object")
	}
	for decoder.More() {
		if _, err := decoder.Token(); err != nil {
			return err
		}
		var price *float64
		if err := decoder.Decode(&price); err != nil || price == nil {
			return fmt.Errorf("cache write TTL price must be a number")
		}
		if !p.Set {
			*p = orderedPrice{Value: *price, Set: true}
		}
	}
	return nil
}

// sourceModel is one normalized models.dev chat row.
type sourceModel struct {
	ID               string
	Name             string
	Status           string
	ToolCall         bool
	Reasoning        bool
	ReasoningOptions []reasoningOption
	ImageInput       bool
	ContextWindow    int // zero when upstream omitted or zeroed the limit
	MaxTokens        int
	Cost             modelCost
	APIs             []sourceAPI
}

type sourceAPI struct {
	Protocol string
	BaseURL  string
	AISDK    string
}

type reasoningOption struct {
	kind   string // "toggle" or "effort"
	values []*string
}

func (o reasoningOption) isEffort() bool { return o.kind == "effort" }

// modelsDevChatModels normalizes every v2 chat row of one provider. Keys are
// sorted so a duplicate embedded ID resolves to the same candidate on every
// run.
func modelsDevChatModels(catalog modelsDevCatalog, provider string) ([]*sourceModel, error) {
	bucket, ok := catalog[provider]
	if !ok {
		return nil, fmt.Errorf("models.dev source %q is missing", provider)
	}
	keys := make([]string, 0, len(bucket.Models))
	for key := range bucket.Models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	models := make([]*sourceModel, 0, len(keys))
	for _, key := range keys {
		raw := bucket.Models[key]
		if raw.Type != "chat" {
			continue
		}
		model, err := normalizeModelsDevModel(raw)
		if err != nil {
			return nil, fmt.Errorf("models.dev %s/%s: %w", provider, key, err)
		}
		// Only these rows contribute price-derived metadata to the projection.
		if (provider == "anthropic" || provider == "openai") && model.ToolCall && len(raw.Cost) > 0 {
			var cost modelsDevCost
			if err := json.Unmarshal(raw.Cost, &cost); err != nil {
				return nil, fmt.Errorf("models.dev %s/%s: invalid cost: %w", provider, model.ID, err)
			}
			model.Cost = normalizeModelsDevCost(&cost)
		}
		models = append(models, model)
	}
	return models, nil
}

func normalizeModelsDevModel(raw modelsDevModel) (*sourceModel, error) {
	if raw.ID == "" {
		return nil, fmt.Errorf("model has an empty id")
	}
	if raw.Limit.Context != nil && *raw.Limit.Context < 0 {
		return nil, fmt.Errorf("negative context limit %d", *raw.Limit.Context)
	}
	if raw.Limit.Output != nil && *raw.Limit.Output < 0 {
		return nil, fmt.Errorf("negative output limit %d", *raw.Limit.Output)
	}
	model := &sourceModel{
		ID:            raw.ID,
		Name:          raw.Name,
		Status:        raw.Status,
		ToolCall:      raw.Capabilities.Tools.Supported,
		Reasoning:     raw.Capabilities.Reasoning.Supported,
		ContextWindow: intValue(raw.Limit.Context),
		MaxTokens:     intValue(raw.Limit.Output),
	}
	if model.Name == "" {
		model.Name = model.ID
	}
	for _, modality := range raw.Modalities.Input {
		if modality == "image" || len(modality) >= len("image/") && modality[:len("image/")] == "image/" {
			model.ImageInput = true
			break
		}
	}
	if model.Reasoning {
		if raw.Capabilities.Reasoning.Toggle {
			model.ReasoningOptions = append(model.ReasoningOptions, reasoningOption{kind: "toggle"})
		}
		if raw.Capabilities.Reasoning.Effort != nil {
			model.ReasoningOptions = append(model.ReasoningOptions, reasoningOption{kind: "effort", values: raw.Capabilities.Reasoning.Effort})
		}
	}
	protocols := make([]string, 0, len(raw.API))
	for protocol := range raw.API {
		protocols = append(protocols, protocol)
	}
	sort.Strings(protocols)
	for _, protocol := range protocols {
		entry := raw.API[protocol]
		aisdk := ""
		if entry.SDK != nil {
			aisdk = entry.SDK.AISDK
		}
		model.APIs = append(model.APIs, sourceAPI{Protocol: protocol, BaseURL: entry.BaseURL, AISDK: aisdk})
	}
	return model, nil
}

func (m *sourceModel) hasProtocol(protocol string) bool {
	for _, api := range m.APIs {
		if api.Protocol == protocol {
			return true
		}
	}
	return false
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func floatValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// normalizeModelsDevCost maps the models.dev cost text into the only cost data
// the projection can emit: Anthropic fallback prices and OpenAI cache-mode
// decisions.
func normalizeModelsDevCost(raw *modelsDevCost) modelCost {
	if raw == nil || raw.Text == nil || raw.Text.Input == nil || raw.Text.Output == nil {
		return modelCost{}
	}
	input, output := raw.Text.Input, raw.Text.Output
	if !tokenUnit(input.Unit) || !tokenUnit(output.Unit) {
		return modelCost{}
	}
	cost := modelCost{
		Input:      floatValue(input.Price),
		Output:     floatValue(output.Price),
		CacheRead:  cacheReadPrice(input.Cache),
		CacheWrite: cacheWritePrice(input.Cache),
	}
	tiers := map[int]*costTier{}
	order := []int{}
	tier := func(above int) *costTier {
		entry, ok := tiers[above]
		if !ok {
			entry = &costTier{InputTokensAbove: above}
			tiers[above] = entry
			order = append(order, above)
		}
		return entry
	}
	for _, override := range input.Overrides {
		above, ok := contextThreshold(override.When)
		if !ok {
			continue
		}
		entry := tier(above)
		if override.Price != nil {
			entry.Input = *override.Price
			entry.hasInput = true
		}
		if override.Cache != nil && override.Cache.Read != nil {
			entry.CacheRead = *override.Cache.Read
			entry.hasCacheRead = true
		}
		if cacheWriteSet(override.Cache) {
			entry.CacheWrite = cacheWritePrice(override.Cache)
			entry.hasCacheWrite = true
		}
	}
	for _, override := range output.Overrides {
		above, ok := contextThreshold(override.When)
		if !ok {
			continue
		}
		if override.Price != nil {
			entry := tier(above)
			entry.Output = *override.Price
			entry.hasOutput = true
		}
	}
	for _, above := range order {
		entry := *tiers[above]
		if !entry.hasInput {
			entry.Input = cost.Input
		}
		if !entry.hasOutput {
			entry.Output = cost.Output
		}
		if !entry.hasCacheRead {
			entry.CacheRead = cost.CacheRead
		}
		if !entry.hasCacheWrite {
			entry.CacheWrite = cost.CacheWrite
		}
		cost.Tiers = append(cost.Tiers, entry)
	}
	return cost
}

func tokenUnit(unit *string) bool {
	return unit == nil || *unit == "token"
}

func cacheReadPrice(cache *modelsDevCachePrices) float64 {
	if cache == nil {
		return 0
	}
	return floatValue(cache.Read)
}

func cacheWritePrice(cache *modelsDevCachePrices) float64 {
	if cache == nil {
		return 0
	}
	if cache.Write.Set {
		return cache.Write.Value
	}
	if cache.Explicit != nil {
		return cache.Explicit.Write.Value
	}
	return 0
}

func cacheWriteSet(cache *modelsDevCachePrices) bool {
	if cache == nil {
		return false
	}
	return cache.Write.Set || cache.Explicit != nil && cache.Explicit.Write.Set
}

func contextThreshold(when map[string]json.RawMessage) (int, bool) {
	if len(when) != 1 {
		return 0, false
	}
	raw, ok := when["context"]
	if !ok {
		return 0, false
	}
	var context map[string]json.RawMessage
	if json.Unmarshal(raw, &context) != nil {
		return 0, false
	}
	for _, key := range []string{"gt", "gte"} {
		if value, ok := context[key]; ok {
			var size int
			if json.Unmarshal(value, &size) == nil {
				return size, true
			}
		}
	}
	return 0, false
}
