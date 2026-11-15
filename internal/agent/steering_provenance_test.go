package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
)

// queuedLoop is the smallest loop that can deliver queued input: delivery only
// touches the store and the sink, never a provider.
func queuedLoop(t *testing.T) (*Loop, *session.Store) {
	t.Helper()
	store, err := session.New(session.OpenOptions{Cwd: t.TempDir(), SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return &Loop{store: store, sink: func(event.Event) {}}, store
}

func provenanceTargets(t *testing.T, store *session.Store) []string {
	t.Helper()
	entries, err := store.ActivePath()
	if err != nil {
		t.Fatalf("active path: %v", err)
	}
	var targets []string
	for _, entry := range entries {
		if entry.Custom == nil || entry.Custom.CustomType != "psi.input" {
			continue
		}
		var payload struct {
			TargetID   string `json:"targetId"`
			Invocation string `json:"invocation"`
			Source     string `json:"source"`
		}
		if err := json.Unmarshal(entry.Custom.Data, &payload); err != nil {
			t.Fatalf("provenance payload: %v", err)
		}
		targets = append(targets, payload.Invocation+"/"+payload.Source+"->"+payload.TargetID)
	}
	return targets
}

// A skill queued while a run is active must keep the invocation it came from.
// Dropping it would show the expanded body as if the user typed it.
func TestSteeringKeepsProvenanceThroughLateDelivery(t *testing.T) {
	loop, store := queuedLoop(t)
	block := skillInvocationText("alpha", "ALPHA_BODY", "")
	loop.SteerWithProvenance(block, "/skill:alpha", "skill")

	delivered, err := loop.deliverQueuedSteering()
	if err != nil || !delivered {
		t.Fatalf("deliver = %v, %v", delivered, err)
	}
	entries, err := store.ActivePath()
	if err != nil {
		t.Fatalf("active path: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want message plus provenance", len(entries))
	}
	if entries[0].Message == nil || entries[0].Message.Message.User.Content != block {
		t.Fatalf("first entry is not the queued block: %+v", entries[0].Message)
	}
	targets := provenanceTargets(t, store)
	want := "/skill:alpha/skill->" + entries[0].ID()
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("provenance = %v, want %q", targets, want)
	}
}

func skillInvocationText(name, body, args string) string {
	text := "<skill name=\"" + name + "\" location=\"/skills/" + name + "/SKILL.md\">\n" +
		"References are relative to /skills/" + name + ".\n\n" + body + "\n</skill>"
	if args != "" {
		text += "\n\n" + args
	}
	return text
}

func lastProviderUserText(req provider.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		var text strings.Builder
		for _, block := range req.Messages[i].Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
			}
		}
		return text.String()
	}
	return ""
}

func deliveredSkillTexts(events []event.Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Type == event.SteeringDelivered || ev.Type == event.FollowUpDelivered {
			out = append(out, ev.Text)
		}
	}
	return out
}

