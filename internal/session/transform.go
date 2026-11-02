package session

import "time"

// TransformMessagesForAPI matches Pi's API replay repair. Tool calls that have
// no durable result receive a synthetic error result in the request context;
// the result is never written to the session and the tool is never rerun.
func TransformMessagesForAPI(messages []AgentMessage) []AgentMessage {
	out := make([]AgentMessage, 0, len(messages))
	var pending []ToolCall
	results := map[string]bool{}
	var heldSystem []AgentMessage

	closePending := func() {
		for _, call := range pending {
			if results[call.ID] {
				continue
			}
			out = append(out, AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{
				Role:       "toolResult",
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content:    []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "No result provided"}}},
				IsError:    true,
				Timestamp:  time.Now().UnixMilli(),
			}})
		}
		pending = nil
		results = map[string]bool{}
		out = append(out, heldSystem...)
		heldSystem = nil
	}

	for _, message := range messages {
		switch message.Role {
		case "assistant":
			closePending()
			if message.Assistant == nil || message.Assistant.StopReason == "error" || message.Assistant.StopReason == "aborted" {
				continue
			}
			for _, block := range message.Assistant.Content {
				if block.Type == "toolCall" && block.Call != nil {
					pending = append(pending, *block.Call)
				}
			}
			out = append(out, message)
		case "toolResult":
			if message.ToolResult != nil {
				results[message.ToolResult.ToolCallID] = true
			}
			out = append(out, message)
		case "system":
			if len(pending) > 0 {
				heldSystem = append(heldSystem, message)
			} else {
				out = append(out, message)
			}
		case "user":
			closePending()
			out = append(out, message)
		default:
			out = append(out, message)
		}
	}
	closePending()
	return out
}
