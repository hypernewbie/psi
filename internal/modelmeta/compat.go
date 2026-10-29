// Package modelmeta holds provider-neutral model metadata shared by the
// configuration and wire-adapter packages.
package modelmeta

import (
	"bytes"
	"encoding/json"
)

// Compat stores Pi's API-specific compatibility object without collapsing
// explicit false, null, and absent fields. Consumers use Bool and String;
// unknown fields remain available for precise unsupported-capability errors.
type Compat map[string]json.RawMessage

func (c Compat) Raw(name string) (json.RawMessage, bool) {
	raw, ok := c[name]
	return raw, ok
}

// Bool returns the value, whether it is a non-null boolean, and whether the
// key was present. A present null is therefore distinguishable from absence.
func (c Compat) Bool(name string) (value, hasValue, present bool) {
	raw, present := c[name]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, false, present
	}
	if json.Unmarshal(raw, &value) != nil {
		return false, false, true
	}
	return value, true, true
}

// String has the same presence semantics as Bool.
func (c Compat) String(name string) (value string, hasValue, present bool) {
	raw, present := c[name]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false, present
	}
	if json.Unmarshal(raw, &value) != nil {
		return "", false, true
	}
	return value, true, true
}

func (c Compat) IsNull(name string) bool {
	raw, ok := c[name]
	return ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
