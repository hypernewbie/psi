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

func TestParseLoopModesAndRejectsCounts(t *testing.T) {
	goal, err := Parse("goal finish the migration")
	if err != nil || !goal.Active || goal.Forever || goal.Goal != "finish the migration" {
		t.Fatalf("goal = %+v, err=%v", goal, err)
	}
	forever, err := Parse("forever keep checking tests")
	if err != nil || !forever.Active || !forever.Forever || forever.Goal != "keep checking tests" {
		t.Fatalf("forever = %+v, err=%v", forever, err)
	}
	if _, err := Parse("3 improve the result"); err == nil || !strings.Contains(err.Error(), "exact-count") {
		t.Fatalf("count form error = %v, want exact-count removal message", err)
	}
	for _, input := range []string{"", "goal", "forever", "3abc task", "-2 task", "unknown task"} {
		if _, err := Parse(input); err == nil {
			t.Errorf("Parse(%q) accepted invalid input", input)
		}
	}
}

// A claim never closes the loop; only a confirmed verdict does.
func TestLoopDoneClaimsAndApproveConfirms(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("goal complete the task"); err != nil {
		t.Fatal(err)
	}
	claim := controller.HandleDone(map[string]any{"reason": "all checks pass"})
	if claim.Error || !claim.State.Active || !claim.State.Evaluating || claim.State.ClaimReason != "all checks pass" {
		t.Fatalf("claim result = %+v", claim)
	}
	if !strings.Contains(claim.Text, "Claim recorded") {
		t.Fatalf("claim text = %q", claim.Text)
	}
	stale := controller.HandleDone(map[string]any{"reason": "second claim"})
	if !stale.Error || !strings.Contains(stale.Text, "already being checked") {
		t.Fatalf("stale claim = %+v", stale)
	}
	rejected := controller.HandleApprove(map[string]any{"verdict": "not_reached", "reason": "tests fail"})
	if rejected.Error || !rejected.State.Active || rejected.State.Evaluating {
		t.Fatalf("rejected verdict = %+v", rejected)
	}
	if !strings.Contains(rejected.Text, "loop stays open") {
		t.Fatalf("rejected text = %q", rejected.Text)
	}
	if result := controller.HandleDone(map[string]any{"reason": "fixed the tests"}); result.Error || !result.State.Evaluating {
		t.Fatalf("second claim = %+v", result)
	}
	approved := controller.HandleApprove(map[string]any{"verdict": "reached", "reason": "tests and lint pass"})
	if approved.Error || approved.State.Active || !approved.State.Done || approved.State.Evaluating {
		t.Fatalf("approved verdict = %+v", approved)
	}
	if approved.State.ReasonDone != "tests and lint pass" {
		t.Fatalf("reason done = %q", approved.State.ReasonDone)
	}
	if !strings.Contains(approved.Text, "Loop complete after 1 iteration(s)") {
		t.Fatalf("approved text = %q", approved.Text)
	}
}

func TestLoopDoneRequiresReasonAndApproveRequiresClaim(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("goal write docs"); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{}, {"reason": "   "}, {"summary": "legacy"}, {"status": "next", "summary": "legacy next"}} {
		result := controller.HandleDone(args)
		if !result.Error || result.State.Evaluating || !strings.Contains(result.Text, "needs a reason") {
			t.Fatalf("HandleDone(%v) = %+v", args, result)
		}
	}
	result := controller.HandleApprove(map[string]any{"verdict": "reached", "reason": "done"})
	if !result.Error || !strings.Contains(result.Text, "only available") {
		t.Fatalf("verdict without claim = %+v", result)
	}
	if state := controller.Snapshot(); !state.Active || state.Done || state.Evaluating {
		t.Fatalf("state after rejected calls = %+v", state)
	}
	// A legacy { status: "done", summary } claim folds into the current shape.
	legacy := controller.HandleDone(map[string]any{"status": "done", "summary": "legacy done"})
	if legacy.Error || !legacy.State.Evaluating || legacy.State.ClaimReason != "legacy done" {
		t.Fatalf("legacy done claim = %+v", legacy)
	}
}

