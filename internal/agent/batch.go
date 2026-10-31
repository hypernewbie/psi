package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

// Tool batch execution, modeled on Pi's agent-loop executeToolCalls:
// preflight every call in assistant order, run eligible calls concurrently,
// and append tool results in assistant order after each settles.
// Reference: temp/pi-mono/packages/agent/src/agent-loop.ts (executeToolCallsParallel).
//
// Two psi deviations stay explicit:
//   - Internal control tools (loop_control) execute in call order relative to
//     each other, because their state transitions and continuation messages
//     are order-sensitive. Unrelated calls still overlap.
//   - Workers never touch the session store or the terminal sink. Only the
//     coordinator appends results and emits events, so terminal success rows
//     commit after the corresponding result is durable, not when a worker
//     finishes.

const (
	notStartedText    = "Tool was not started because the run was cancelled."
	outcomeNotStarted = "not_started"
	outcomeCancelled  = "cancelled"
)

type batchKind int

const (
	batchInternal batchKind = iota
	batchBuiltin
	batchDisabled
)

type batchPlan struct {
	invocation tools.Invocation
	kind       batchKind
	internal   InternalToolHandler
	runner     func(context.Context) tools.Result
	summary    event.ToolSummary
	// notStarted marks a call the batch never admitted (psiOutcome
	// "not_started"); started calls keep truthful results.
	notStarted bool
	result     tools.Result
	completed  bool
}

type batchCompletion struct {
	index   int
	started bool
	result  tools.Result
}

type batch struct {
	loop        *Loop
	cfg         Config
	plans       []*batchPlan
	completions chan batchCompletion
	outstanding int
}

// onPrepareHook is an optional test hook called during preflight for builtin tools.
var onPrepareHook func(tools.Invocation)

// runBatch preflights calls in order, executes them concurrently, and settles
// results in order. The next model request waits behind this settlement
// barrier; the caller only proceeds when runBatch returns.
func (l *Loop) runBatch(ctx context.Context, calls []provider.Block, cfg Config) error {
	if len(calls) == 0 {
		return nil
	}
	enabled := map[string]bool{}
	for _, d := range cfg.Tools {
		enabled[d.Name] = true
	}
	home, _ := os.UserHomeDir()

	// The batch owns a cancellable child context: a storage failure during
	// settlement stops admission and execution of the remaining calls.
	batchCtx, stopBatch := context.WithCancel(ctx)
	defer stopBatch()

	b := &batch{loop: l, cfg: cfg, completions: make(chan batchCompletion, len(calls))}
	b.plans = make([]*batchPlan, len(calls))

	// Preflight phase: populate plans, prepare builtin tools, and emit ToolStart in call order.
	for i, tc := range calls {
		plan := &batchPlan{
			invocation: tools.Invocation{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments},
			summary:    tools.SummarizeArguments(tc.Name, tc.Arguments, l.store.Cwd(), home),
		}
		if handler, ok := cfg.InternalTools[tc.Name]; ok {
			plan.kind = batchInternal
			plan.internal = handler
		} else if enabled[tc.Name] {
			plan.kind = batchBuiltin
		} else {
			plan.kind = batchDisabled
			plan.result = tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: "Tool is not enabled in this session"}}}
			plan.completed = true
		}
		b.plans[i] = plan
		// Preflight emits every start in call order, like Pi.
		l.sink(event.Event{Type: event.ToolStart, ToolName: tc.Name, ToolCallID: tc.ID, State: "running", Summary: plan.summary})
		if plan.kind == batchBuiltin {
			if onPrepareHook != nil {
				onPrepareHook(plan.invocation)
			}
			plan.runner = b.loop.reg.Prepare(plan.invocation)
		}
	}

	// Dispatch phase: only after all calls in the batch have completed preflight
	// and emitted ToolStart, launch worker goroutines.
	// Internal control calls chain in call order among themselves.
	prevInternal := closedChain()
	for i, plan := range b.plans {
		switch plan.kind {
		case batchInternal:
			chain := make(chan struct{})
			go b.runInternalCall(batchCtx, plan, prevInternal, chain, i)
			prevInternal = chain
			b.outstanding++
		case batchBuiltin:
			go b.runBuiltinCall(batchCtx, plan, plan.runner, i)
			b.outstanding++
		}
	}

	err := b.coordinate(batchCtx, stopBatch)
	if err != nil {
		return err
	}
	// A cancelled run surfaces the cancellation to the caller, matching the
	// pre-parallel behavior of returning at the call boundary.
	return batchCtx.Err()
}

