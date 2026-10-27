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

// LocalModels applies the same overrides as actual selection, including custom
// model declarations. Unsupported APIs stay out of the executable catalog.
func (mf *ModelsFile) LocalModels(provider string) []ModelMeta {
	ids := map[string]bool{}
	for _, m := range BundledModels(provider) {
		ids[m.ID] = true
	}
	for _, m := range mf.Providers[provider].Models {
		ids[m.ID] = true
	}
	var out []ModelMeta
	for id := range ids {
		if m, err := mf.SelectModel(provider, id); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
