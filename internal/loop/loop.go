// Package loop ports the MIT-licensed pi-loop-minimal behavior into psi's
// agent and session lifecycle. See LICENSE for the upstream notice.
package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"psi/internal/provider"
	"psi/internal/session"
)

const (
	ControlToolName      = "loop_control"
	StateEntryType       = "psi.loop.state"
	NudgeMessageType     = "psi.loop.nudge"
	IterationMessageType = "psi.loop.iteration"
)

type Mode string

const (
	ModeGoal   Mode = "goal"
	ModePasses Mode = "passes"
)

// State is durable loop control state. MaxSteps is zero for an open-ended goal.
type State struct {
	Active      bool   `json:"active"`
	Mode        Mode   `json:"mode"`
	CurrentStep int    `json:"currentStep"`
	MaxSteps    int    `json:"maxSteps"`
	Goal        string `json:"goal"`
	Done        bool   `json:"done"`
	ReasonDone  string `json:"reasonDone"`
	Forever     bool   `json:"forever"`
}

func EmptyState() State { return State{Mode: ModeGoal} }

func Parse(args string) (State, error) {
	parts := strings.Fields(strings.TrimSpace(args))
	if len(parts) == 0 {
		return State{}, errors.New("usage: /loop goal <description> | /loop forever <task> | /loop <N> <task>")
	}
	state := State{Active: true, Mode: ModeGoal}
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
		if !decimal(parts[0]) {
			return State{}, fmt.Errorf("unknown loop mode %q; use goal, forever, or <N>", parts[0])
		}
		count, err := strconv.Atoi(parts[0])
		if err != nil || count < 1 {
			return State{}, errors.New("provide a valid number of passes (N >= 1)")
		}
		state.Mode, state.MaxSteps = ModePasses, count
		state.Goal = strings.Join(parts[1:], " ")
		if state.Goal == "" {
			return State{}, errors.New("provide a task description")
		}
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

func BuildPrompt(state State) string {
	if state.Forever {
		return strings.Join([]string{
			"## Forever Loop",
			"Task: " + state.Goal,
			"This work session remains active until the user stops it.",
			"Continue working on the task. Use the available tools when they help.",
		}, "\n")
	}
	if state.Mode == ModePasses {
		step, maxSteps := state.CurrentStep, state.MaxSteps
		instruction := "This is the first pass. Do an initial implementation/analysis."
		if step > 0 && step < maxSteps-1 {
			instruction = "This is a refinement pass. Review and improve on the previous pass."
		} else if step >= maxSteps-1 {
			instruction = `This is the **final pass**. Do a final polish, then call loop_control with status "done".`
		}
		return strings.Join([]string{
			fmt.Sprintf("## Loop — Pass %d of %d", step+1, maxSteps),
			"Task: " + state.Goal,
			instruction,
			`When this pass is complete, call loop_control with status "next" (or "done" on the final pass).`,
		}, "\n")
	}
	return strings.Join([]string{
		fmt.Sprintf("## Loop — Iteration %d", state.CurrentStep+1),
		"Goal: " + state.Goal,
		`Work toward the goal. When the goal is fully met, call loop_control with status "done" and explain why.`,
		`If more work is needed, call loop_control with status "next" describing what's left.`,
	}, "\n")
}

func NudgePrompt(state State) string {
	if state.Forever {
		return strings.Join([]string{
			"The forever loop remains active.",
			"Continue working on: " + state.Goal,
			"Only the user can stop this work session.",
		}, "\n")
	}
	label := fmt.Sprintf("iteration %d", state.CurrentStep+1)
	if state.Mode == ModePasses {
		label = fmt.Sprintf("pass %d/%d", state.CurrentStep+1, state.MaxSteps)
	}
	return strings.Join([]string{
		fmt.Sprintf("The loop is still active — you ended your turn before finishing %s.", label),
		"Continue working on: " + state.Goal,
		"Do not end your turn until this iteration is complete.",
	}, "\n")
}

func SystemPromptAddition(state State) string {
	if !state.Active {
		return ""
	}
	if state.Forever {
		return strings.Join([]string{"", "", "## Active Forever Loop", "Task: " + state.Goal, "Continue working until the user stops this work session."}, "\n")
	}
	step := strconv.Itoa(state.MaxSteps)
	if state.Mode == ModeGoal {
		step = "∞"
	}
	return strings.Join([]string{
		"", "", "## Active Loop",
		fmt.Sprintf("Mode: %s | Step: %d/%s", state.Mode, state.CurrentStep+1, step),
		"Goal: " + state.Goal,
		"You MUST call `loop_control` when you finish your work for this iteration.",
		`Use status "next" to advance or "done" when the goal is fully met.`,
	}, "\n")
}

func ToolDefinition() provider.ToolDef {
	return provider.ToolDef{
		Name: ControlToolName,
		Description: "Signal progress in an active goal or exact-count loop. Call this when you finish an iteration. " +
			`Use status "next" to advance or status "done" when complete. This tool is unavailable in forever loops.`,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status":  map[string]any{"type": "string", "enum": []string{"next", "done"}},
				"summary": map[string]any{"type": "string", "description": "Brief summary of this iteration"},
				"reason":  map[string]any{"type": "string", "description": "Why the goal is complete"},
			},
			"required": []string{"status", "summary"},
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
	path, err := c.store.ActivePath()
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
	return nil
}

func validState(state State) bool {
	if state.Mode != ModeGoal && state.Mode != ModePasses {
		return false
	}
	if state.CurrentStep < 0 || strings.TrimSpace(state.Goal) == "" {
		return false
	}
	if state.Mode == ModePasses && (state.MaxSteps < 1 || state.Forever) {
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
	if err := c.persistLocked(state); err != nil {
		return c.state, false, err
	}
	c.state = state
	return state, true, nil
}

func (c *Controller) HandleControl(args map[string]any) ControlResult {
	status, _ := args["status"].(string)
	summary, _ := args["summary"].(string)
	reason, _ := args["reason"].(string)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Active {
		return ControlResult{Text: "No active loop. Start one with /loop.", State: c.state, Error: true}
	}
	if c.state.Forever {
		return ControlResult{Text: "loop_control is unavailable during a forever loop.", State: c.state, Error: true}
	}
	if (status != "next" && status != "done") || summary == "" {
		return ControlResult{Text: `loop_control requires status "next" or "done" and a summary.`, State: c.state, Error: true}
	}
	state := c.state
	text := ""
	if status == "done" {
		state.Active, state.Done = false, true
		state.ReasonDone = reason
		if state.ReasonDone == "" {
			state.ReasonDone = summary
		}
		text = fmt.Sprintf("✓ Loop complete after %d iteration(s). Reason: %s", state.CurrentStep+1, state.ReasonDone)
	} else {
		state.CurrentStep++
		if state.Mode == ModePasses && state.CurrentStep >= state.MaxSteps {
			state.Done, state.Active = true, false
			state.ReasonDone = "Completed all passes"
			text = fmt.Sprintf("✓ Loop complete — all %d iterations done.", state.MaxSteps)
		} else if state.Mode == ModePasses {
			text = fmt.Sprintf("→ Advancing to pass %d. Summary: %s", state.CurrentStep+1, summary)
		} else {
			text = fmt.Sprintf("→ Advancing to iteration %d. Summary: %s", state.CurrentStep+1, summary)
		}
	}
	if err := c.persistLocked(state); err != nil {
		return ControlResult{Text: "Could not save loop state: " + err.Error(), State: c.state, Error: true}
	}
	c.state = state
	return ControlResult{Text: text, State: state}
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

func (c *Controller) ContinuePrompt() (string, bool) {
	state := c.Snapshot()
	if !state.Active {
		return "", false
	}
	return NudgePrompt(state), true
}

func (c *Controller) StatusLine() string {
	state := c.Snapshot()
	if !state.Active {
		return ""
	}
	label := fmt.Sprintf("iter %d", state.CurrentStep+1)
	if state.Mode == ModePasses {
		label = fmt.Sprintf("pass %d/%d", state.CurrentStep+1, state.MaxSteps)
	}
	if state.Forever {
		label = "forever"
	}
	return fmt.Sprintf("loop · %s · %s · Ctrl+X to stop", label, state.Goal)
}
