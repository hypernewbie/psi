package agent

// PLAN3 F04: failed context construction sends no provider request.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
)

// TestFailedContextSendsNoRequest corrupts a persisted payload and requires
// Submit to fail before the provider is contacted again.
func TestFailedContextSendsNoRequest(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}
	if err := loop.Submit(context.Background(), "hello", cfg); err != nil {
		t.Fatal(err)
	}
	callsBefore := len(fake.Calls)
	path := store.File()
	if path == "" {
		t.Fatal("expected persisted session file")
	}
	// Corrupt the persisted user payload with identical length.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.Contains(l, `"content":"hello"`) || strings.Contains(l, `hello`) {
			lines[i] = strings.Replace(l, "hello", "HELLO", 1)
			break
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loop.Submit(context.Background(), "again", cfg); err == nil {
		t.Fatal("Submit succeeded on a changed session file")
	} else if !strings.Contains(err.Error(), "changed externally") {
		t.Fatalf("wrong error: %v", err)
	}
	if len(fake.Calls) != callsBefore {
		t.Fatalf("provider contacted on failed context: %d -> %d", callsBefore, len(fake.Calls))
	}
}

// The compaction preparation runs on the loaded session: like Pi, no record is
// read from disk when the compaction starts.
func TestCompactionPreparationUsesTheLoadedSession(t *testing.T) {
	store, _, _, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	path := store.File()
	if path == "" {
		t.Fatal("expected persisted session file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err := loop.CheckCompaction(Config{KeepRecentTokens: 20000})
	var noWork *CompactionNoWorkError
	if !errors.As(err, &noWork) {
		t.Fatalf("preparation did not run on the loaded session: %v", err)
	}
}