// Enter steers and Alt+Enter follows up. Steering is delivered before the
// follow-up, and both retain the exact block and its invocation provenance.
func TestSkillQueuesKeepProvenanceAndPriority(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	var events []event.Event
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	fake.Responses = []*provider.Response{
		{Text: "first answer", StopReason: "stop"},
		{Text: "after steering", StopReason: "stop"},
		{Text: "after follow-up", StopReason: "stop"},
	}
	steerBlock := skillInvocationText("steer", "STEER_BODY", "steer-args")
	followBlock := skillInvocationText("follow", "FOLLOW_BODY", "follow-args")
	loop.SteerWithProvenance(steerBlock, "/skill:steer steer-args", "skill")
	loop.FollowUpWithProvenance(followBlock, "/skill:follow follow-args", "skill")
	// Queueing alone has no persistence or provider effect.
	if fake.CallCount() != 0 || len(store.Entries()) != 0 {
		t.Fatalf("enqueue had side effects: calls=%d entries=%d", fake.CallCount(), len(store.Entries()))
	}
	if err := loop.Submit(context.Background(), "initial", Config{Model: modelMeta(), Tools: toolDefs()}); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want steering then follow-up", len(requests))
	}
	if got := lastProviderUserText(requests[0]); got != steerBlock {
		t.Fatalf("steering request user = %q, want %q", got, steerBlock)
	}
	for _, message := range requests[0].Messages {
		for _, block := range message.Content {
			if block.Type == "text" && strings.Contains(block.Text, "FOLLOW_BODY") {
				t.Fatal("follow-up arrived in the steering request")
			}
		}
	}
	if got := lastProviderUserText(requests[1]); got != followBlock {
		t.Fatalf("follow-up request user = %q, want %q", got, followBlock)
	}
	if got := deliveredSkillTexts(events); len(got) != 2 || got[0] != steerBlock || got[1] != followBlock {
		t.Fatalf("delivery order = %q", got)
	}
	var stored []string
	for _, entry := range store.Entries() {
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.User != nil {
			if text, ok := entry.Message.Message.User.Content.(string); ok {
				stored = append(stored, text)
			}
		}
	}
	if len(stored) != 3 || stored[0] != "initial" || stored[1] != steerBlock || stored[2] != followBlock {
		t.Fatalf("stored user texts = %q", stored)
	}
	targets := provenanceTargets(t, store)
	if len(targets) != 2 || !strings.HasPrefix(targets[0], "/skill:steer steer-args/skill->") || !strings.HasPrefix(targets[1], "/skill:follow follow-args/skill->") {
		t.Fatalf("provenance = %v", targets)
	}
}

// A provenance failure after the durable user append must not queue that user
// again or report delivery.
func TestSkillFollowUpProvenanceFailureKeepsDurableUserOnce(t *testing.T) {
	store, reg, _, _, _ := testSetup(t)
	var events []event.Event
	loop := New(store, reg, nil, func(e event.Event) { events = append(events, e) })
	block := skillInvocationText("doer", "SKILL_BODY", "run-it")
	later := skillInvocationText("later", "LATER_BODY", "later-args")
	loop.FollowUpWithProvenance(block, "/skill:doer run-it", "skill")
	loop.FollowUpWithProvenance(later, "/skill:later later-args", "skill")
	injected := errors.New("injected provenance failure")
	store.FailWritesAfterForTest(1, injected)
	delivered, err := loop.deliverFollowUp()
	if delivered || !errors.Is(err, injected) {
		t.Fatalf("delivery = %v, %v; want the injected failure", delivered, err)
	}
	if pending := loop.PendingFollowUp(); len(pending) != 1 || pending[0] != later {
		t.Fatalf("remainder = %q, want only the later whole block", pending)
	}
	if cleared := loop.ClearFollowUp(); len(cleared) != 1 || cleared[0] != "/skill:later later-args" {
		t.Fatalf("remainder recovery = %q, want the later invocation", cleared)
	}
	users, provenance := 0, 0
	for _, entry := range store.Entries() {
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.User != nil {
			if entry.Message.Message.User.Content != block {
				t.Fatalf("stored user = %q", entry.Message.Message.User.Content)
			}
			users++
		}
		if entry.Kind == "custom" && entry.Custom != nil && entry.Custom.CustomType == "psi.input" {
			provenance++
		}
	}
	if users != 1 || provenance != 0 {
		t.Fatalf("users=%d provenance=%d, want one durable user without provenance", users, provenance)
	}
	if delivered := deliveredSkillTexts(events); len(delivered) != 0 {
		t.Fatalf("failed delivery reported %q", delivered)
	}
}

