package app

import (
	"fmt"
	"strings"

	"psi/internal/config"
)

// resolveConfiguredModelOption resolves a provider or model reference that
// names a provider from models.json. Parse knows the built-in providers only,
// so the configured providers resolve after models.json loads. The resolution
// is pure: it returns the same options when no configured provider is named.
func resolveConfiguredModelOption(o Options, models *config.ModelsFile) (Options, error) {
	if models == nil {
		return o, nil
	}
	ids := models.ProviderIDs()
	if o.Provider != "" {
		matched := ""
		for _, id := range ids {
			if strings.EqualFold(o.Provider, id) {
				matched = id
				break
			}
		}
		if matched == "" {
			return o, fmt.Errorf("unsupported provider %q (available: %s)", o.Provider, strings.Join(ids, ", "))
		}
		o.Provider = matched
	}
	if o.Model == "" {
		return o, nil
	}
	if i := strings.IndexByte(o.Model, '/'); i > 0 {
		prefix := o.Model[:i]
		for _, id := range ids {
			if !strings.EqualFold(prefix, id) {
				continue
			}
			if o.Provider != "" && !strings.EqualFold(o.Provider, id) {
				return o, fmt.Errorf("--model provider %q conflicts with --provider %q", id, o.Provider)
			}
			o.Provider, o.Model = id, o.Model[i+1:]
			break
		}
	}
	if strings.TrimSpace(o.Model) == "" {
		return o, fmt.Errorf("--model requires a model ID")
	}
	return o, nil
}
