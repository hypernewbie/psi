package loop

import (
	"strings"
	"testing"

	"psi/internal/session"
)

func newTestController(t *testing.T) (*session.Store, *Controller) {
	t.Helper()
	store, err := session.New(session.OpenOptions{Cwd: t.TempDir(), AgentDir: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	return store, NewController(store)
}

func TestParseLoopModesAndRejectsLooseCounts(t *testing.T) {
	goal, err := Parse("goal finish the migration")
	if err != nil || !goal.Active || goal.Mode != ModeGoal || goal.MaxSteps != 0 || goal.Goal != "finish the migration" {
		t.Fatalf("goal = %+v, err=%v", goal, err)
	}
	forever, err := Parse("forever keep checking tests")
	if err != nil || !forever.Active || !forever.Forever || forever.Mode != ModeGoal {
		t.Fatalf("forever = %+v, err=%v", forever, err)
	}
	count, err := Parse("3 improve the result")
	if err != nil || count.Mode != ModePasses || count.MaxSteps != 3 || count.Goal != "improve the result" {
		t.Fatalf("count = %+v, err=%v", count, err)
	}
	for _, input := range []string{"", "goal", "forever", "0 task", "3abc task", "-2 task", "2"} {
		if _, err := Parse(input); err == nil {
			t.Errorf("Parse(%q) accepted invalid input", input)
		}
	}
}

func TestLoopControlExactCountAndGoalCompletion(t *testing.T) {
	_, controller := newTestController(t)
	state, err := controller.Start("2 polish")
	if err != nil || state.CurrentStep != 0 {
		t.Fatalf("start = %+v, err=%v", state, err)
	}
	first := controller.HandleControl(map[string]any{"status": "next", "summary": "first pass"})
	if first.Error || !first.State.Active || first.State.CurrentStep != 1 || !strings.Contains(first.Text, "pass 2") {
		t.Fatalf("first pass result = %+v", first)
	}
	last := controller.HandleControl(map[string]any{"status": "next", "summary": "second pass"})
	if last.Error || last.State.Active || !last.State.Done || last.State.CurrentStep != 2 || !strings.Contains(last.Text, "all 2 iterations") {
		t.Fatalf("last pass result = %+v", last)
	}
	goal, err := controller.Start("goal complete the task")
	if err != nil || !goal.Active {
		t.Fatalf("goal start = %+v, err=%v", goal, err)
	}
	done := controller.HandleControl(map[string]any{"status": "done", "summary": "finished", "reason": "all checks pass"})
	if done.Error || done.State.Active || done.State.ReasonDone != "all checks pass" {
		t.Fatalf("goal done result = %+v", done)
	}
}

func TestLoopStateReconstructsFromActiveSessionBranch(t *testing.T) {
	store, controller := newTestController(t)
	if _, err := controller.Start("goal persist state"); err != nil {
		t.Fatal(err)
	}
	if result := controller.HandleControl(map[string]any{"status": "next", "summary": "one"}); result.Error {
		t.Fatal(result.Text)
	}
	restored := NewController(store)
	if err := restored.Reconstruct(); err != nil {
		t.Fatal(err)
	}
	state := restored.Snapshot()
	if !state.Active || state.CurrentStep != 1 || state.Goal != "persist state" {
		t.Fatalf("restored state = %+v", state)
	}
	if _, stopped, err := restored.Stop("user stop"); err != nil || !stopped {
		t.Fatalf("stop returned stopped=%v err=%v", stopped, err)
	}
	restored = NewController(store)
	if err := restored.Reconstruct(); err != nil {
		t.Fatal(err)
	}
	if state := restored.Snapshot(); state.Active || !state.Done || state.ReasonDone != "user stop" {
		t.Fatalf("restored stopped state = %+v", state)
	}
}

func TestForeverLoopHasNoControlToolAndCannotBeControlled(t *testing.T) {
	_, controller := newTestController(t)
	state, err := controller.Start("forever keep testing")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(SystemPromptAddition(state), "loop_control") {
		t.Fatal("forever system prompt exposed loop_control")
	}
	result := controller.HandleControl(map[string]any{"status": "next", "summary": "stale"})
	if !result.Error || !result.State.Active || !strings.Contains(result.Text, "unavailable") {
		t.Fatalf("forever stale tool call = %+v", result)
	}
	if prompt, ok := controller.ContinuePrompt(); !ok || !strings.Contains(prompt, "Only the user can stop") {
		t.Fatalf("forever continuation = %q, %v", prompt, ok)
	}
}