// closedChain returns an already-settled chain head.
func closedChain() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// runBuiltinCall executes one built-in tool call. The worker never touches
// the store or the sink; it reports through completions exactly once.
func (b *batch) runBuiltinCall(ctx context.Context, plan *batchPlan, runner func(context.Context) tools.Result, index int) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			if err, ok := r.(error); ok {
				msg = err.Error()
			}
			b.completions <- batchCompletion{
				index:   index,
				started: true,
				result:  tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: msg}}},
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		if runner != nil {
			_ = runner(ctx)
		}
		b.completions <- batchCompletion{index: index, started: false, result: notStartedResult()}
		return
	}
	res := runner(ctx)
	b.completions <- batchCompletion{index: index, started: true, result: res}
}

// runInternalCall runs an internal control tool after the previous internal
// call in the same batch settles, then opens the chain for its successor.
func (b *batch) runInternalCall(ctx context.Context, plan *batchPlan, wait, done chan struct{}, index int) {
	defer close(done)
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			if err, ok := r.(error); ok {
				msg = err.Error()
			}
			b.completions <- batchCompletion{
				index:   index,
				started: true,
				result:  tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: msg}}},
			}
		}
	}()
	<-wait
	if err := ctx.Err(); err != nil {
		b.completions <- batchCompletion{index: index, started: false, result: notStartedResult()}
		return
	}
	res := plan.internal(ctx, plan.invocation)
	b.completions <- batchCompletion{index: index, started: true, result: res}
}

func notStartedResult() tools.Result {
	return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: notStartedText}}}
}

// coordinate receives worker completions, settles contiguous results in call
// order, and joins every dispatched worker before returning.
func (b *batch) coordinate(ctx context.Context, stopBatch context.CancelFunc) error {
	next := 0
	for next < len(b.plans) {
		// Settle every result that is complete and whose predecessors settled.
		for next < len(b.plans) && b.plans[next].completed {
			if err := b.loop.settleBatchCall(ctx, b.plans[next], b.cfg); err != nil {
				// Storage failure: stop admission, join active workers, and
				// report. Completed mutations cannot be rolled back.
				stopBatch()
				b.drain()
				return err
			}
			next++
		}
		if next >= len(b.plans) {
			break
		}
		completion := <-b.completions
		b.apply(completion)
		b.outstanding--
	}
	return nil
}

func (b *batch) apply(completion batchCompletion) {
	plan := b.plans[completion.index]
	plan.result = completion.result
	plan.notStarted = !completion.started
	plan.completed = true
}

// drain joins every dispatched worker after the batch stopped admitting.
func (b *batch) drain() {
	for b.outstanding > 0 {
		b.apply(<-b.completions)
		b.outstanding--
	}
}

// settleBatchCall appends one tool result durably, then commits the terminal
// row and post-processing hooks. Not-started calls record psiOutcome
// "not_started"; started calls keep their truthful result and cancellation
// state.
func (l *Loop) settleBatchCall(ctx context.Context, plan *batchPlan, cfg Config) error {
	tc := plan.invocation
	state := "done"
	if plan.result.IsError {
		state = "failed"
	}
	if plan.notStarted {
		state = "not started"
	} else if ctx.Err() != nil && plan.result.IsError {
		state = "cancelled"
	}
	var content []session.UserContentBlock
	for _, block := range plan.result.Content {
		if block.Type == "image" {
			content = append(content, session.UserContentBlock{Type: "image", Image: &session.ImageContent{Type: "image", Data: block.Data, MimeType: block.MimeType}})
		} else {
			content = append(content, session.UserContentBlock{Type: "text", Text: &session.TextContent{Type: "text", Text: block.Text}})
		}
	}
	var details json.RawMessage
	if plan.notStarted {
		details = json.RawMessage(`{"psiOutcome":"` + outcomeNotStarted + `"}`)
	} else {
		var err error
		details, err = json.Marshal(plan.result.Details)
		if err != nil {
			return err
		}
		if state == "cancelled" {
			if details, err = mergeCancelledOutcome(details); err != nil {
				return err
			}
		}
	}
	msg := session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{Role: "toolResult", ToolCallID: tc.ID, ToolName: tc.Name, Content: content, Details: details, IsError: plan.result.IsError, Timestamp: time.Now().UnixMilli()}}
	if _, err := l.store.AppendMessage(msg); err != nil {
		return fmt.Errorf("storage failure, stopping: %w", err)
	}
	summary := plan.summary
	tools.SetExitCode(&summary, plan.result.Details)
	l.sink(event.Event{Type: event.ToolEnd, ToolName: tc.Name, ToolCallID: tc.ID, State: state, Summary: summary})
	if !plan.notStarted && cfg.AfterToolCall != nil {
		if err := cfg.AfterToolCall(ctx, tc, plan.result); err != nil {
			return fmt.Errorf("tool post-processing: %w", err)
		}
	}
	return nil
}

func mergeCancelledOutcome(details json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(details, &fields); err != nil {
		fields = map[string]json.RawMessage{"toolDetails": details}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	fields["psiOutcome"] = json.RawMessage(`"` + outcomeCancelled + `"`)
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return out, nil
}
