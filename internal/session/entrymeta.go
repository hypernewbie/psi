package session

// Display metadata that the session store reads from a loaded entry: the
// psi.input provenance target and the tool-call summaries shown in history.

import (
	"encoding/json"

	"psi/internal/event"
	"psi/internal/tools"
)

// fillEntryMetadata fills the display metadata of a loaded entry.
func fillEntryMetadata(e *Entry) {
	switch {
	case e.Custom != nil && e.Custom.CustomType == "psi.input":
		e.provenanceTarget = rawTargetID(e.Custom.Data)
	case e.CustomMessage != nil && e.CustomMessage.CustomType == "psi.input":
		e.provenanceTarget = rawTargetID(e.CustomMessage.Details)
	}
	e.toolSummaries = entryToolSummaries(e)
}

// rawTargetID reads the provenance target from a raw payload. The property
// name is exact, like Pi's typed access.
func rawTargetID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	obj, err := exactObject(raw)
	if err != nil {
		return ""
	}
	target, _ := exactString(obj, "targetId")
	return target
}

// entryToolSummaries summarizes the tool calls of a loaded entry. Only the
// display fields of psi's own tools are kept.
func entryToolSummaries(e *Entry) map[string]event.ToolSummary {
	if e.Message == nil {
		return nil
	}
	m := e.Message.Message
	switch {
	case m.Assistant != nil:
		var summaries map[string]event.ToolSummary
		for _, block := range m.Assistant.Content {
			if block.Call == nil || block.Call.ID == "" {
				continue
			}
			if summaries != nil && len(summaries) >= maxToolSummariesPerEntry {
				break
			}
			if summaries == nil {
				summaries = make(map[string]event.ToolSummary)
			}
			summaries[block.Call.ID] = tools.SummarizeArguments(block.Call.Name, block.Call.Arguments, "", "")
		}
		return summaries
	case m.BashExecution != nil:
		return map[string]event.ToolSummary{
			e.ID(): tools.SummarizeArguments("bash", map[string]any{"command": m.BashExecution.Command}, "", ""),
		}
	}
	return nil
}