// The turn boundary advances the loop; the model cannot advance it.
func TestTurnEndAdvancesLoopAndCheckResumes(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("goal iterate"); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 2; want++ {
		messageType, prompt, ok := controller.ContinuePrompt()
		if !ok || messageType != IterationMessageType {
			t.Fatalf("continue = %q %v", messageType, ok)
		}
		state := controller.Snapshot()
		if state.CurrentStep != want || !strings.Contains(prompt, "Iteration "+string(rune('0'+want+1))) {
			t.Fatalf("advance %d state = %+v prompt = %q", want, state, prompt)
		}
	}
	if result := controller.HandleDone(map[string]any{"reason": "looks done"}); result.Error {
		t.Fatal(result.Text)
	}
	messageType, prompt, ok := controller.ContinuePrompt()
	if !ok || messageType != ResumeMessageType {
		t.Fatalf("check-turn continue = %q %v", messageType, ok)
	}
	state := controller.Snapshot()
	if !state.Active || state.Evaluating || state.CurrentStep != 2 {
		t.Fatalf("resume state = %+v", state)
	}
	if !strings.Contains(prompt, "not confirmed") || !strings.Contains(prompt, "looks done") {
		t.Fatalf("resume prompt = %q", prompt)
	}
	if _, _, ok := controller.ContinuePrompt(); !ok {
		t.Fatal("resumed loop stopped advancing")
	}
}

func TestEvaluationAndResumePromptsFollowClaim(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("goal verify"); err != nil {
		t.Fatal(err)
	}
	if _, ok := controller.EvaluationPrompt(); ok {
		t.Fatal("evaluation prompt appeared without a claim")
	}
	controller.HandleDone(map[string]any{"reason": "seems done"})
	prompt, ok := controller.EvaluationPrompt()
	if !ok || !strings.Contains(prompt, "Confirmation check") || !strings.Contains(prompt, "seems done") {
		t.Fatalf("evaluation prompt = %q %v", prompt, ok)
	}
	if _, ok := controller.ResumePrompt(); ok {
		t.Fatal("resume prompt appeared during a pending check")
	}
	controller.HandleApprove(map[string]any{"verdict": "not_reached", "reason": "missing tests"})
	resume, ok := controller.ResumePrompt()
	if !ok || !strings.Contains(resume, "not confirmed") {
		t.Fatalf("resume prompt = %q %v", resume, ok)
	}
}

func TestForeverLoopHasNoToolAndCannotBeControlled(t *testing.T) {
	_, controller := newTestController(t)
	state, err := controller.Start("forever keep testing")
	if err != nil {
		t.Fatal(err)
	}
	if ToolDefinition(state) != nil {
		t.Fatal("forever loop exposed a loop tool")
	}
	addition := SystemPromptAddition(state)
	for _, tool := range []string{"loop_done", "loop_approve"} {
		if strings.Contains(addition, tool) {
			t.Fatalf("forever system prompt exposed %s: %q", tool, addition)
		}
	}
	for _, result := range []ControlResult{
		controller.HandleDone(map[string]any{"reason": "stale"}),
		controller.HandleApprove(map[string]any{"verdict": "reached"}),
	} {
		if !result.Error || !result.State.Active || !strings.Contains(result.Text, "unavailable") {
			t.Fatalf("forever tool call = %+v", result)
		}
	}
	messageType, prompt, ok := controller.ContinuePrompt()
	if !ok || messageType != NudgeMessageType || !strings.Contains(prompt, "Only the user can stop") {
		t.Fatalf("forever continuation = %q %q %v", messageType, prompt, ok)
	}
}

func TestBuildPromptAndNudgePromptAllModes(t *testing.T) {
	foreverState := State{Active: true, Forever: true, Goal: "run test suite"}
	bp := BuildPrompt(foreverState)
	if !strings.Contains(bp, "Forever Loop") || !strings.Contains(bp, "run test suite") {
		t.Errorf("BuildPrompt forever = %q", bp)
	}
	if np := NudgePrompt(foreverState); !strings.Contains(np, "The forever loop remains active") {
		t.Errorf("NudgePrompt forever = %q", np)
	}

	goalState := State{Active: true, CurrentStep: 1, Goal: "fix bugs"}
	bp = BuildPrompt(goalState)
	if !strings.Contains(bp, "Iteration 2") || !strings.Contains(bp, "fix bugs") || !strings.Contains(bp, "loop_done") {
		t.Errorf("BuildPrompt goal = %q", bp)
	}
	if strings.Contains(bp, "loop_control") {
		t.Errorf("BuildPrompt goal still names loop_control: %q", bp)
	}
}

