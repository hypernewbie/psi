package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"psi/internal/event"
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
	loop.SteerWithProvenance("expanded alpha body", "/skill:alpha", "skill")

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
	if entries[0].Message == nil || entries[0].Message.Message.User.Content != "expanded alpha body" {
		t.Fatalf("first entry is not the queued text: %+v", entries[0].Message)
	}
	targets := provenanceTargets(t, store)
	want := "/skill:alpha/skill->" + entries[0].ID()
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("provenance = %v, want %q", targets, want)
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