// A user-append failure keeps the complete undelivered batch in order.
func TestSkillFollowUpAppendFailureKeepsWholeBatch(t *testing.T) {
	store, reg, _, _, _ := testSetup(t)
	var events []event.Event
	loop := New(store, reg, nil, func(e event.Event) { events = append(events, e) })
	first := skillInvocationText("first", "FIRST_BODY", "")
	second := skillInvocationText("second", "SECOND_BODY", "")
	loop.FollowUpWithProvenance(first, "/skill:first", "skill")
	loop.FollowUpWithProvenance(second, "/skill:second", "skill")
	injected := errors.New("injected user append failure")
	store.FailWritesAfterForTest(0, injected)
	delivered, err := loop.deliverFollowUp()
	if delivered || !errors.Is(err, injected) {
		t.Fatalf("delivery = %v, %v; want the injected failure", delivered, err)
	}
	if pending := loop.PendingFollowUp(); len(pending) != 2 || pending[0] != first || pending[1] != second {
		t.Fatalf("pending = %q, want both whole blocks", pending)
	}
	if cleared := loop.ClearFollowUp(); len(cleared) != 2 || cleared[0] != "/skill:first" || cleared[1] != "/skill:second" {
		t.Fatalf("recovery = %q, want both invocations", cleared)
	}
	if len(store.Entries()) != 0 {
		t.Fatalf("failed delivery persisted %d entries", len(store.Entries()))
	}
	if delivered := deliveredSkillTexts(events); len(delivered) != 0 {
		t.Fatalf("failed delivery reported %q", delivered)
	}
}

// ContinuePending is the real post-settlement entry point: a provenance
// failure must stop before any provider request.
func TestSkillFollowUpProvenanceFailureStopsContinuation(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	var events []event.Event
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	fake.Responses = []*provider.Response{{Text: "must not run", StopReason: "stop"}}
	block := skillInvocationText("doer", "SKILL_BODY", "run-it")
	loop.FollowUpWithProvenance(block, "/skill:doer run-it", "skill")
	injected := errors.New("injected provenance failure")
	store.FailWritesAfterForTest(1, injected)
	if err := loop.ContinuePending(context.Background(), Config{Model: modelMeta(), Tools: toolDefs()}); !errors.Is(err, injected) {
		t.Fatalf("ContinuePending error = %v, want the injected failure", err)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("provider calls = %d, want none", fake.CallCount())
	}
	if delivered := deliveredSkillTexts(events); len(delivered) != 0 {
		t.Fatalf("failed continuation reported %q", delivered)
	}
	if pending := loop.PendingFollowUp(); len(pending) != 0 {
		t.Fatalf("durable user was queued again: %q", pending)
	}
}

func TestSkillRecoveryTextUsesInvocation(t *testing.T) {
	block := skillInvocationText("doer", strings.Repeat("BODY", 60_000), "run-it")
	skill := UserInput{Content: block, Display: block, Invocation: "/skill:doer run-it", Source: "skill"}
	if got := skill.RecoveryText(); got != "/skill:doer run-it" {
		t.Fatalf("skill recovery text = %q", got)
	}
	if len(skill.RecoveryText()) >= len(block) {
		t.Fatal("skill recovery kept the expanded body")
	}
	plain := UserInput{Content: "plain", Display: "plain"}
	if got := plain.RecoveryText(); got != "plain" {
		t.Fatalf("plain recovery text = %q", got)
	}
}

// Plain steering stays a plain user message: no provenance entry is invented.
func TestSteeringWithoutInvocationAppendsOnlyTheMessage(t *testing.T) {
	loop, store := queuedLoop(t)
	loop.Steer("plain note")
	if delivered, err := loop.deliverQueuedSteering(); err != nil || !delivered {
		t.Fatalf("deliver = %v, %v", delivered, err)
	}
	entries, _ := store.ActivePath()
	if len(entries) != 1 || !strings.Contains(entries[0].Message.Message.User.Content.(string), "plain note") {
		t.Fatalf("entries = %+v, want one plain message", entries)
	}
}

