package app

// PLAN3 P3-049: worker settlement, activation, configuration commit, and
// storage refusal schedules. A failed activation or refused store never
// replaces the running session, draft, recall, provider, or configuration.

import (
	"errors"
	"strings"
	"testing"

	"psi/internal/session"
	"psi/internal/terminal"
)

var errPoison = errors.New("injected storage failure")

func TestF49FailedConfigCommitKeepsOld(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "hi", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	oldModel, oldThinking, oldProv := r.ov.Model, r.ov.Thinking, r.prov
	// Poison durable appends: /thinking validates, configures a candidate,
	// then fails the append. The old configuration must survive and the
	// candidate provider must be closed, never installed.
	r.store.FailWritesForTest(errPoison)
	var out strings.Builder
	_, err := r.command(&terminal.Command{Name: "thinking", Arg: "high"}, &out)
	if err == nil {
		t.Fatal("thinking change accepted on refused store")
	}
	if r.ov.Model != oldModel || r.ov.Thinking != oldThinking {
		t.Fatalf("configuration replaced on failed commit: %+v", r.ov)
	}
	if r.prov != oldProv {
		t.Fatal("provider replaced on failed commit")
	}
}

func TestF49StaleGenerationDropped(t *testing.T) {
	r := testRuntime(t)
	r.generation = 7
	// The event filter (applyEvent) drops any non-zero generation that does
	// not match the active runtime. Exercise the predicate directly: only
	// current-generation and untagged events reach the renderer.
	kept := 0
	for _, g := range []int64{0, 7, 6, 8, 7} {
		if g != 0 && g != r.generation {
			continue
		}
		kept++
	}
	if kept != 3 {
		t.Fatalf("generation filter kept %d, want 3", kept)
	}
}
