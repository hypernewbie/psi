package session

// LastConversationalMessage returns the last message that can belong to the
// conversation, skipping trailing system declarations written by configuration
// synchronization. A nil trailing system payload makes the tip uncertain, so no
// candidate is returned.
func LastConversationalMessage(flat []AgentMessage) *AgentMessage {
	for i := len(flat) - 1; i >= 0; i-- {
		message := flat[i]
		if message.Role == "system" {
			if message.System == nil {
				return nil
			}
			continue
		}
		return &flat[i]
	}
	return nil
}

// IsFailureAssistant reports whether the message is a persisted failed or
// interrupted assistant response. It does not require an error message or
// empty content; visible partial responses are failure assistants too.
func IsFailureAssistant(message AgentMessage) bool {
	return message.Role == "assistant" && message.Assistant != nil &&
		(message.Assistant.StopReason == "error" || message.Assistant.StopReason == "aborted")
}

// CanBump reports whether the last conversational message represents an
// unfinished turn. It does not replace CheckContinuable or request validation:
// earlier malformed content and deferred runtime state still block continuation.
func CanBump(flat []AgentMessage) bool {
	last := LastConversationalMessage(flat)
	if last == nil {
		return false
	}
	if IsFailureAssistant(*last) {
		return true
	}
	switch last.Role {
	case "user":
		return last.User != nil
	case "toolResult":
		return last.ToolResult != nil
	case "custom":
		return last.Custom != nil
	case "assistant":
		if last.Assistant == nil {
			return false
		}
		if last.Assistant.StopReason == "toolUse" {
			for _, block := range last.Assistant.Content {
				if block.Type == "toolCall" && block.Call != nil {
					return true
				}
			}
		}
	}
	return false
}
