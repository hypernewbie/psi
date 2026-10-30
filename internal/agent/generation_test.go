package agent

// PLAN3 F04: failed context construction sends no provider request.

import (
	"context"
	"os"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
)

// TestFailedContextSendsNoRequest corrupts a persisted payload mid-walk and
// requires Submit to fail before the provider is contacted again.
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
	// Corrupt the persisted user payload with identical length on next read.
	store.LoadHook = func(m session.RecordMeta) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			if strings.Contains(l, `"content":"hello"`) || strings.Contains(l, `hello`) {
				lines[i] = strings.Replace(l, "hello", "HELLO", 1)
				break
			}
		}
		_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
	}
	if err := loop.Submit(context.Background(), "again", cfg); err == nil {
		t.Fatal("Submit succeeded on mixed generation")
	} else if !strings.Contains(err.Error(), "changed externally") {
		t.Fatalf("wrong error: %v", err)
	}
	if len(fake.Calls) != callsBefore {
		t.Fatalf("provider contacted on failed context: %d -> %d", callsBefore, len(fake.Calls))
	}
}
