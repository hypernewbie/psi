package provider

import (
	"encoding/json"
	"math"
)

// The pinned Pi rules keep the requested output inside the context window.
// An endpoint refuses a limit that leaves no room for the prompt, so every
// adapter caps the request before it is sent.
const (
	contextSafetyTokens = 4096
	contextSafetyRatio  = 0.15
	minOutputRoom       = 32768
	minMaxTokens        = 1
	estimatedImageChars = 4800
	charsPerToken       = 3.5
)

// clampMaxTokensToContext caps the requested output tokens at the room left in
// the model's context window. A model whose output cap equals its context
// window would otherwise ask for more tokens than the endpoint can ever
// return, and the endpoint rejects that request.
func clampMaxTokensToContext(model ModelMeta, req Request, requested int) int {
	if model.ContextWindow <= 0 {
		return max(minMaxTokens, requested)
	}
	input := estimateRequestTokens(req)
	margin := max(contextSafetyTokens, int(math.Ceil(float64(input)*contextSafetyRatio)))
	room := model.ContextWindow - input - margin
	if room < minOutputRoom {
		room = min(minOutputRoom, model.ContextWindow-input-contextSafetyTokens)
	}
	return min(requested, max(minMaxTokens, room))
}

// estimateRequestTokens estimates the prompt size of a request. Pi prefers the
// reported usage of the last assistant message; a request carries no usage, so
// the estimate falls back to the message contents. Pi keeps the system prompt
// and the tool definitions in its initial system message, so both count here.
func estimateRequestTokens(req Request) int {
	tokens := 0
	if req.System != "" {
		tokens += int(math.Ceil(float64(len(req.System)) / charsPerToken))
	}
	if len(req.Tools) > 0 {
		tokens += int(math.Ceil(float64(jsonChars(req.Tools)) / charsPerToken))
	}
	for _, message := range req.Messages {
		tokens += messageTokens(message)
	}
	return tokens
}

// messageTokens estimates the size of one message.
func messageTokens(message Message) int {
	chars := 0
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			chars += len(block.Text)
		case "thinking":
			chars += len(block.Thinking)
		case "image":
			chars += estimatedImageChars
		case "toolCall":
			chars += len(block.Name) + jsonChars(block.Arguments)
		}
	}
	for _, section := range message.Sections {
		if section.Value != nil {
			chars += len(*section.Value)
		}
	}
	return int(math.Ceil(float64(chars) / charsPerToken))
}

// jsonChars returns the length of the JSON encoding of a value.
func jsonChars(value any) int {
	if value == nil {
		return 0
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}
