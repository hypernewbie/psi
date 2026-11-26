package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
)

func appendShell(t *testing.T, store *session.Store, command, output string, exclude bool) string {
	t.Helper()
	code := 0
	var fullPath *string
	if exclude {
		path := "EXCLUDED_SHELL_PATH"
		fullPath = &path
	}
	id, err := store.AppendMessage(session.AgentMessage{Role: "bashExecution", BashExecution: &session.BashExecutionMessage{
		Role:               "bashExecution",
		Command:            command,
		Output:             output,
		ExitCode:           &code,
		Truncated:          exclude,
		FullOutputPath:     fullPath,
		Timestamp:          10,
		ExcludeFromContext: &exclude,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func requestJSON(t *testing.T, request provider.Request) string {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBangShellContextPrivacy(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("older request")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, strings.Repeat("older answer ", 400))
	includedID := appendShell(t, store, "INCLUDED_SHELL_COMMAND", "INCLUDED_SHELL_OUTPUT", false)
	excludedID := appendShell(t, store, "EXCLUDED_SHELL_COMMAND", "EXCLUDED_SHELL_OUTPUT", true)
	if _, err := store.AppendMessage(userMsg("current request")); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{
		{Text: "ordinary answer", StopReason: "stop"},
		{Text: "summary of older work", StopReason: "stop"},
		{Text: "post compaction answer", StopReason: "stop"},
		{Text: "extra answer", StopReason: "stop"},
		{Text: "extra summary", StopReason: "stop"},
	}
	cfg := Config{Model: modelMeta(), KeepRecentTokens: 0}
	if err := loop.Submit(context.Background(), "ordinary continuation", cfg); err != nil {
		t.Fatal(err)
	}
	if err := loop.Compact(context.Background(), cfg, ""); err != nil {
		t.Fatal(err)
	}
	if err := loop.Submit(context.Background(), "after compaction", cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) < 2 {
		t.Fatalf("provider calls = %d", len(fake.Calls))
	}
	foundIncluded := false
	for i, call := range fake.Calls {
		encoded := requestJSON(t, call)
		if strings.Contains(encoded, "INCLUDED_SHELL_COMMAND") || strings.Contains(encoded, "INCLUDED_SHELL_OUTPUT") {
			foundIncluded = true
		}
		for _, excluded := range []string{"EXCLUDED_SHELL_COMMAND", "EXCLUDED_SHELL_OUTPUT", "EXCLUDED_SHELL_PATH"} {
			if strings.Contains(encoded, excluded) {
				t.Fatalf("request %d leaked %q: %s", i, excluded, encoded)
			}
		}
	}
	if !foundIncluded {
		t.Fatal("included shell context never reached a provider request")
	}

	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	firstKept := -1
	includedIndex, excludedIndex := -1, -1
	for i, entry := range path {
		if entry.Compaction != nil {
			for j, candidate := range path {
				if candidate.ID() == entry.Compaction.FirstKeptEntryID {
					firstKept = j
					break
				}
			}
		}
		if entry.ID() == includedID {
			includedIndex = i
		}
		if entry.ID() == excludedID {
			excludedIndex = i
		}
	}
	if firstKept < 0 || includedIndex < 0 || excludedIndex < 0 || includedIndex >= firstKept || excludedIndex >= firstKept {
		t.Fatalf("compaction cut firstKept=%d included=%d excluded=%d", firstKept, includedIndex, excludedIndex)
	}
}
