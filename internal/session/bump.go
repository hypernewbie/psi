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

// CanBump reports whether the last conversational message represents an
// unfinished turn. It does not replace CheckContinuable or request validation:
// earlier malformed content and deferred runtime state still block continuation.
func CanBump(flat []AgentMessage) bool {
	last := LastConversationalMessage(flat)
	if last == nil {
		return false
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
		switch last.Assistant.StopReason {
		case "error", "aborted":
			return true
		case "toolUse":
			for _, block := range last.Assistant.Content {
				if block.Type == "toolCall" && block.Call != nil {
					return true
				}
			}
		}
	}
	return false
}
