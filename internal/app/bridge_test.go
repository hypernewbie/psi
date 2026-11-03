package app

import (
	"errors"
	"testing"

	"psi/internal/event"
	"psi/internal/pibridge"
)

func TestRunOutcomeOf(t *testing.T) {
	cases := []struct {
		name string
		ev   event.Event
		want pibridge.Outcome
	}{
		{"interrupted wins over error", event.Event{Outcome: event.OutcomeInterrupted, Err: errors.New("canceled")}, pibridge.OutcomeAborted},
		{"error", event.Event{Outcome: event.OutcomeUnsaved, Err: errors.New("storage")}, pibridge.OutcomeError},
		{"completed", event.Event{Outcome: event.OutcomeDurable}, pibridge.OutcomeCompleted},
		{"unknown", event.Event{Outcome: event.OutcomeTransient}, pibridge.OutcomeUnspecified},
	}
	for _, tc := range cases {
		if got := runOutcomeOf(tc.ev); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSyncBridgeWithoutEnvStaysPlain(t *testing.T) {
	for _, key := range []string{
		"PHI_PI_BRIDGE_ENDPOINT", "PHI_PI_BRIDGE_PANE_ID", "PHI_PI_BRIDGE_PROCESS_ID",
		"PHI_PI_BRIDGE_TOKEN", "PHI_PI_ATTACHMENT_ROOT",
	} {
		t.Setenv(key, "")
	}
	r := &runtime{generation: 1}
	r.syncBridge()
	if r.bridge != nil {
		t.Fatal("bridge started without provisioned environment")
	}
	if !r.bridgeTried {
		t.Fatal("environment was probed more than once")
	}
	r.syncBridge()
}

func TestSyncBridgeWaitsForGeneration(t *testing.T) {
	t.Setenv("PHI_PI_BRIDGE_ENDPOINT", "unix:/tmp/never.sock")
	t.Setenv("PHI_PI_BRIDGE_PANE_ID", "p")
	t.Setenv("PHI_PI_BRIDGE_PROCESS_ID", "q")
	t.Setenv("PHI_PI_BRIDGE_TOKEN", "t")
	r := &runtime{}
	r.syncBridge()
	if r.bridgeTried || r.bridge != nil {
		t.Fatal("bridge probed before the runtime generation was valid")
	}
}
