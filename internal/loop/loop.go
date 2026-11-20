// Package loop ports the MIT-licensed pi-loop-minimal behavior into psi's
// agent and session lifecycle. See LICENSE for the upstream notice.
//
// Two loop forms remain:
//
//	/loop goal <description>  — iterate until the goal is confirmed
//	/loop forever <task>      — iterate until the user stops it
//
// A goal loop runs in two phases. In the working phase only loop_done is
// exposed; the model can claim completion, never advance the loop itself. The
// turn boundary advances the loop, so a turn that ends starts the next
// iteration. A claim starts a checking phase where only loop_approve is
// exposed; "reached" closes the loop and anything else resumes the work.
package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
)

const (
	DoneToolName          = "loop_done"
	ApproveToolName       = "loop_approve"
	StateEntryType        = "psi.loop.state"
	NudgeMessageType      = "psi.loop.nudge"
	IterationMessageType  = "psi.loop.iteration"
	EvaluationMessageType = "psi.loop.evaluation"
	ResumeMessageType     = "psi.loop.resume"
)

// State is durable loop control state. A goal loop runs until a completion
// claim is confirmed; a forever loop runs until the user stops it.
type State struct {
	Active      bool   `json:"active"`
	CurrentStep int    `json:"currentStep"`
	Goal        string `json:"goal"`
	Done        bool   `json:"done"`
	ReasonDone  string `json:"reasonDone"`
	Forever     bool   `json:"forever"`
	// Evaluating is set while a completion claim is being checked. The claim
	// only closes the loop once loop_approve confirms it.
	Evaluating  bool   `json:"evaluating"`
	ClaimReason string `json:"claimReason"`
}

func EmptyState() State { return State{} }

func Parse(args string) (State, error) {
	parts := strings.Fields(strings.TrimSpace(args))
	if len(parts) == 0 {
		return State{}, errors.New("usage: /loop goal <description> | /loop forever <task>")
	}
	state := State{Active: true}
	switch parts[0] {
	case "goal":
		state.Goal = strings.Join(parts[1:], " ")
		if state.Goal == "" {
			return State{}, errors.New("provide a goal description")
		}
	case "forever":
		state.Goal = strings.Join(parts[1:], " ")
		if state.Goal == "" {
			return State{}, errors.New("provide a task description")
		}
		state.Forever = true
	default:
		if decimal(parts[0]) {
			return State{}, errors.New("exact-count loops were removed; use /loop goal <description> or /loop forever <task>")
		}
		return State{}, fmt.Errorf("unknown loop mode %q; use goal or forever", parts[0])
	}
	return state, nil
}

func decimal(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// BuildPrompt is the iteration prompt for the working phase.
func BuildPrompt(state State) string {
	if state.Forever {
		return strings.Join([]string{
			"## Forever Loop",
			"Task: " + state.Goal,
			"This work session remains active until the user stops it.",
			"Continue working on the task. Use the available tools when they help.",
		}, "\n")
	}
	return strings.Join([]string{
		fmt.Sprintf("## Loop — Iteration %d", state.CurrentStep+1),
		"Goal: " + state.Goal,
		"Work toward the goal.",
		"Each iteration ends when you end your turn — the loop continues automatically.",
		"When the goal is fully met, call loop_done and give the reason why.",
	}, "\n")
}

// BuildEvaluationPrompt is the checking-phase prompt: a claim was made and it
// has to be judged against the goal itself.
func BuildEvaluationPrompt(state State) string {
	return strings.Join([]string{
		"## Loop — Confirmation check",
		"Goal: " + state.Goal,
		"A claim was made that the goal is met: " + state.ClaimReason,
		"",
		"Has the goal actually been reached? Judge it against the goal itself, not against the claim.",
		"",
		`- Call loop_approve with verdict "reached" and your reason if the goal is genuinely done.`,
		`- Call loop_approve with verdict "not_reached" and what is still missing if it is not.`,
	}, "\n")
}

// BuildResumePrompt keeps the loop open after an unconfirmed claim.
func BuildResumePrompt(state State, why string) string {
	lines := []string{
		fmt.Sprintf("## Loop — Iteration %d (not confirmed)", state.CurrentStep+1),
		"Goal: " + state.Goal,
		"The loop is still open: " + why,
	}
	if state.ClaimReason != "" {
		lines = append(lines, "The claim was: "+state.ClaimReason)
	}
	lines = append(lines, "Keep working on the goal. If part of it looks unreachable, do what is achievable and keep going — the loop only closes when a claim is confirmed.")
	return strings.Join(lines, "\n")
}

// NudgePrompt continues a forever loop. Forever loops expose no loop tool and
// have no iteration counter.
func NudgePrompt(state State) string {
	return strings.Join([]string{
		"The forever loop remains active.",
		"Continue working on: " + state.Goal,
		"Only the user can stop this work session.",
	}, "\n")
}

func SystemPromptAddition(state State) string {
	if !state.Active {
		return ""
	}
	if state.Forever {
		return strings.Join([]string{"", "", "## Active Forever Loop", "Task: " + state.Goal, "Continue working until the user stops this work session."}, "\n")
	}
	if state.Evaluating {
		return strings.Join([]string{
			"", "", "## Active Loop — Confirmation check",
			"Goal: " + state.Goal,
			"A completion claim is being checked. Call `loop_approve` with your verdict.",
		}, "\n")
	}
	return strings.Join([]string{
		"", "", "## Active Loop",
		"Goal: " + state.Goal,
		"Iteration: " + strconv.Itoa(state.CurrentStep+1),
		"The loop advances automatically when you end your turn.",
		"Call `loop_done` only when the goal is fully met — a claim is confirmed in a follow-up check before the loop closes.",
	}, "\n")
}

// ToolDefinition returns the one tool the current phase exposes. The model
// never sees both tools at once, and a forever loop exposes neither.
func ToolDefinition(state State) *provider.ToolDef {
	if !state.Active || state.Forever {
		return nil
	}
	if state.Evaluating {
		def := approveToolDefinition()
		return &def
	}
	def := doneToolDefinition()
	return &def
}

// IsTool reports whether name is one of the loop tools.
func IsTool(name string) bool {
	return name == DoneToolName || name == ApproveToolName
}

func doneToolDefinition() provider.ToolDef {
	return provider.ToolDef{
		Name: DoneToolName,
		Description: "Claim that the goal is fully met. Call this only when it is. " +
			"The claim is confirmed in a follow-up check before the loop closes. " +
			"reason: why the goal is met.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{"type": "string", "description": "Why the goal is met (the claim to be checked)"},
			},
			"required": []string{"reason"},
		},
	}
}