func TestSystemPromptAdditionAndToolDefinition(t *testing.T) {
	if got := SystemPromptAddition(State{Active: false}); got != "" {
		t.Errorf("SystemPromptAddition(inactive) = %q, want empty", got)
	}
	forever := State{Active: true, Forever: true, Goal: "monitor"}
	if got := SystemPromptAddition(forever); !strings.Contains(got, "Active Forever Loop") {
		t.Errorf("SystemPromptAddition(forever) = %q", got)
	}

	working := State{Active: true, CurrentStep: 0, Goal: "ship feature"}
	gotWorking := SystemPromptAddition(working)
	if !strings.Contains(gotWorking, "Iteration: 1") || !strings.Contains(gotWorking, "loop_done") {
		t.Errorf("SystemPromptAddition(working) = %q", gotWorking)
	}
	done := ToolDefinition(working)
	if done == nil || done.Name != DoneToolName || done.Schema == nil {
		t.Fatalf("working tool definition = %+v", done)
	}
	if required, _ := done.Schema["required"].([]string); len(required) != 1 || required[0] != "reason" {
		t.Errorf("loop_done required = %#v", done.Schema["required"])
	}

	checking := State{Active: true, CurrentStep: 2, Goal: "ship feature", Evaluating: true, ClaimReason: "shipped"}
	gotChecking := SystemPromptAddition(checking)
	if !strings.Contains(gotChecking, "Confirmation check") || !strings.Contains(gotChecking, "loop_approve") {
		t.Errorf("SystemPromptAddition(checking) = %q", gotChecking)
	}
	approve := ToolDefinition(checking)
	if approve == nil || approve.Name != ApproveToolName {
		t.Fatalf("checking tool definition = %+v", approve)
	}
	if required, _ := approve.Schema["required"].([]string); len(required) != 2 {
		t.Errorf("loop_approve required = %#v", approve.Schema["required"])
	}

	if ToolDefinition(State{Active: false, Goal: "x"}) != nil {
		t.Error("inactive loop exposed a tool")
	}
}

func TestLoopStateReconstructsFromActiveSessionBranch(t *testing.T) {
	store, controller := newTestController(t)
	if _, err := controller.Start("goal persist state"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := controller.ContinuePrompt(); !ok {
		t.Fatal("turn end did not advance")
	}
	controller.HandleDone(map[string]any{"reason": "persisted claim"})

	restored := NewController(store)
	if err := restored.Reconstruct(); err != nil {
		t.Fatal(err)
	}
	state := restored.Snapshot()
	if !state.Active || state.CurrentStep != 1 || state.Goal != "persist state" || !state.Evaluating || state.ClaimReason != "persisted claim" {
		t.Fatalf("restored state = %+v", state)
	}
	if _, stopped, err := restored.Stop("user stop"); err != nil || !stopped {
		t.Fatalf("stop returned stopped=%v err=%v", stopped, err)
	}
	restored = NewController(store)
	if err := restored.Reconstruct(); err != nil {
		t.Fatal(err)
	}
	if state := restored.Snapshot(); state.Active || !state.Done || state.ReasonDone != "user stop" || state.Evaluating {
		t.Fatalf("restored stopped state = %+v", state)
	}
}

func TestStatusLineAndSetStore(t *testing.T) {
	store1, controller := newTestController(t)
	if s := controller.StatusLine(); s != "" {
		t.Errorf("StatusLine(inactive) = %q, want empty", s)
	}
	if _, err := controller.Start("goal write tests"); err != nil {
		t.Fatal(err)
	}
	if status := controller.StatusLine(); !strings.Contains(status, "iter 1") || !strings.Contains(status, "write tests") {
		t.Errorf("StatusLine(goal) = %q", status)
	}
	controller.HandleDone(map[string]any{"reason": "claim"})
	if status := controller.StatusLine(); !strings.Contains(status, "evaluating") {
		t.Errorf("StatusLine(checking) = %q", status)
	}

	store2, err := session.New(session.OpenOptions{Cwd: t.TempDir(), AgentDir: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.SetStore(store2); err != nil {
		t.Fatal(err)
	}
	if s := controller.StatusLine(); s != "" {
		t.Errorf("StatusLine after SetStore to empty = %q, want empty", s)
	}
	if err := controller.SetStore(store1); err != nil {
		t.Fatal(err)
	}
	if s := controller.StatusLine(); !strings.Contains(s, "evaluating") {
		t.Errorf("StatusLine after SetStore back = %q, want evaluating", s)
	}
}

func TestControllerConcurrentUsage(t *testing.T) {
	_, controller := newTestController(t)
	if _, err := controller.Start("goal stress test"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				_ = controller.Snapshot()
				_ = controller.StatusLine()
				_, _ = controller.EvaluationPrompt()
				_, _ = controller.ResumePrompt()
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
		if err == nil && !validState(st) {
			t.Fatalf("Parse accepted invalid state: %+v for input %q", st, input)
		}
	})
}
