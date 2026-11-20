package loop

import (
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/session"
)

func TestLoopNextAdvancesAndCountLoopCloses(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("2 polish the result"); err != nil {
		t.Fatal(err)
	}
	first := controller.HandleNext(map[string]any{"summary": "layout done"})
	if first.Error || !first.State.Active || first.State.CurrentStep != 1 || first.State.Done {
		t.Fatalf("first next = %+v", first)
	}
	if !strings.Contains(first.Text, "Continuing to iteration 2") || !strings.Contains(first.Text, "layout done") {
		t.Fatalf("first next text = %q", first.Text)
	}
	last := controller.HandleNext(nil)
	if last.Error || last.State.Active || !last.State.Done || last.State.CurrentStep != 2 {
		t.Fatalf("last next = %+v", last)
	}
	if last.State.ReasonDone != "Completed all passes" || !strings.Contains(last.Text, "all 2 passes") {
		t.Fatalf("completion = %+v", last)
	}
	if defs := ToolDefinitions(last.State); len(defs) != 0 {
		t.Fatalf("completed loop exposed tools: %+v", defs)
	}
}

// A turn end right after loop_next keeps the current iteration instead of
// counting it a second time.
func TestTurnEndAfterLoopNextDoesNotDoubleCount(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("goal keep working"); err != nil {
		t.Fatal(err)
	}
	if result := controller.HandleNext(nil); result.Error {
		t.Fatal(result.Text)
	}
	messageType, prompt, ok, err := controller.ContinuePrompt()
	if err != nil || !ok || messageType != NudgeMessageType {
		t.Fatalf("post-next turn end = %q %v %v", messageType, ok, err)
	}
	if state := controller.Snapshot(); state.CurrentStep != 1 {
		t.Fatalf("iteration counted twice: %+v", state)
	}
	if !strings.Contains(prompt, "iteration 2") || !strings.Contains(prompt, "Continue working") {
		t.Fatalf("nudge prompt = %q", prompt)
	}
	messageType, prompt, ok, err = controller.ContinuePrompt()
	if err != nil || !ok || messageType != IterationMessageType {
		t.Fatalf("plain turn end = %q %v %v", messageType, ok, err)
	}
	if state := controller.Snapshot(); state.CurrentStep != 2 {
		t.Fatalf("plain turn end did not advance: %+v", state)
	}
	if !strings.Contains(prompt, "Iteration 3") {
		t.Fatalf("iteration prompt = %q", prompt)
	}
}

// A count loop that reaches its cap at a turn end announces itself once and
// does not continue the run.
func TestCountLoopCompletesAtTurnEnd(t *testing.T) {
	store, controller := newTestController(t)
	if _, err := controller.Start("1 single pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "start"}}); err != nil {
		t.Fatal(err)
	}
	messageType, content, ok, err := controller.ContinuePrompt()
	if err != nil || ok || messageType != "" || content != "" {
		t.Fatalf("completing turn end = %q %q %v %v", messageType, content, ok, err)
	}
	if state := controller.Snapshot(); state.Active || !state.Done || state.CurrentStep != 1 {
		t.Fatalf("completed state = %+v", state)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, entry := range path {
		if entry.CustomMessage != nil && entry.CustomMessage.CustomType == CompleteMessageType {
			notices++
			if !entry.CustomMessage.Display {
				t.Fatal("completion notice is hidden")
			}
		}
	}
	if notices != 1 {
		t.Fatalf("completion notices = %d, want 1", notices)
	}
	// The completed loop stays closed on the next turn end.
	if _, _, ok, err := controller.ContinuePrompt(); err != nil || ok {
		t.Fatalf("completed loop continued: ok=%v err=%v", ok, err)
	}
}

func TestLoopNextUnavailableInForeverAndDuringCheck(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("forever watch the build"); err != nil {
		t.Fatal(err)
	}
	if result := controller.HandleNext(nil); !result.Error || !strings.Contains(result.Text, "unavailable") {
		t.Fatalf("forever next = %+v", result)
	}
	if _, _, err := controller.Stop("done"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Start("goal check first"); err != nil {
		t.Fatal(err)
	}
	if result := controller.HandleDone(map[string]any{"reason": "looks done"}); result.Error {
		t.Fatal(result.Text)
	}
	if result := controller.HandleNext(nil); !result.Error || !strings.Contains(result.Text, "being checked") {
		t.Fatalf("next during check = %+v", result)
	}
}
