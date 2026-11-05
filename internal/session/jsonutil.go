package session

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func jsonDecoder(data []byte) *json.Decoder {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec
}

// jsonView is a parsed JSON object: exact property names and the raw bytes
// of every value. Duplicate keys keep last-wins semantics (matching
// JSON.parse). Unknown keys stay in the view for preservation.
type jsonView = map[string][]byte

// exactObject splits one JSON object into its exact property names.
// Go struct decoding folds ASCII case when matching field names, but the
// session protocol (like Pi's JSON.parse) is case-sensitive: "ID" is not
// "id" and "ROLE" is not "role". All protocol-field lookups go through
// this helper so every reader agrees with Pi.
func exactObject(data []byte) (jsonView, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid JSON object")
	}
	return splitObject(data)
}

// splitObject splits an object whose bytes are already known to be valid
// JSON: a nested value of a validated record. Malformed input is an error,
// never a panic.
func splitObject(data []byte) (view jsonView, err error) {
	defer func() {
		if recover() != nil {
			view, err = nil, fmt.Errorf("invalid JSON object")
		}
	}()
	return metadataObject(data)
}

// exactString reads one canonical property. Alias-case keys never match.
// An explicit null counts as absent (a null parentId is a root entry).
func exactString(obj jsonView, key string) (string, bool) {
	raw, ok := obj[key]
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}
