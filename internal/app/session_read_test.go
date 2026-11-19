package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

// The tool reads the raw active branch, so a detail that a compaction summary
// replaced is still recoverable through search and read.
func TestReadSessionToolReadsRawBranchThroughCompaction(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.store.AppendMessage(agentMessageUser("first detail NEEDLE-7")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AppendMessage(agentMessageUser("answer")); err != nil {
		t.Fatal(err)
	}
	kept, err := r.store.AppendMessage(agentMessageUser("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AppendCompaction("summary", kept, 10, nil); err != nil {
		t.Fatal(err)
	}

	search := r.readSessionHandler(context.Background(), tools.Invocation{Name: readSessionToolName, Arguments: map[string]any{
		"action": "search", "query": "NEEDLE-7",
	}})
	if search.IsError || len(search.Content) != 1 {
		t.Fatalf("search result = %+v", search)
	}
	details, ok := search.Details.(session.HistoryResult)
	if !ok || len(details.ReturnedIDs) != 1 {
		t.Fatalf("search details = %#v", search.Details)
	}
	if !strings.Contains(search.Content[0].Text, "NEEDLE-7") {
		t.Fatalf("search text = %q", search.Content[0].Text)
	}

	id := details.ReturnedIDs[0]
	read := r.readSessionHandler(context.Background(), tools.Invocation{Name: readSessionToolName, Arguments: map[string]any{
		"action": "read", "startEntryId": id, "endEntryId": id, "maxTokens": float64(500),
	}})
	if read.IsError || !strings.Contains(read.Content[0].Text, "Session history excerpt") {
		t.Fatalf("read result = %+v", read)
	}

	list := r.readSessionHandler(context.Background(), tools.Invocation{Name: readSessionToolName, Arguments: map[string]any{"action": "list"}})
	if list.IsError || !strings.Contains(list.Content[0].Text, "Session history index") {
		t.Fatalf("list result = %+v", list)
	}

	unknown := r.readSessionHandler(context.Background(), tools.Invocation{Name: readSessionToolName, Arguments: map[string]any{"action": "delete"}})
	if !unknown.IsError || !strings.Contains(unknown.Content[0].Text, "Unknown action") {
		t.Fatalf("unknown action result = %+v", unknown)
	}
}

func TestReadSessionAvailableInRPCWithoutCompactionOrSelectedTools(t *testing.T) {
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	s.r.settings.DefaultTools = nil
	cfg := s.runConfig()
	_, _, definitions := cfg.DynamicConfig()
	if len(definitions) != 1 || definitions[0].Name != readSessionToolName {
		t.Fatalf("RPC tools = %+v", definitions)
	}
	if cfg.InternalTools[readSessionToolName] == nil {
		t.Fatal("RPC history handler missing")
	}
}

// Agent tool arguments arrive as json.Number (decoder.UseNumber), so the
// token bound must accept that type instead of silently falling back to the
// default.
func TestReadSessionToolBoundsWithJSONNumberTokens(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.store.AppendMessage(agentMessageUser(strings.Repeat("bulk ", 8_000))); err != nil {
		t.Fatal(err)
	}
	kept, err := r.store.AppendMessage(agentMessageUser("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AppendCompaction("summary", kept, 10, nil); err != nil {
		t.Fatal(err)
	}
	path, err := r.store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	result := r.readSessionHandler(context.Background(), tools.Invocation{Name: readSessionToolName, Arguments: map[string]any{
		"action": "read", "startEntryId": path[0].ID(), "endEntryId": path[0].ID(), "maxTokens": json.Number("500"),
	}})
	if result.IsError {
		t.Fatalf("read failed: %+v", result.Content)
	}
	details, _ := result.Details.(session.HistoryResult)
	if details.EstimatedTokens > 500 {
		t.Fatalf("estimated tokens = %d, want at most 500", details.EstimatedTokens)
	}
	if !strings.Contains(result.Content[0].Text, "bytes omitted") {
		t.Fatal("large entry was not bounded")
	}
}

func TestHistoryTokenArgumentClampsBeforeConversion(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  int
	}{
		{nil, 4000},
		{json.Number("500"), 500},
		{json.Number("0"), 500},
		{json.Number("-1"), 500},
		{json.Number("1e999"), 8000},
		{json.Number("-1e999"), 500},
		{json.Number("bogus"), 4000},
	} {
		if got := historyTokenArgument(tc.value); got != tc.want {
			t.Errorf("historyTokenArgument(%v) = %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestReadSessionToolDefinitionIsAdvertised(t *testing.T) {
	definition := readSessionToolDefinition()
	if definition.Name != readSessionToolName {
		t.Fatalf("tool name = %q", definition.Name)
	}
	properties, _ := definition.Schema["properties"].(map[string]any)
	action, _ := properties["action"].(map[string]any)
	enum, _ := action["enum"].([]string)
	if len(enum) != 3 || enum[0] != "list" || enum[1] != "search" || enum[2] != "read" {
		t.Fatalf("action enum = %#v", action["enum"])
	}
	if !strings.Contains(definition.Description, "Output is bounded") {
		t.Fatalf("description = %q", definition.Description)
	}
}
