package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/tools"
)

// Tool batch execution, modeled on Pi's agent-loop executeToolCalls:
// preflight every call in assistant order, run eligible calls concurrently,
// and append tool results in assistant order after each settles.
// Reference: temp/pi-mono/packages/agent/src/agent-loop.ts (executeToolCallsParallel).
//
// Two psi deviations stay explicit:
//   - Internal control tools (loop_done, loop_approve) execute in call order relative to
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
	batchExternal
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
	webDisplay  bool
	plans       []*batchPlan
	completions chan batchCompletion
	// progress carries display-only web progress to the coordinator. Sends
	// are non-blocking; the coordinator alone emits the sink event.
	progress    chan event.WebProgress
	outstanding int
}

// onPrepareHook is an optional test hook called during preflight for builtin tools.
var onPrepareHook func(tools.Invocation)

// runBatch preflights calls in order, executes them concurrently, and settles
// results in order. The next model request waits behind this settlement
// barrier; the caller only proceeds when runBatch returns.
func (l *Loop) runBatch(ctx context.Context, calls []provider.Block, cfg Config, webCalls map[string]event.WebCall) error {
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

	b := &batch{
		loop: l, webDisplay: cfg.WebDisplay != nil,
		completions: make(chan batchCompletion, len(calls)),
		progress:    make(chan event.WebProgress, 8),
	}
	b.plans = make([]*batchPlan, len(calls))

	// Preflight phase: populate plans, prepare builtin tools, and emit ToolStart in call order.
	for i, tc := range calls {
		plan := &batchPlan{
			invocation: tools.Invocation{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments},
			summary:    tools.SummarizeArguments(tc.Name, tc.Arguments, l.store.Cwd(), home),
		}
		if call, ok := webCalls[tc.ID]; ok {
			plan.summary.Web = &event.WebDisplay{Call: call}
		}
		if handler, ok := cfg.InternalTools[tc.Name]; ok && enabled[tc.Name] {
			plan.kind = batchInternal
			plan.internal = handler
		} else if _, internal := cfg.InternalTools[tc.Name]; !internal && enabled[tc.Name] {
			if lookup := cfg.ConcurrentTools; lookup != nil {
				if handler, ok := lookup(tc.Name); ok {
					plan.kind = batchExternal
					plan.internal = handler
				}
			}
			if plan.kind != batchExternal {
				plan.kind = batchBuiltin
			}
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
		case batchExternal:
			go b.runBuiltinCall(batchCtx, plan, func(ctx context.Context) tools.Result {
				return plan.internal(ctx, plan.invocation)
			}, i)
			b.outstanding++
		}
	}

	err := b.coordinate(batchCtx, stopBatch)
	if err != nil {
		return err
	}
	// Post-processing may append model-visible messages. All tool results
	// must precede those messages, even when the first call in a parallel
	// batch completes before its siblings.
	if cfg.AfterToolCall != nil {
		for _, plan := range b.plans {
			if !plan.notStarted {
				if err := cfg.AfterToolCall(batchCtx, plan.invocation, plan.result); err != nil {
					return fmt.Errorf("tool post-processing: %w", err)
				}
			}
		}
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
		// Prepared built-in runners release mutation slots acquired during
		// preparation, so they still run for an already-cancelled batch.
		// External handlers never started; invoking one here would perform
		// an unadmitted side effect.
		if runner != nil && plan.kind == batchBuiltin {
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
	res := plan.internal(ctx, invocationWithProgress(b.webDisplay, plan.invocation, b.progress, ctx))
	b.completions <- batchCompletion{index: index, started: true, result: res}
}

// invocationWithProgress adds the optional display-only progress callback for
// a web call. Interactive hosts set WebDisplay; RPC and other internal tools
// never receive the callback.
func invocationWithProgress(webDisplay bool, inv tools.Invocation, progress chan<- event.WebProgress, ctx context.Context) tools.Invocation {
	if !webDisplay || !event.IsWebTool(inv.Name) {
		return inv
	}
	inv.Report = func(update event.WebProgress) {
		if ctx.Err() != nil {
			return
		}
		update.ToolCallID = inv.ID
		// Drop excess progress instead of blocking a worker or the terminal.
		select {
		case progress <- update:
		default:
		}
	}
	return inv
}

func notStartedResult() tools.Result {
	return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: notStartedText}}}
}

// coordinate receives worker completions, settles contiguous results in call
// order, and joins every dispatched worker before returning. Display-only web
// progress is emitted only by this coordinator and never delays a completion.
func (b *batch) coordinate(ctx context.Context, stopBatch context.CancelFunc) error {
	next := 0
	for next < len(b.plans) {
		// Settle every result that is complete and whose predecessors settled.
		for next < len(b.plans) && b.plans[next].completed {
			if err := b.settleBatchCall(ctx, b.plans[next]); err != nil {
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
		// A ready completion always wins over queued progress.
		select {
		case completion := <-b.completions:
			b.apply(completion)
			b.outstanding--
			continue
		default:
		}
		select {
		case completion := <-b.completions:
			b.apply(completion)
			b.outstanding--
		case update := <-b.progress:
			// Recheck completions before emitting, so a finished call does not
			// show progress after settlement.
			select {
			case completion := <-b.completions:
				b.apply(completion)
				b.outstanding--
			default:
				b.loop.sink(event.Event{Type: event.WebProgressUpdate, ToolCallID: update.ToolCallID, State: "running", Progress: &update})
			}
		}
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
// row. Not-started calls record psiOutcome
// "not_started"; started calls keep their truthful result and cancellation
// state.
func (b *batch) settleBatchCall(ctx context.Context, plan *batchPlan) error {
	l := b.loop
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
	var webMeta *event.WebMeta
	if b.webDisplay && plan.result.Web != nil {
		webMeta = &event.WebMeta{CallID: tc.ID, Result: plan.result.Web}
	}
	if _, err := l.store.AppendMessageDisplay(msg, webMeta); err != nil {
		return fmt.Errorf("storage failure, stopping: %w", err)
	}
	summary := plan.summary
	if plan.result.Web != nil {
		web := event.WebDisplay{Result: plan.result.Web}
		if plan.summary.Web != nil {
			web.Call = plan.summary.Web.Call
		}
		summary.Web = &web
	}
	tools.SetExitCode(&summary, plan.result.Details)
	l.sink(event.Event{Type: event.ToolEnd, ToolName: tc.Name, ToolCallID: tc.ID, State: state, Summary: summary})
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
