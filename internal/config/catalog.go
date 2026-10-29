package config

import (
	_ "embed"
	"encoding/json"
	"sort"
)

//go:embed data/catalog.json
var catalogJSON []byte

func BundledModels(provider string) []ModelMeta {
	var all map[string][]ModelMeta
	// Generated, embedded public data. Never load a catalog from the network.
	if err := json.Unmarshal(catalogJSON, &all); err != nil {
		panic("invalid embedded public model catalog")
	}
	for i := range all[provider] {
		all[provider][i].InputImage = containsString(all[provider][i].Input, "image")
	}
	return all[provider]
}
func bundledModel(provider, id string) (ModelMeta, bool) {
	for _, m := range BundledModels(provider) {
		if m.ID == id {
			return m, true
		}
	}
	return ModelMeta{}, false
}

type ModelOption struct {
	Model     ModelMeta
	Available bool
	Reason    string
}

// ModelOptions resolves every bundled, cached, and explicitly configured model
// through the same path used by activation. Invalid entries stay visible with
// their reason; LocalModels is the available-only compatibility view.
func (mf *ModelsFile) ModelOptions(provider string) []ModelOption {
	ids := map[string]bool{}
	for _, model := range BundledModels(provider) {
		ids[model.ID] = true
	}
	for id := range mf.cachedModels[provider] {
		ids[id] = true
	}
	p := mf.Providers[provider]
	for _, model := range p.Models {
		ids[model.ID] = true
	}
	for id := range p.ModelOverrides {
		ids[id] = true
	}
	var out []ModelOption
	for id := range ids {
		model, err := mf.SelectModel(provider, id)
		if err == nil {
			out = append(out, ModelOption{Model: model, Available: true})
			continue
		}
		candidate, ok := mf.cachedModels[provider][id]
		if !ok {
			candidate, ok = bundledModel(provider, id)
		}
		if !ok {
			for _, configured := range p.Models {
				if configured.ID == id {
					candidate, ok = configured, true
					break
				}
			}
		}
		if !ok {
			candidate.ID = id
		}
		candidate.UnavailableReason = err.Error()
		out = append(out, ModelOption{Model: candidate, Reason: err.Error()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model.ID < out[j].Model.ID })
	return out
}

func (mf *ModelsFile) LocalModels(provider string) []ModelMeta {
	var out []ModelMeta
	for _, option := range mf.ModelOptions(provider) {
		if option.Available {
			out = append(out, option.Model)
		}
	}
	return out
}
