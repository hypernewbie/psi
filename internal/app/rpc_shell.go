package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/hypernewbie/psi/internal/tools"
)

// rpcShellWorker owns one direct shell request. The server sequence, not the
// client-provided ID, identifies the worker; duplicate client IDs never share
// cancellation or settlement.
type rpcShellWorker struct {
	id     *string
	seq    uint64
	cancel context.CancelFunc
	done   chan struct{}
}

// rpcShellCompletion is one finished command. Its response may be sent before
// the model boundary; its durable append decision is published under the shell
// state lock and the worker exits only after that decision.
type rpcShellCompletion struct {
	worker       *rpcShellWorker
	command      string
	exclude      bool
	result       shellResult
	appended     chan struct{}
	appendErr    error
	responseSent bool
}

type rpcBashResult struct {
	Output         string `json:"output"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	Cancelled      bool   `json:"cancelled"`
	Truncated      bool   `json:"truncated"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
}

// rpcShellRunFunc is the per-runner shell seam. Tests replace it before
// exercising the command; production leaves it nil and uses BashTool.Run.
type rpcShellRunFunc func(context.Context, *tools.BashTool, string, func([]byte)) tools.BashRunResult

func rpcRequiredString(raw json.RawMessage, name string) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("%s is required", name)
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return *value, nil
}

func rpcOptionalBool(raw json.RawMessage, name string) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	var value *bool
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return *value, nil
}

func (s *rpcRunner) shellActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.shells) > 0
}

func (s *rpcRunner) shellCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.shells)
}

