package app

import (
	"context"
	"encoding/json"
	"fmt"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

const readSessionToolName = "read_session"

// readSessionToolDefinition advertises a bounded read of the raw active-branch
// history. The compaction-aware projection hides entries that a summary
// replaced, so this tool exists to recover one specific missing fact.
func readSessionToolDefinition() provider.ToolDef {
	return provider.ToolDef{
		Name: readSessionToolName,
		Description: "Read a small, specific raw excerpt from earlier messages in the current session. " +
			"Search or list first, then read a narrow range. This tool cannot read a whole session at once. " +
			"Use it only for a specific missing fact from earlier raw session history.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":       map[string]any{"type": "string", "enum": []string{"list", "search", "read"}},
				"query":        map[string]any{"type": "string", "description": "Literal text to search for; required for search"},
				"startEntryId": map[string]any{"type": "string", "description": "First entry ID to read; required for read"},
				"endEntryId":   map[string]any{"type": "string", "description": "Last entry ID to read; required for read"},
				"maxTokens":    map[string]any{"type": "integer", "minimum": 500, "maximum": 8000, "description": "Maximum bounded output size"},
			},
			"required": []string{"action"},
		},
	}
}

// readSessionAvailable advertises the tool only once a compaction summary is
// on the active branch. Before that every raw entry is already in the
// model-facing context, so there is nothing hidden to recover.
func (r *runtime) readSessionAvailable() bool {
	path, err := r.store.ActivePath()
	if err != nil {
		return false
	}
	for _, entry := range path {
		if entry.Kind == "compaction" {
			return true
		}
	}
	return false
}

func (r *runtime) readSessionHandler(_ context.Context, invocation tools.Invocation) tools.Result {
	action, _ := invocation.Arguments["action"].(string)
	query, _ := invocation.Arguments["query"].(string)
	startID, _ := invocation.Arguments["startEntryId"].(string)
	endID, _ := invocation.Arguments["endEntryId"].(string)
	maxTokens := intArgument(invocation.Arguments["maxTokens"])

	path, err := r.store.ActivePath()
	if err != nil {
		result := session.HistoryResult{
			Action: action,
			Error:  "the active session branch is unavailable",
			Text:   "Cannot read session history: the active session branch is unavailable.",
		}
		return tools.Result{Content: []tools.Block{{Type: "text", Text: result.Text}}, Details: result, IsError: true}
	}

	var result session.HistoryResult
	switch action {
	case session.HistoryActionList:
		result = session.ListHistory(path, 0)
	case session.HistoryActionSearch:
		result = session.SearchHistory(path, query, 0)
	case session.HistoryActionRead:
		result = session.ReadHistory(path, startID, endID, maxTokens)
	default:
		message := fmt.Sprintf("Unknown action %q. Use list, search, or read.", action)
		result = session.HistoryResult{
			Action: action,
			Error:  message,
			Text:   "Cannot read session history: " + message,
		}
	}
	return tools.Result{
		Content: []tools.Block{{Type: "text", Text: result.Text}},
		Details: result,
		IsError: !result.OK,
	}
}

// intArgument reads a JSON number. Agent tool calls are decoded with
// json.Decoder.UseNumber, so the common case is json.Number, not float64.
func intArgument(value any) int {
	switch v := value.(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
		if f, err := v.Float64(); err == nil {
			return int(f)
		}
		return 0
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}
