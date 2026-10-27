package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
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

// Canonical protocol keys for alias defolding (F05). Struct decoding folds
// ASCII case when matching names, so an alias-case key such as "messAge"
// can populate -- or break -- a field Pi reads as absent. defoldRecord drops
// only keys that match a canonical key case-insensitively; canonical and
// unknown keys pass through untouched (encoding/json ignores unknown keys,
// and Raw always preserves original bytes).

var entryKeys = map[string]bool{
	"type": true, "version": true, "id": true, "parentId": true, "timestamp": true,
	"cwd": true, "parentSession": true, "message": true, "provider": true, "modelId": true,
	"thinkingLevel": true, "firstKeptEntryId": true, "customType": true, "targetId": true,
	"display": true, "name": true, "data": true, "details": true, "usage": true,
	"kind": true, "model": true, "note": true, "summary": true, "tokensBefore": true,
	"fromHook": true, "systemMessage": true, "fromId": true, "label": true, "replacement": true,
}

var messageKeys = map[string]bool{
	"role": true, "provider": true, "model": true, "api": true, "responseModel": true,
	"responseId": true, "providerThinkingLevel": true, "thinkingLevel": true, "usage": true,
	"content": true, "toolCallId": true, "toolName": true, "isError": true, "cancelled": true,
	"exitCode": true, "details": true, "command": true, "output": true, "truncated": true,
	"fullOutputPath": true, "excludeFromContext": true, "customType": true, "display": true,
	"summary": true, "fromId": true, "tokensBefore": true, "timestamp": true, "durationMs": true,
	"deferred": true, "errorMessage": true, "rawStopReason": true, "endTurn": true,
	"sections": true, "toolsAdded": true, "toolsRemoved": true,
}

var blockKeys = map[string]bool{
	"type": true, "text": true, "textSignature": true, "thinking": true,
	"thinkingSignature": true, "redacted": true, "id": true, "name": true,
	"arguments": true, "thoughtSignature": true, "namespace": true,
	"argumentsError": true, "data": true, "mimeType": true,
}

var usageKeys = map[string]bool{
	"input": true, "output": true, "cacheRead": true, "cacheWrite": true,
	"cacheWrite1h": true, "reasoning": true, "totalTokens": true, "cost": true,
}

var costKeys = map[string]bool{
	"input": true, "output": true, "cacheRead": true, "cacheWrite": true,
	"totalFromProvider": true,
}

var objectKeys = map[string]bool{"targetId": true, "psiOutcome": true}

var toolKeys = map[string]bool{"name": true, "description": true, "parameters": true}

var toolRefKeys = map[string]bool{"name": true, "namespace": true}

// foldedAlias reports whether key would match a canonical struct field only
// through case-insensitive folding.
func foldedAlias(key string, allow map[string]bool) bool {
	if allow[key] {
		return false
	}
	for k := range allow {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

func defoldMap(obj map[string]json.RawMessage, allow map[string]bool, child func(string, json.RawMessage) json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		if foldedAlias(k, allow) {
			continue
		}
		if child != nil {
			v = child(k, v)
		}
		out[k] = v
	}
	return out
}

func defoldSimple(raw json.RawMessage, allow map[string]bool) json.RawMessage {
	obj, err := exactObject(raw)
	if err != nil {
		return raw
	}
	clean, err := json.Marshal(defoldMap(obj, allow, nil))
	if err != nil {
		return raw
	}
	return clean
}

func defoldUsage(raw json.RawMessage) json.RawMessage {
	obj, err := exactObject(raw)
	if err != nil {
		return raw
	}
	out := defoldMap(obj, usageKeys, func(k string, v json.RawMessage) json.RawMessage {
		if k == "cost" {
			return defoldSimple(v, costKeys)
		}
		return v
	})
	clean, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return clean
}

func defoldMessage(raw json.RawMessage) json.RawMessage {
	obj, err := exactObject(raw)
	if err != nil {
		return raw
	}
	out := defoldMap(obj, messageKeys, func(k string, v json.RawMessage) json.RawMessage {
		switch k {
		case "content":
			if len(v) > 0 && v[0] == '[' {
				var blocks []json.RawMessage
				if err := json.Unmarshal(v, &blocks); err != nil {
					return v
				}
				for i, b := range blocks {
					blocks[i] = defoldSimple(b, blockKeys)
				}
				clean, err := json.Marshal(blocks)
				if err != nil {
					return v
				}
				return clean
			}
			return v
		case "usage":
			return defoldUsage(v)
		case "details":
			return defoldSimple(v, objectKeys)
		case "toolsAdded":
			return defoldArray(v, toolKeys)
		case "toolsRemoved":
			return defoldArray(v, toolRefKeys)
		}
		return v
	})
	clean, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return clean
}

func defoldArray(raw json.RawMessage, allow map[string]bool) json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw
	}
	for i, it := range items {
		items[i] = defoldSimple(it, allow)
	}
	clean, err := json.Marshal(items)
	if err != nil {
		return raw
	}
	return clean
}

// defoldRecord strips alias-case keys from an already-parsed record object
// so struct decoding cannot see them. Canonical and unknown keys survive;
// Raw preservation happens separately on the original bytes.
func defoldRecord(obj map[string]json.RawMessage) []byte {
	out := defoldMap(obj, entryKeys, func(k string, v json.RawMessage) json.RawMessage {
		switch k {
		case "message", "systemMessage":
			return defoldMessage(v)
		case "usage":
			return defoldUsage(v)
		case "data", "details":
			return defoldSimple(v, objectKeys)
		}
		return v
	})
	clean, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return clean
}