func (s *rpcRunner) cancelShells() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.shells))
	for _, worker := range s.shells {
		cancels = append(cancels, worker.cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *rpcRunner) abortBash(cmd rpcCommand) *rpcResponse {
	s.cancelShells()
	out := rpcOK(cmd.ID, "abort_bash", nil)
	return &out
}

func (s *rpcRunner) bash(cmd rpcCommand) *rpcResponse {
	command, err := rpcRequiredString(cmd.Command, "command")
	if err != nil {
		out := rpcErr(cmd.ID, "bash", err.Error())
		return &out
	}
	exclude, err := rpcOptionalBool(cmd.ExcludeFromContext, "excludeFromContext")
	if err != nil {
		out := rpcErr(cmd.ID, "bash", err.Error())
		return &out
	}
	r := s.r
	if writeErr := r.store.WriteError(); writeErr != nil {
		out := rpcErr(cmd.ID, "bash", writeErr.Error())
		return &out
	}
	// The shell state lock makes admission, the model-boundary append, and a
	// new prompt's optimistic user message one ordered decision.
	s.shellStateMu.Lock()
	s.mu.Lock()
	select {
	case <-s.shutdown:
		s.mu.Unlock()
		s.shellStateMu.Unlock()
		out := rpcErr(cmd.ID, "bash", "Server is shutting down.")
		return &out
	default:
	}
	if s.reservation != nil || len(s.shells) >= maxShellSlots {
		s.mu.Unlock()
		s.shellStateMu.Unlock()
		out := rpcErr(cmd.ID, "bash", "Cannot start another shell command while compaction runs or the shell slot limit is reached.")
		return &out
	}
	ctx, cancel := context.WithCancel(context.Background())
	worker := &rpcShellWorker{id: cmd.ID, seq: s.shellSeq, cancel: cancel, done: make(chan struct{})}
	s.shellSeq++
	if s.shells == nil {
		s.shells = make(map[uint64]*rpcShellWorker)
	}
	s.shells[worker.seq] = worker
	s.mu.Unlock()
	s.shellStateMu.Unlock()

	tool := &tools.BashTool{Cwd: r.store.Cwd(), ShellPath: r.settings.ShellPath, ShellPrefix: r.settings.ShellPrefix}
	go s.runBash(ctx, worker, tool, command, exclude)
	return nil
}

func (s *rpcRunner) runBash(ctx context.Context, worker *rpcShellWorker, tool *tools.BashTool, command string, exclude bool) {
	completion := &rpcShellCompletion{
		worker: worker, command: command, exclude: exclude, appended: make(chan struct{}),
	}
	defer close(worker.done)
	defer worker.cancel()
	var collector shellCollector
	collector.observe = func(delta string) {
		if delta == "" {
			return
		}
		update := map[string]any{"type": "bash_execution_update", "delta": delta}
		if worker.id != nil {
			update["id"] = *worker.id
		}
		s.emit(update)
	}
	var runResult tools.BashRunResult
	if s.shellRun != nil {
		runResult = s.shellRun(ctx, tool, command, collector.Feed)
	} else {
		runResult = tool.Run(ctx, command, 0, collector.Feed)
	}
	diagnostic := ""
	if runResult.InfraErr != nil {
		diagnostic = "shell: " + runResult.InfraErr.Error()
	}
	output, truncated := collector.finish(diagnostic)
	fullPath := ""
	if runResult.FullOutputPath != "" {
		if _, err := os.Stat(runResult.FullOutputPath); err == nil {
			fullPath = runResult.FullOutputPath
		}
	}
	completion.result = shellResult{
		output: output, exitCode: runResult.ExitCode, cancelled: runResult.Cancelled,
		truncated: runResult.Truncated || truncated, fullOutputPath: fullPath,
		infraErr: runResult.InfraErr,
	}

	s.shellStateMu.Lock()
	s.mu.Lock()
	modelOwned := s.running || s.reservation != nil
	s.mu.Unlock()
	// responseSent is published under shellStateMu together with the queue
	// entry and never written after. A boundary flush on the reader goroutine
	// then reads it through the same lock order without a data race.
	completion.responseSent = modelOwned
	s.shellQueue = append(s.shellQueue, completion)
	var failures []*rpcShellCompletion
	if !modelOwned {
		failures = s.drainShellQueueLocked()
	}
	s.shellStateMu.Unlock()

	// An execution result is not a durability acknowledgement. A result that
	// completed inside a model run answers now and waits for the append
	// decision at the model boundary.
	s.emitBashExecution(completion)
	if modelOwned {
		<-completion.appended
	}
	for _, failed := range failures {
		if failed == completion {
			continue
		}
		s.emitBashAppendFailure(failed)
	}
	s.retireBash(worker)
}

// drainShellQueueLocked appends every published completion in FIFO order. The
// caller holds shellStateMu, so a prompt admission cannot slip its user message
// ahead of a ready shell result.
func (s *rpcRunner) drainShellQueueLocked() []*rpcShellCompletion {
	var failures []*rpcShellCompletion
	for _, completion := range s.shellQueue {
		if _, err := s.r.store.AppendMessage(bashExecutionMessage(completion.command, completion.result, completion.exclude)); err != nil {
			completion.appendErr = err
			failures = append(failures, completion)
		}
		close(completion.appended)
	}
	s.shellQueue = nil
	return failures
}

func (s *rpcRunner) flushShellQueue() {
	s.shellStateMu.Lock()
	s.mu.Lock()
	modelOwned := s.running || s.reservation != nil
	s.mu.Unlock()
	var failures []*rpcShellCompletion
	if !modelOwned {
		failures = s.drainShellQueueLocked()
	}
	s.shellStateMu.Unlock()
	for _, failed := range failures {
		s.emitBashAppendFailure(failed)
	}
}

func (s *rpcRunner) emitBashExecution(completion *rpcShellCompletion) {
	if !completion.responseSent && completion.appendErr != nil {
		s.emit(rpcErr(completion.worker.id, "bash", "Shell output could not be saved: "+completion.appendErr.Error()))
		return
	}
	if completion.result.infraErr != nil {
		s.emit(rpcErr(completion.worker.id, "bash", completion.result.infraErr.Error()))
		return
	}
	s.emit(rpcOK(completion.worker.id, "bash", rpcBashResult{
		Output: completion.result.output, ExitCode: completion.result.exitCode,
		Cancelled: completion.result.cancelled, Truncated: completion.result.truncated,
		FullOutputPath: completion.result.fullOutputPath,
	}))
}

func (s *rpcRunner) emitBashAppendFailure(completion *rpcShellCompletion) {
	if completion.appendErr == nil {
		return
	}
	if completion.responseSent {
		frame := map[string]any{"type": "extension_error", "error": "Shell output could not be saved: " + completion.appendErr.Error()}
		if completion.worker.id != nil {
			frame["id"] = *completion.worker.id
		}
		s.emit(frame)
		return
	}
	s.emit(rpcErr(completion.worker.id, "bash", "Shell output could not be saved: "+completion.appendErr.Error()))
}

func (s *rpcRunner) retireBash(worker *rpcShellWorker) {
	s.mu.Lock()
	delete(s.shells, worker.seq)
	s.mu.Unlock()
}
