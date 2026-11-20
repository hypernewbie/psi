package agent

import (
	"encoding/json"

	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
)

// webCallDisplay builds the retained call-display map in assistant order. The
// same bounded subset feeds the live rows and the persisted envelope, so a
// card never shows a call the session did not retain. Calls outside the web
// tools, or without a display hook, are skipped.
func webCallDisplay(cfg Config, calls []provider.Block) map[string]event.WebCall {
	if cfg.WebDisplay == nil || len(calls) == 0 {
		return nil
	}
	var out map[string]event.WebCall
	size := 0
	for _, call := range calls {
		if len(out) >= event.WebMaxCallsPerEntry {
			break
		}
		if call.ID == "" {
			continue
		}
		display, ok := cfg.WebDisplay(call.Name, call.Arguments)
		if !ok {
			continue
		}
		encoded, err := json.Marshal(event.WebMeta{Calls: map[string]event.WebCall{call.ID: display}})
		if err != nil || size+len(encoded) > event.WebMaxMetaBytes {
			break
		}
		if out == nil {
			out = make(map[string]event.WebCall)
		}
		out[call.ID] = display
		size += len(encoded)
	}
	return out
}
