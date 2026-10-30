package provider

import (
	"encoding/json"
	"strings"
)

// Completions reasoning_details use the existing Pi thinkingSignature slot.
// Keep unknown detail fields opaque, but admit only Pi's recognized shapes.
type completionReasoningDetail map[string]json.RawMessage

func reasoningString(detail completionReasoningDetail, name string) (string, bool) {
	var value string
	raw, exists := detail[name]
	if !exists || json.Unmarshal(raw, &value) != nil || string(raw) == "null" {
		return "", false
	}
	return value, true
}

func decodeCompletionReasoningDetail(raw json.RawMessage) (completionReasoningDetail, bool) {
	var detail completionReasoningDetail
	if json.Unmarshal(raw, &detail) != nil || detail == nil {
		return nil, false
	}
	for _, field := range []string{"id", "format"} {
		if value, exists := detail[field]; exists {
			if field == "id" && string(value) == "null" {
				continue
			}
			if _, ok := reasoningString(detail, field); !ok {
				return nil, false
			}
		}
	}
	if raw, exists := detail["index"]; exists {
		var index float64
		if string(raw) == "null" || json.Unmarshal(raw, &index) != nil {
			return nil, false
		}
	}
	kind, _ := reasoningString(detail, "type")
	field := ""
	switch kind {
	case "reasoning.text":
		field = "text"
		if signature, exists := detail["signature"]; exists && string(signature) != "null" {
			if _, ok := reasoningString(detail, "signature"); !ok {
				return nil, false
			}
		}
	case "reasoning.summary":
		field = "summary"
	case "reasoning.encrypted":
		field = "data"
	default:
		return nil, false
	}
	_, ok := reasoningString(detail, field)
	return detail, ok
}

func parseCompletionReasoningDetails(signature string) []completionReasoningDetail {
	var raw []json.RawMessage
	if json.Unmarshal([]byte(signature), &raw) != nil || len(raw) == 0 {
		return nil
	}
	out := make([]completionReasoningDetail, 0, len(raw))
	for _, item := range raw {
		detail, ok := decodeCompletionReasoningDetail(item)
		if !ok {
			return nil
		}
		out = append(out, detail)
	}
	return out
}

// Accumulate textual detail deltas without re-encoding the entire prefix on
// every chunk. Only block transitions and final/partial settlement encode it.
type completionReasoningStream struct {
	details     []completionReasoningDetail
	kind, field string
	text        strings.Builder
}

func (s *completionReasoningStream) flush() {
	if s.field != "" {
		s.details[len(s.details)-1][s.field], _ = json.Marshal(s.text.String())
		s.text.Reset()
	}
}

func (s *completionReasoningStream) append(detail completionReasoningDetail) {
	kind, _ := reasoningString(detail, "type")
	if s.kind == kind && s.field != "" {
		incoming, _ := reasoningString(detail, s.field)
		s.text.WriteString(incoming)
		last := s.details[len(s.details)-1]
		for _, key := range []string{"id", "format", "index", "signature"} {
			value, exists := last[key]
			if !exists || string(value) == "null" || (key != "index" && string(value) == `""`) {
				if next, exists := detail[key]; exists {
					last[key] = next
				}
			}
		}
		return
	}
	s.flush()
	s.kind, s.field = kind, ""
	if kind == "reasoning.text" {
		s.field = "text"
	} else if kind == "reasoning.summary" {
		s.field = "summary"
	}
	if s.field != "" {
		value, _ := reasoningString(detail, s.field)
		s.text.WriteString(value)
	}
	s.details = append(s.details, detail)
}

func (s *completionReasoningStream) signature() string {
	s.flush()
	s.kind, s.field = "", ""
	raw, _ := json.Marshal(s.details)
	return string(raw)
}
