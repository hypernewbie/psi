package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/tools"
)

const readSessionToolName = "read_session"

// readSessionToolDefinition advertises a bounded read of the raw active-branch
// history. The compaction-aware projection hides entries that a summary
// replaced, so this tool exists to recover one specific missing fact.
func readSessionToolDefinition() provider.ToolDef {
	return provider.ToolDef{
		Name: readSessionToolName,
		Description: "Read a small, specific raw excerpt from earlier messages on the active branch of the current session, including compacted history. " +
			"Search or list first, then read a narrow range. Output is bounded and intended for specific missing facts, not whole sessions.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":       map[string]any{"type": "string", "enum": []string{"list", "search", "read"}},
				"query":        map[string]any{"type": "string", "description": "Literal text to search for; required for search"},
				"startEntryId": map[string]any{"type": "string", "description": "First entry ID to read; required for read"},
				"endEntryId":   map[string]any{"type": "string", "description": "Last entry ID to read; required for read"},
				"maxTokens":    map[string]any{"type": "integer", "minimum": 500, "maximum": 8000, "description": "Maximum estimated output tokens for read (default 4000); list and search use separate limits"},
			},
			"required": []string{"action"},
		},
	}
}

func (r *runtime) readSessionHandler(_ context.Context, invocation tools.Invocation) tools.Result {
	action, _ := invocation.Arguments["action"].(string)
	query, _ := invocation.Arguments["query"].(string)
	startID, _ := invocation.Arguments["startEntryId"].(string)
	endID, _ := invocation.Arguments["endEntryId"].(string)
	maxTokens := historyTokenArgument(invocation.Arguments["maxTokens"])

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

// historyTokenArgument clamps before converting to int, including on 32-bit
// hosts. Tool arguments normally arrive as json.Number via UseNumber.
func historyTokenArgument(value any) int {
	var n float64
	switch v := value.(type) {
	case json.Number:
		var err error
		n, err = v.Float64()
		if err != nil && !math.IsInf(n, 0) {
			return session.DefaultHistoryReadTokens
		}
	case float64:
		n = v
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	default:
		return session.DefaultHistoryReadTokens
	}
	if math.IsNaN(n) {
		return session.DefaultHistoryReadTokens
	}
	if n < session.MinHistoryReadTokens {
		return session.MinHistoryReadTokens
	}
	if n > session.MaxHistoryReadTokens {
		return session.MaxHistoryReadTokens
	}
	return int(n)
}
