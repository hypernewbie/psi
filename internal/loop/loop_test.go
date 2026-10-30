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

func TestBuildPromptAndNudgePromptAllModes(t *testing.T) {
	// Forever
	foreverState := State{Active: true, Forever: true, Goal: "run test suite"}
	bp := BuildPrompt(foreverState)
	if !strings.Contains(bp, "Forever Loop") || !strings.Contains(bp, "run test suite") {
		t.Errorf("BuildPrompt forever = %q", bp)
	}
	np := NudgePrompt(foreverState)
	if !strings.Contains(np, "The forever loop remains active") {
		t.Errorf("NudgePrompt forever = %q", np)
	}

	// Passes: step 0 (initial)
	passInitial := State{Active: true, Mode: ModePasses, CurrentStep: 0, MaxSteps: 3, Goal: "refactor code"}
	bp = BuildPrompt(passInitial)
	if !strings.Contains(bp, "Pass 1 of 3") || !strings.Contains(bp, "initial implementation") {
		t.Errorf("BuildPrompt pass initial = %q", bp)
	}
	np = NudgePrompt(passInitial)
	if !strings.Contains(np, "pass 1/3") {
		t.Errorf("NudgePrompt pass initial = %q", np)
	}

	// Passes: step 1 (refinement)
	passRefine := State{Active: true, Mode: ModePasses, CurrentStep: 1, MaxSteps: 3, Goal: "refactor code"}
	bp = BuildPrompt(passRefine)
	if !strings.Contains(bp, "Pass 2 of 3") || !strings.Contains(bp, "refinement pass") {
		t.Errorf("BuildPrompt pass refine = %q", bp)
	}

	// Passes: step 2 (final pass)
	passFinal := State{Active: true, Mode: ModePasses, CurrentStep: 2, MaxSteps: 3, Goal: "refactor code"}
	bp = BuildPrompt(passFinal)
	if !strings.Contains(bp, "final pass") || !strings.Contains(bp, "Pass 3 of 3") {
		t.Errorf("BuildPrompt pass final = %q", bp)
	}

	// Goal mode
	goalState := State{Active: true, Mode: ModeGoal, CurrentStep: 1, Goal: "fix bugs"}
	bp = BuildPrompt(goalState)
	if !strings.Contains(bp, "Iteration 2") || !strings.Contains(bp, "fix bugs") {
		t.Errorf("BuildPrompt goal = %q", bp)
	}
	np = NudgePrompt(goalState)
	if !strings.Contains(np, "iteration 2") {
		t.Errorf("NudgePrompt goal = %q", np)
	}
}

func TestSystemPromptAdditionAndToolDefinition(t *testing.T) {
	inactive := State{Active: false}
	if got := SystemPromptAddition(inactive); got != "" {
		t.Errorf("SystemPromptAddition(inactive) = %q, want empty", got)
	}

	forever := State{Active: true, Forever: true, Goal: "monitor"}
	gotForever := SystemPromptAddition(forever)
	if !strings.Contains(gotForever, "Active Forever Loop") {
		t.Errorf("SystemPromptAddition(forever) = %q", gotForever)
	}

	goal := State{Active: true, Mode: ModeGoal, CurrentStep: 0, Goal: "ship feature"}
	gotGoal := SystemPromptAddition(goal)
	if !strings.Contains(gotGoal, "Step: 1/∞") || !strings.Contains(gotGoal, "loop_control") {
		t.Errorf("SystemPromptAddition(goal) = %q", gotGoal)
	}

	passes := State{Active: true, Mode: ModePasses, CurrentStep: 1, MaxSteps: 4, Goal: "optimize"}
	gotPasses := SystemPromptAddition(passes)
	if !strings.Contains(gotPasses, "Step: 2/4") {
		t.Errorf("SystemPromptAddition(passes) = %q", gotPasses)
	}

	td := ToolDefinition()
	if td.Name != ControlToolName {
		t.Errorf("ToolDefinition name = %q, want %q", td.Name, ControlToolName)
	}
	if td.Description == "" || td.Schema == nil {
		t.Errorf("ToolDefinition invalid: %+v", td)
	}
}

func TestStatusLineAndSetStore(t *testing.T) {
	store1, controller := newTestController(t)

	// Inactive
	if s := controller.StatusLine(); s != "" {
		t.Errorf("StatusLine(inactive) = %q, want empty", s)
	}

	// Active Passes
	if _, err := controller.Start("3 write tests"); err != nil {
		t.Fatal(err)
	}
	status := controller.StatusLine()
	if !strings.Contains(status, "pass 1/3") || !strings.Contains(status, "write tests") {
		t.Errorf("StatusLine(passes) = %q", status)
	}

	// Switch store via SetStore
	store2, err := session.New(session.OpenOptions{Cwd: t.TempDir(), AgentDir: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.SetStore(store2); err != nil {
		t.Fatal(err)
	}
	// Newly attached empty store should reset to inactive
	if s := controller.StatusLine(); s != "" {
		t.Errorf("StatusLine after SetStore to empty = %q, want empty", s)
	}

	// SetStore back to store1 should reconstruct the active state
	if err := controller.SetStore(store1); err != nil {
		t.Fatal(err)
	}
	if s := controller.StatusLine(); !strings.Contains(s, "pass 1/3") {
		t.Errorf("StatusLine after SetStore back = %q, want pass 1/3", s)
	}
}

func TestControllerConcurrentUsage(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("10 stress test"); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				_ = controller.Snapshot()
				_ = controller.StatusLine()
				_, _ = controller.ContinuePrompt()
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func FuzzParseLoop(f *testing.F) {
	seeds := []string{
		"goal finish migration",
		"forever run watcher",
		"3 polish app",
		"100 test",
		"goal",
		"forever",
		"",
		"-5 negative",
		"0 zero",
		"abc def",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		st, err := Parse(input)
		if err == nil {
			if !validState(st) {
				t.Fatalf("Parse accepted invalid state: %+v for input %q", st, input)
			}
		}
	})
}
