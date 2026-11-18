package app

import (
	"context"
	"strings"
	"testing"

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

// The tool is only advertised once a compaction can hide raw entries.
func TestReadSessionAvailableFollowsCompaction(t *testing.T) {
	r := testRuntime(t)
	if r.readSessionAvailable() {
		t.Fatal("tool advertised without a compaction")
	}
	if _, err := r.store.AppendMessage(agentMessageUser("kept")); err != nil {
		t.Fatal(err)
	}
	path, err := r.store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AppendCompaction("summary", path[0].ID(), 10, nil); err != nil {
		t.Fatal(err)
	}
	if !r.readSessionAvailable() {
		t.Fatal("tool not advertised after a compaction")
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
	if !strings.Contains(definition.Description, "cannot read a whole session") {
		t.Fatalf("description = %q", definition.Description)
	}
}
