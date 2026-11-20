package provider

import (
	"encoding/json"

	"github.com/hypernewbie/psi/internal/secretmem"
)

// releaseRawEntries wipes and removes every owned raw-message value. It must
// not be used on a borrowed parent map whose values are still live elsewhere.
func releaseRawEntries(entries map[string]json.RawMessage) {
	for key, raw := range entries {
		secretmem.Wipe(raw[:cap(raw)])
		delete(entries, key)
	}
}

// ReleaseAuthEntries releases a map returned by AuthEntries. Callers that
// receive that map own its raw-message allocations and must call this after
// the last credential read.
func ReleaseAuthEntries(entries map[string]json.RawMessage) {
	releaseRawEntries(entries)
}