func approveToolDefinition() provider.ToolDef {
	return provider.ToolDef{
		Name: ApproveToolName,
		Description: `Answer the loop's confirmation check: has the goal actually been reached? ` +
			`verdict "reached" closes the loop; verdict "not_reached" leaves it open so the work resumes. ` +
			"reason: your justification.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{"type": "string", "enum": []string{"reached", "not_reached"}},
				"reason":  map[string]any{"type": "string", "description": "Why the goal is (or is not) reached"},
			},
			"required": []string{"verdict", "reason"},
		},
	}
}

type ControlResult struct {
	Text  string
	State State
	Error bool
}

type Controller struct {
	mu    sync.Mutex
	store *session.Store
	state State
}

func NewController(store *session.Store) *Controller {
	return &Controller{store: store, state: EmptyState()}
}

func (c *Controller) SetStore(store *session.Store) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store, c.state = store, EmptyState()
	return c.reconstructLocked()
}

func (c *Controller) Reconstruct() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reconstructLocked()
}

func (c *Controller) reconstructLocked() error {
	c.state = EmptyState()
	if c.store == nil {
		return nil
	}
	path, err := c.store.ActiveCustomEntries(StateEntryType)
	if err != nil {
		return err
	}
	for _, entry := range path {
		if entry.Kind != "custom" || entry.Custom == nil || entry.Custom.CustomType != StateEntryType {
			continue
		}
		var state State
		if err := json.Unmarshal(entry.Custom.Data, &state); err != nil || !validState(state) {
			continue
		}
		c.state = state
	}
	// Like the reference extension, a pending confirmation is abandoned on
	// session reconstruction; it must not approve a stale claim after restart.
	c.state.Evaluating = false
	c.state.ClaimReason = ""
	return nil
}

func validState(state State) bool {
	if state.CurrentStep < 0 || strings.TrimSpace(state.Goal) == "" {
		return false
	}
	// A claim is only meaningful inside an active loop.
	if state.Evaluating && (!state.Active || state.Forever) {
		return false
	}
	return true
}

