package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
)

// A failed stream must keep skill queues out of storage and the provider, then
// recover both queues as short invocations with their arguments intact.
func TestSkillBlockRecoveryAfterProviderFailure(t *testing.T) {
	r := testRuntime(t)
	steerBlock := replayedSkillText("steer", "STEER_BODY", "steer-args")
	followBlock := replayedSkillText("follow", "FOLLOW_BODY", "follow-args")
	streamErr := errors.New("scripted stream failure")
	fake := &provider.Fake{
		TextDeltas: [][]string{{"partial answer"}},
		Errors:     []error{streamErr},
	}
	var once sync.Once
	var loop *agent.Loop
	loop = agent.New(r.store, r.reg, fake, func(ev event.Event) {
		if ev.Type != event.TextDelta {
			return
		}
		once.Do(func() {
			loop.SteerWithProvenance(steerBlock, "/skill:steer steer-args", "skill")
			loop.FollowUpWithProvenance(followBlock, "/skill:follow follow-args", "skill")
		})
	})
	if err := loop.Submit(context.Background(), "initial", r.cfg); err == nil {
		t.Fatal("scripted stream failure did not stop the run")
	}
	if fake.CallCount() != 1 {
		t.Fatalf("provider calls = %d, want one failed stream", fake.CallCount())
	}
	for _, entry := range r.store.Entries() {
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.User != nil {
			if content, ok := entry.Message.Message.User.Content.(string); ok && strings.Contains(content, "<skill name=") {
				t.Fatalf("failed stream persisted a queued skill: %q", content)
			}
		}
		if entry.Kind == "custom" && entry.Custom != nil && entry.Custom.CustomType == "psi.input" {
			t.Fatalf("failed stream persisted skill provenance: %s", entry.Custom.Data)
		}
	}
	texts := r.takeSteering(loop)
	if len(texts) != 1 || texts[0] != "/skill:steer steer-args" {
		t.Fatalf("steering recovery = %q", texts)
	}
	if got := loop.ClearFollowUp(); len(got) != 1 || got[0] != "/skill:follow follow-args" {
		t.Fatalf("follow-up recovery = %q", got)
	}
	if pending := loop.PendingSteering(); len(pending) != 0 {
		t.Fatalf("steering queue = %q, want empty", pending)
	}
	if pending := loop.PendingFollowUp(); len(pending) != 0 {
		t.Fatalf("follow-up queue = %q, want empty", pending)
	}
}
