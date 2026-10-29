package session

import (
	"psi/internal/event"
	toolsummary "psi/internal/tools"
)

const maxToolSummariesPerEntry = 64

// metadataToolSummaries extracts only the allowlisted argument fields while
// the indexer already has the record bytes. Hidden bodies remain raw slices
// used by the JSON scanner and are never decoded or retained.
func metadataToolSummaries(record []byte) map[string]event.ToolSummary {
	root, err := metadataObject(record)
	if err != nil {
		return nil
	}
	messageRaw := root["message"]
	message, err := metadataObject(messageRaw)
	if err != nil {
		return nil
	}
	role := metadataString(message, "role")
	if role == "bashExecution" {
		id := metadataString(root, "id")
		if id == "" {
			return nil
		}
		return map[string]event.ToolSummary{id: toolsummary.SummarizeArgumentsJSON("bash", messageRaw, "", "")}
	}
	if role != "assistant" {
		return nil
	}
	content := message["content"]
	if len(content) == 0 || metadataNull(content) {
		return nil
	}
	var summaries map[string]event.ToolSummary
	_ = metadataEach(content, func(raw []byte) error {
		block, err := metadataObject(raw)
		if err != nil || metadataString(block, "type") != "toolCall" {
			return nil
		}
		id := metadataString(block, "id")
		if id == "" || summaries != nil && len(summaries) >= maxToolSummariesPerEntry {
			return nil
		}
		if summaries == nil {
			summaries = make(map[string]event.ToolSummary)
		}
		name := metadataString(block, "name")
		summaries[id] = toolsummary.SummarizeArgumentsJSON(name, block["arguments"], "", "")
		return nil
	})
	return summaries
}
