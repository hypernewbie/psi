package loop

import (
	"strings"
	"testing"
)

// A goal loop has one model-facing verb, the completion claim. The controller
// owns its iteration boundaries, so loop_next must stay out of the tool set
// and a stale call must not move the loop.
func TestGoalLoopHasNoAdvanceTool(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("goal ship it"); err != nil {
		t.Fatal(err)
	}
	state := controller.Snapshot()
	if defs := ToolDefinitions(state); len(defs) != 1 || defs[0].Name != DoneToolName {
		t.Fatalf("goal working tools = %+v", defs)
	}
	for _, text := range []string{BuildPrompt(state), SystemPromptAddition(state), NudgePrompt(state)} {
		if strings.Contains(text, NextToolName) {
			t.Fatalf("goal prompt advertises %s: %q", NextToolName, text)
		}
	}

	result := controller.HandleNext(map[string]any{"summary": "skip ahead"})
	if !result.Error || !strings.Contains(result.Text, "count loop") {
		t.Fatalf("goal next = %+v", result)
	}
	if after := controller.Snapshot(); after.CurrentStep != state.CurrentStep || after.Done || !after.Active {
		t.Fatalf("goal next changed state: %+v", after)
	}
}

// Count loops keep both the advance and the claim tool in the working phase.
func TestCountLoopKeepsAdvanceTool(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("3 ship it"); err != nil {
		t.Fatal(err)
	}
	state := controller.Snapshot()
	defs := ToolDefinitions(state)
	if len(defs) != 2 || defs[0].Name != NextToolName || defs[1].Name != DoneToolName {
		t.Fatalf("count working tools = %+v", defs)
	}
	if !strings.Contains(BuildPrompt(state), NextToolName) {
		t.Fatalf("count prompt does not advertise %s: %q", NextToolName, BuildPrompt(state))
	}
	if result := controller.HandleNext(nil); result.Error || result.State.CurrentStep != 1 {
		t.Fatalf("count next = %+v", result)
	}
}