// A storage failure must keep the undelivered text queued, in order, so a
// retry cannot drop or reorder input.
func TestSteeringFailureKeepsTheRemainderQueued(t *testing.T) {
	store, err := session.New(session.OpenOptions{Cwd: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	loop := &Loop{store: store, sink: func(event.Event) {}}
	loop.SteerWithProvenance("first", "", "")
	loop.SteerWithProvenance("second", "", "")
	// Removing the session directory makes the append fail the way a full or
	// unwritable disk does: the store cannot write the record.
	if err := os.RemoveAll(store.Dir()); err != nil {
		t.Fatalf("remove session dir: %v", err)
	}
	if _, err := loop.deliverQueuedSteering(); err == nil {
		t.Fatal("delivery succeeded without a writable session")
	}
	pending := loop.PendingSteering()
	if len(pending) != 2 || pending[0] != "first" || pending[1] != "second" {
		t.Fatalf("pending = %q, want both messages intact", pending)
	}
}

// All-mode queues deliver every queued skill together, preserving order and
// one provenance entry per message.
func TestSkillQueuesDeliverTogetherInAllMode(t *testing.T) {
	t.Run("steering", func(t *testing.T) {
		store, reg, fake, _, _ := testSetup(t)
		fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
		loop := New(store, reg, fake, nil)
		first := skillInvocationText("first", "FIRST_BODY", "")
		second := skillInvocationText("second", "SECOND_BODY", "")
		loop.SetQueueModes(queueModeAll, "")
		loop.SteerWithProvenance(first, "/skill:first", "skill")
		loop.SteerWithProvenance(second, "/skill:second", "skill")
		if err := loop.Submit(context.Background(), "initial", Config{Model: modelMeta(), Tools: toolDefs()}); err != nil {
			t.Fatal(err)
		}
		requests := fake.Requests()
		if len(requests) != 1 {
			t.Fatalf("provider calls = %d, want one", len(requests))
		}
		if got := lastProviderUserText(requests[0]); got != second {
			t.Fatalf("last user = %q, want the second queued block", got)
		}
		joined := ""
		for _, message := range requests[0].Messages {
			for _, block := range message.Content {
				joined += block.Text
			}
		}
		if !strings.Contains(joined, first) || !strings.Contains(joined, second) {
			t.Fatal("all-mode steering did not deliver both skill blocks")
		}
		if targets := provenanceTargets(t, store); len(targets) != 2 || !strings.HasPrefix(targets[0], "/skill:first/skill->") || !strings.HasPrefix(targets[1], "/skill:second/skill->") {
			t.Fatalf("provenance = %v", targets)
		}
	})

	t.Run("follow-up", func(t *testing.T) {
		store, reg, fake, _, _ := testSetup(t)
		fake.Responses = []*provider.Response{{Text: "one", StopReason: "stop"}, {Text: "two", StopReason: "stop"}}
		loop := New(store, reg, fake, nil)
		first := skillInvocationText("first", "FIRST_BODY", "")
		second := skillInvocationText("second", "SECOND_BODY", "")
		loop.SetQueueModes("", queueModeAll)
		loop.FollowUpWithProvenance(first, "/skill:first", "skill")
		loop.FollowUpWithProvenance(second, "/skill:second", "skill")
		if err := loop.Submit(context.Background(), "initial", Config{Model: modelMeta(), Tools: toolDefs()}); err != nil {
			t.Fatal(err)
		}
		requests := fake.Requests()
		if len(requests) != 2 {
			t.Fatalf("provider calls = %d, want the run plus one follow-up batch", len(requests))
		}
		if got := lastProviderUserText(requests[1]); got != second {
			t.Fatalf("last user = %q, want the second queued block", got)
		}
		joined := ""
		for _, message := range requests[1].Messages {
			for _, block := range message.Content {
				joined += block.Text
			}
		}
		if !strings.Contains(joined, first) || !strings.Contains(joined, second) {
			t.Fatal("all-mode follow-up did not deliver both skill blocks")
		}
		if targets := provenanceTargets(t, store); len(targets) != 2 || !strings.HasPrefix(targets[0], "/skill:first/skill->") || !strings.HasPrefix(targets[1], "/skill:second/skill->") {
			t.Fatalf("provenance = %v", targets)
		}
	})
}