func (c *Controller) Snapshot() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Controller) Start(args string) (State, error) {
	state, err := Parse(args)
	if err != nil {
		return State{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Active {
		return State{}, errors.New("a loop is already active; use /loop-stop first")
	}
	if err := c.persistLocked(state); err != nil {
		return State{}, err
	}
	c.state = state
	return state, nil
}

func (c *Controller) Stop(reason string) (State, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Active {
		return c.state, false, nil
	}
	state := c.state
	state.Active, state.Done, state.ReasonDone = false, true, reason
	// A stop during a check abandons the claim rather than resurrecting it.
	state.Evaluating, state.ClaimReason = false, ""
	if err := c.persistLocked(state); err != nil {
		return c.state, false, err
	}
	c.state = state
	return state, true, nil
}

// HandleDone records a completion claim. It never closes the loop itself: the
// next turn checks the claim, so a stale or invalid call cannot end the loop.
func (c *Controller) HandleDone(args map[string]any) ControlResult {
	reason, _ := args["reason"].(string)
	// Older sessions stored { status: "done", summary, reason } calls. Fold
	// them into the current shape; a legacy "next" call has no equivalent any
	// more, so it folds to an empty reason and is rejected below.
	if reason == "" {
		if status, _ := args["status"].(string); status == "done" {
			reason, _ = args["summary"].(string)
		}
	}
	reason = strings.TrimSpace(reason)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Active {
		return ControlResult{Text: "No active loop. Start one with /loop.", State: c.state, Error: true}
	}
	if c.state.Forever {
		return ControlResult{Text: "loop_done is unavailable during a forever loop.", State: c.state, Error: true}
	}
	if c.state.Evaluating {
		return ControlResult{Text: "A completion claim is already being checked. Wait for its verdict.", State: c.state, Error: true}
	}
	if reason == "" {
		return ControlResult{Text: "loop_done needs a reason: state why the goal is met. The loop is still active.", State: c.state, Error: true}
	}
	state := c.state
	state.Evaluating, state.ClaimReason = true, reason
	if err := c.persistLocked(state); err != nil {
		return ControlResult{Text: "Could not save loop state: " + err.Error(), State: c.state, Error: true}
	}
	c.state = state
	return ControlResult{Text: "Claim recorded — the goal is confirmed in the next turn before the loop closes.", State: state}
}

// HandleApprove records the checking turn's verdict. "reached" closes the
// loop; anything else leaves it open so the work resumes.
func (c *Controller) HandleApprove(args map[string]any) ControlResult {
	verdict, _ := args["verdict"].(string)
	reason, _ := args["reason"].(string)
	reason = strings.TrimSpace(reason)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Active {
		return ControlResult{Text: "No active loop. Start one with /loop.", State: c.state, Error: true}
	}
	if c.state.Forever {
		return ControlResult{Text: "loop_approve is unavailable during a forever loop.", State: c.state, Error: true}
	}
	if !c.state.Evaluating {
		return ControlResult{Text: "loop_approve is only available while a completion claim is being checked.", State: c.state, Error: true}
	}
	state := c.state
	if verdict == "reached" {
		state.Active, state.Done, state.Evaluating = false, true, false
		state.ReasonDone = reason
		if state.ReasonDone == "" {
			state.ReasonDone = state.ClaimReason
		}
		if state.ReasonDone == "" {
			state.ReasonDone = "Goal met"
		}
		state.ClaimReason = ""
		if err := c.persistLocked(state); err != nil {
			return ControlResult{Text: "Could not save loop state: " + err.Error(), State: c.state, Error: true}
		}
		c.state = state
		return ControlResult{Text: fmt.Sprintf("✓ Loop complete after %d iteration(s). Reason: %s", state.CurrentStep+1, state.ReasonDone), State: state}
	}
	state.Evaluating = false
	if err := c.persistLocked(state); err != nil {
		return ControlResult{Text: "Could not save loop state: " + err.Error(), State: c.state, Error: true}
	}
	c.state = state
	return ControlResult{Text: "Not confirmed. The loop stays open — keep working on the goal.", State: state}
}

// ContinuePrompt advances the loop when a turn ends and returns the message
// to append. The controller owns iteration boundaries: the model cannot
// advance the loop itself.
func (c *Controller) ContinuePrompt() (messageType, content string, ok bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Active {
		return "", "", false, nil
	}
	if c.state.Forever {
		return NudgeMessageType, NudgePrompt(c.state), true, nil
	}
	state := c.state
	if state.Evaluating {
		// A check turn ended without a verdict; resume rather than accepting it.
		state.Evaluating = false
		messageType = ResumeMessageType
		content = BuildResumePrompt(state, "the claim was not confirmed")
	} else {
		state.CurrentStep++
		messageType = IterationMessageType
		content = BuildPrompt(state)
	}
	// Never advance in memory when the durable state could not be saved.
	if err := c.persistLocked(state); err != nil {
		return "", "", false, err
	}
	c.state = state
	return messageType, content, true, nil
}

// EvaluationPrompt is appended after a recorded claim so the same run checks
// it before the loop can close.
func (c *Controller) EvaluationPrompt() (string, bool) {
	state := c.Snapshot()
	if !state.Active || state.Forever || !state.Evaluating {
		return "", false
	}
	return BuildEvaluationPrompt(state), true
}

// ResumePrompt is appended after a rejected verdict so the work continues in
// the same run.
func (c *Controller) ResumePrompt(reason string) (string, bool) {
	state := c.Snapshot()
	if !state.Active || state.Forever || state.Evaluating {
		return "", false
	}
	if strings.TrimSpace(reason) == "" {
		reason = "the goal was not confirmed"
	}
	return BuildResumePrompt(state, strings.TrimSpace(reason)), true
}

func (c *Controller) StatusLine() string {
	state := c.Snapshot()
	if !state.Active {
		return ""
	}
	label := fmt.Sprintf("iter %d", state.CurrentStep+1)
	if state.Forever {
		label = "forever"
	} else if state.Evaluating {
		label = "evaluating"
	}
	return fmt.Sprintf("loop · %s · %s · Ctrl+X to stop", label, state.Goal)
}

func (c *Controller) persistLocked(state State) error {
	if c.store == nil {
		return errors.New("loop session is unavailable")
	}
	if err := c.store.WriteError(); err != nil {
		return err
	}
	_, err := c.store.AppendCustomEntry(StateEntryType, state)
	return err
}
