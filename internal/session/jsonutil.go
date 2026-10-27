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

// exactObject decodes a JSON object preserving exact property names.
// Go struct decoding folds ASCII case when matching field names, but the
// session protocol (like Pi's JSON.parse) is case-sensitive: "ID" is not
// "id" and "ROLE" is not "role". All protocol-field lookups go through
// this helper so eager, indexed, and lazy readers agree with Pi. Duplicate
// keys keep last-wins semantics (matching JSON.parse). Unknown fields stay
// in the raw map for preservation.
func exactObject(data []byte) (map[string]json.RawMessage, error) {
	dec := jsonDecoder(data)
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return obj, nil
}

// exactString reads one canonical property. Alias-case keys never match.
// An explicit null counts as absent (a null parentId is a root entry).
func exactString(obj map[string]json.RawMessage, key string) (string, bool) {
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
