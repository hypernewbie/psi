package app

// Mid-run compact ownership for `psi --mode rpc`: a compact admitted while
// a run owns the loop cancels the run, waits for its done channel, and only
// then summarizes. Aborts route to the reservation; prompts refused with P1.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/provider"
	"psi/internal/session"
)

// rpcSlowCancelProvider holds the first request until release; once the run
// is cancelled it reports the cancellation and waits at settleGate before
// returning, so the test lands an abort deterministically inside the compact
// worker's wait for the outgoing run.
type rpcSlowCancelProvider struct {
	mu         sync.Mutex
	reqs       []provider.Request
	entered    chan struct{}
	cancelled  chan struct{}
	release    chan struct{}
	settleGate chan struct{}
	enterOnce  sync.Once
	cancelOnce sync.Once
}

func (g *rpcSlowCancelProvider) Send(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	g.mu.Lock()
	g.reqs = append(g.reqs, req)
	first := len(g.reqs) == 1
	g.mu.Unlock()
	if !first {
		return &provider.Response{Text: "slow answer", StopReason: "stop"}, nil
	}
	g.enterOnce.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return &provider.Response{Text: "slow answer", StopReason: "stop"}, nil
	case <-ctx.Done():
		g.cancelOnce.Do(func() { close(g.cancelled) })
		<-g.settleGate
		return nil, ctx.Err()
	}
}

func (g *rpcSlowCancelProvider) Requests() []provider.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]provider.Request, len(g.reqs))
	copy(out, g.reqs)
	return out
}

func (g *rpcSlowCancelProvider) CloseIdle() {}

// rpcSummaryGatedProvider answers summary requests itself after release and
// passes normal turns to the inner provider, so the test lands commands
// deterministically inside a summary.
type rpcSummaryGatedProvider struct {
	inner     provider.Provider
	mu        sync.Mutex
	summaries []provider.Request
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
}

func rpcIsSummary(req provider.Request) bool {
	return strings.Contains(req.System, "summarization")
}

func (g *rpcSummaryGatedProvider) Send(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	if !rpcIsSummary(req) {
		return g.inner.Send(ctx, req, onDelta)
	}
	g.mu.Lock()
	g.summaries = append(g.summaries, req)
	g.mu.Unlock()
	g.enterOnce.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return &provider.Response{Text: "compacted summary", StopReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *rpcSummaryGatedProvider) SummaryCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.summaries)
}

func (g *rpcSummaryGatedProvider) CloseIdle() { g.inner.CloseIdle() }

func rpcWaitCompactResponse(t *testing.T, log *rpcLog, id string) map[string]any {
	t.Helper()
	objs := log.waitFor(t, "compact "+id, func(obj map[string]any) bool {
		return obj["type"] == "response" && obj["id"] == id
	})
	resp := responseByID(objs, id)
	if resp == nil {
		t.Fatalf("no terminal compact response %q in %v", id, objs)
	}
	return resp
}

func rpcCountResponses(objs []map[string]any, id string) int {
	n := 0
	for _, obj := range objs {
		if obj["type"] == "response" && obj["id"] == id {
			n++
		}
	}
	return n
}

func rpcSettledFlags(objs []map[string]any) []bool {
	var out []bool
	for _, obj := range objs {
		if obj["type"] == "agent_settled" {
			aborted, _ := obj["aborted"].(bool)
			out = append(out, aborted)
		}
	}
	return out
}

// rpcTerminalGateWriter delivers a terminal frame but waits to return from
// Write. This lets the test look for stale ownership once a client can read it.
type rpcTerminalGateWriter struct {
	dst     *rpcLog
	match   string
	written chan struct{}
	release chan struct{}
}

func (w *rpcTerminalGateWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if bytes.Contains(p, []byte(w.match)) {
		close(w.written)
		<-w.release
	}
	return n, err
}

func TestRPCTerminalFrameOrdersOwnershipRelease(t *testing.T) {
	for _, command := range []string{"prompt", "compact"} {
		t.Run(command, func(t *testing.T) {
			prov := newRPCBlockingProvider()
			s, _, log := newRPCTestRunner(t, prov, nil)
			gate := &rpcTerminalGateWriter{dst: log, match: `"type":"agent_settled"`, written: make(chan struct{}), release: make(chan struct{})}
			if command == "compact" {
				gate.match = `"command":"compact"`
			}
			s.out = json.NewEncoder(gate)
			unblock := sync.OnceFunc(func() { close(gate.release) })
			defer unblock()

			var done <-chan struct{}
			if command == "compact" {
				// An empty session reaches the terminal response without a
				// provider call. Keep the reservation so we can wait on it.
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				res := &rpcCompactReservation{id: strptr("c1"), cfg: s.runConfig(), done: make(chan struct{}), cancel: cancel}
				s.reservation, s.compacting = res, true
				done = res.done
				go s.compactWorker(res, nil, ctx)
			} else {
				if resp := s.handle(rpcCommand{Type: "prompt", Message: "hello"}); resp == nil || !resp.Success {
					t.Fatalf("prompt: %+v", resp)
				}
				done = s.run.done
				close(prov.release)
			}
			select {
			case <-gate.written:
			case <-time.After(8 * time.Second):
				t.Fatal("worker did not write a terminal frame")
			}
			select {
			case <-done:
				t.Fatal("worker finished before its terminal write returned")
			default:
			}
			// Probe command admission without blocking the parked writer.
			if s.mu.TryLock() {
				busy := s.running || s.compacting
				s.mu.Unlock()
				if busy {
					t.Fatal("command admission can still observe stale ownership after the terminal frame")
				}
			}
			unblock()
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("worker did not release ownership")
			}
			state := s.getState(rpcCommand{Type: "get_state"}).Data.(map[string]any)
			if state["isStreaming"] != false || state["isCompacting"] != false {
				t.Fatalf("worker still busy after settlement: %v", state)
			}
		})
	}
}

// TestRPCCompactDuringRunCancelsAndSummarizes proves a compact admitted
// mid-run cancels the run, waits for it, then summarizes: the interrupted
// prompt settles aborted and is never replayed, prompts during the
// reservation fail with P1, and a second compact is refused.
func TestRPCCompactDuringRunCancelsAndSummarizes(t *testing.T) {
	gate := &rpcGatedProvider{entered: make(chan struct{}), release: make(chan struct{})}
	s, store, log := newRPCTestRunner(t, gate, nil, 64)
	big := strings.Repeat("context filler for the compaction budget. ", 60)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "old question"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role:    "assistant",
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: big}}},
	}}); err != nil {
		t.Fatal(err)
	}
	first := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "hello"})
	if first == nil || !first.Success {
		t.Fatalf("prompt: %+v", first)
	}
	rpcGateEntered(t, gate)
	if resp := s.handle(rpcCommand{ID: strptr("c1"), Type: "compact"}); resp != nil {
		t.Fatalf("compact answers from its worker, got %+v", resp)
	}
	// Reads stay usable and the reservation is visible while it is held.
	state := s.handle(rpcCommand{ID: strptr("g1"), Type: "get_state"})
	if state == nil || !state.Success || state.Data.(map[string]any)["isCompacting"] != true {
		t.Fatalf("state during compaction: %+v", state)
	}
	late := s.handle(rpcCommand{ID: strptr("p2"), Type: "prompt", Message: "late steer", StreamingBehavior: "steer"})
	if late == nil || late.Success || late.Error != "Cannot submit a prompt while compaction is in progress. Wait for compaction to finish and retry." {
		t.Fatalf("prompt during compaction: %+v", late)
	}
	second := s.handle(rpcCommand{ID: strptr("c2"), Type: "compact"})
	if second == nil || second.Success || second.Error != "Cannot compact while compaction is in progress." {
		t.Fatalf("second compact: %+v", second)
	}
	// The run settles through cancellation; no release is needed.
	resp := rpcWaitCompactResponse(t, log, "c1")
	if resp["success"] != true {
		t.Fatalf("compact terminal response: %v", resp)
	}
	objs := log.objects(t)
	if n := rpcCountResponses(objs, "c1"); n != 1 {
		t.Fatalf("terminal compact responses = %d, want 1", n)
	}
	if flags := rpcSettledFlags(objs); len(flags) != 2 || !flags[0] || flags[1] {
		t.Fatalf("settled aborted flags = %v, want [true false]", flags)
	}
	if !hasEvent(objs, "compaction_end", map[string]any{"reason": "manual"}) {
		t.Fatalf("missing manual compaction_end: %v", objs)
	}
	for _, obj := range objs {
		if obj["type"] == "extension_error" {
			t.Fatalf("run reported an error: %v", obj)
		}
		if obj["type"] == "message_start" {
			if msg, ok := obj["message"].(map[string]any); ok && msg["content"] == "late steer" {
				t.Fatalf("refused prompt leaked a user event: %v", obj)
			}
		}
	}
	reqs := gate.Requests()
	if len(reqs) != 2 {
		t.Fatalf("provider requests = %d, want 2 (interrupted turn plus summary)", len(reqs))
	}
	if !rpcIsSummary(reqs[1]) {
		t.Fatalf("second request is not the summary: %#v", reqs[1].System)
	}
	rpcAssertUserTranscript(t, store, "old question", "hello")
}

// TestRPCCompactDeliversPreviouslyQueuedSteering proves steering queued
// before a mid-run compact is not stranded: the reservation refuses only
// new prompts, and the compact delivers the earlier queue after the summary.
func TestRPCCompactDeliversPreviouslyQueuedSteering(t *testing.T) {
	gate := &rpcGatedProvider{entered: make(chan struct{}), release: make(chan struct{})}
	s, store, log := newRPCTestRunner(t, gate, nil, 64)
	big := strings.Repeat("context filler for the compaction budget. ", 60)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "old question"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role:    "assistant",
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: big}}},
	}}); err != nil {
		t.Fatal(err)
	}
	first := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "hello"})
	if first == nil || !first.Success {
		t.Fatalf("prompt: %+v", first)
	}
	rpcGateEntered(t, gate)
	steer := s.handle(rpcCommand{ID: strptr("p2"), Type: "prompt", Message: "queued steer", StreamingBehavior: "steer"})
	if steer == nil || !steer.Success {
		t.Fatalf("steer queue: %+v", steer)
	}
	if resp := s.handle(rpcCommand{ID: strptr("c1"), Type: "compact"}); resp != nil {
		t.Fatalf("compact answers from its worker, got %+v", resp)
	}
	// The run settles through cancellation; no release is needed.
	resp := rpcWaitCompactResponse(t, log, "c1")
	if resp["success"] != true {
		t.Fatalf("compact terminal response: %v", resp)
	}
	objs := log.objects(t)
	rpcAssertNoExtensionError(t, objs)
	if flags := rpcSettledFlags(objs); len(flags) != 2 || !flags[0] || flags[1] {
		t.Fatalf("settled aborted flags = %v, want [true false]", flags)
	}
	reqs := gate.Requests()
	if len(reqs) != 3 {
		t.Fatalf("provider requests = %d, want 3 (interrupted turn, summary, delivered steer)", len(reqs))
	}
	if !rpcIsSummary(reqs[1]) {
		t.Fatalf("second request is not the summary: %#v", reqs[1].System)
	}
	if rpcIsSummary(reqs[2]) {
		t.Fatalf("third request is a second summary: %#v", reqs[2].System)
	}
	if last := rpcLastUserText(t, reqs[2]); last != "queued steer" {
		t.Fatalf("third request newest user message = %q, want %q", last, "queued steer")
	}
	rpcAssertUserTranscript(t, store, "old question", "hello", "queued steer")
}

// TestRPCAbortDuringCompactWaitPreventsSummary proves an abort that lands
// while the compact worker waits for the outgoing run prevents the summary:
// no summary request starts and the terminal response reports the abort.
func TestRPCAbortDuringCompactWaitPreventsSummary(t *testing.T) {
	slow := &rpcSlowCancelProvider{
		entered: make(chan struct{}), cancelled: make(chan struct{}),
		release: make(chan struct{}), settleGate: make(chan struct{}),
	}
	t.Cleanup(func() {
		select {
		case <-slow.settleGate:
		default:
			close(slow.settleGate)
		}
	})
	s, _, log := newRPCTestRunner(t, slow, nil)
	first := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "hello"})
	if first == nil || !first.Success {
		t.Fatalf("prompt: %+v", first)
	}
	select {
	case <-slow.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("first request never reached the provider")
	}
	if resp := s.handle(rpcCommand{ID: strptr("c1"), Type: "compact"}); resp != nil {
		t.Fatalf("compact answers from its worker, got %+v", resp)
	}
	// The cancelled run cannot settle before the test opens the gate, so
	// the compact worker is provably inside its wait here.
	select {
	case <-slow.cancelled:
	case <-time.After(8 * time.Second):
		t.Fatal("run was not cancelled")
	}
	aborted := s.handle(rpcCommand{ID: strptr("a1"), Type: "abort"})
	if aborted == nil || !aborted.Success {
		t.Fatalf("abort: %+v", aborted)
	}
	close(slow.settleGate)
	resp := rpcWaitCompactResponse(t, log, "c1")
	if resp["success"] != false || resp["error"] != "compaction aborted" {
		t.Fatalf("compact terminal response: %v", resp)
	}
	objs := log.objects(t)
	if n := rpcCountResponses(objs, "c1"); n != 1 {
		t.Fatalf("terminal compact responses = %d, want 1", n)
	}
	if hasEvent(objs, "compaction_end", nil) {
		t.Fatalf("aborted wait emitted compaction_end: %v", objs)
	}
	if flags := rpcSettledFlags(objs); len(flags) != 1 || !flags[0] {
		t.Fatalf("settled aborted flags = %v, want [true]", flags)
	}
	if reqs := slow.Requests(); len(reqs) != 1 {
		t.Fatalf("provider requests = %d, want 1 (no summary started)", len(reqs))
	}
}

// TestRPCAbortAfterSummaryStartsEmitsAbortedOutcome proves an abort that
// lands after the summary starts surfaces the cancellation: the started
// summary reports aborted, and the terminal response carries the failure.
func TestRPCAbortAfterSummaryStartsEmitsAbortedOutcome(t *testing.T) {
	fake := &provider.Fake{Responses: []*provider.Response{
		{Text: "first answer", StopReason: "stop"},
		{Text: "second answer", StopReason: "stop"},
	}}
	sum := &rpcSummaryGatedProvider{inner: fake, entered: make(chan struct{}), release: make(chan struct{})}
	s, _, log := newRPCTestRunner(t, sum, nil, 64)
	big := strings.Repeat("context filler for the compaction budget. ", 60)
	for i, id := range []string{"p1", "p2"} {
		started := s.handle(rpcCommand{ID: strptr(id), Type: "prompt", Message: big + id})
		if started == nil || !started.Success {
			t.Fatalf("prompt %d = %+v", i, started)
		}
		want := i + 1
		log.waitFor(t, "settled "+id, func(obj map[string]any) bool {
			return obj["type"] == "agent_settled" && len(rpcSettledFlags(log.objects(t))) >= want
		})
	}
	if resp := s.handle(rpcCommand{ID: strptr("c1"), Type: "compact"}); resp != nil {
		t.Fatalf("compact answers from its worker, got %+v", resp)
	}
	select {
	case <-sum.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("summary never started")
	}
	late := s.handle(rpcCommand{ID: strptr("p3"), Type: "prompt", Message: "late", StreamingBehavior: "steer"})
	if late == nil || late.Success || late.Error != "Cannot submit a prompt while compaction is in progress. Wait for compaction to finish and retry." {
		t.Fatalf("prompt during summary: %+v", late)
	}
	aborted := s.handle(rpcCommand{ID: strptr("a1"), Type: "abort"})
	if aborted == nil || !aborted.Success {
		t.Fatalf("abort: %+v", aborted)
	}
	resp := rpcWaitCompactResponse(t, log, "c1")
	if resp["success"] != false || resp["error"] == "" {
		t.Fatalf("compact terminal response: %v", resp)
	}
	objs := log.objects(t)
	if n := rpcCountResponses(objs, "c1"); n != 1 {
		t.Fatalf("terminal compact responses = %d, want 1", n)
	}
	if !hasEvent(objs, "compaction_end", map[string]any{"reason": "manual", "aborted": true}) {
		t.Fatalf("missing aborted manual compaction_end: %v", objs)
	}
	if flags := rpcSettledFlags(objs); len(flags) != 3 || flags[0] || flags[1] || !flags[2] {
		t.Fatalf("settled aborted flags = %v, want [false false true]", flags)
	}
	if n := sum.SummaryCount(); n != 1 {
		t.Fatalf("summary requests = %d, want 1", n)
	}
	if n := fake.CallCount(); n != 2 {
		t.Fatalf("main-turn requests = %d, want 2", n)
	}
}

// TestRPCPromptRefusedDuringAutomaticCompaction proves a prompt that arrives
// while an automatic summary owns the turn fails with P1 and appends
// nothing: the queue stays empty and no user event leaks.
func TestRPCPromptRefusedDuringAutomaticCompaction(t *testing.T) {
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "answer", StopReason: "stop"}}}
	sum := &rpcSummaryGatedProvider{inner: fake, entered: make(chan struct{}), release: make(chan struct{})}
	s, store, log := newRPCTestRunner(t, sum, nil)
	s.r.cfg.AutoCompact = true
	s.r.cfg.KeepRecentTokens = 50
	s.r.cfg.Model.ContextWindow = 20000
	big := strings.Repeat("prior ", 40000)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "old question"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role:    "assistant",
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: big}}},
	}}); err != nil {
		t.Fatal(err)
	}
	started := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "hello"})
	if started == nil || !started.Success {
		t.Fatalf("prompt: %+v", started)
	}
	// The automatic summary fires before the first request; its start event
	// proves the flag is set, so the prompt below lands inside the summary.
	log.waitFor(t, "compaction_start", func(obj map[string]any) bool {
		return obj["type"] == "compaction_start"
	})
	late := s.handle(rpcCommand{ID: strptr("p2"), Type: "prompt", Message: "late steer", StreamingBehavior: "steer"})
	if late == nil || late.Success || late.Error != "Cannot submit a prompt while compaction is in progress. Wait for compaction to finish and retry." {
		t.Fatalf("prompt during automatic summary: %+v", late)
	}
	if n := s.pendingCount(); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
	close(sum.release)
	objs := log.waitFor(t, "settled", isType("agent_settled"))
	if !hasEvent(objs, "compaction_end", map[string]any{"reason": "threshold"}) {
		t.Fatalf("missing threshold compaction_end: %v", objs)
	}
	for _, obj := range objs {
		if obj["type"] == "extension_error" {
			t.Fatalf("run reported an error: %v", obj)
		}
		if obj["type"] == "message_start" {
			if msg, ok := obj["message"].(map[string]any); ok && msg["content"] == "late steer" {
				t.Fatalf("refused prompt leaked a user event: %v", obj)
			}
		}
	}
	if n := sum.SummaryCount(); n != 1 {
		t.Fatalf("summary requests = %d, want 1", n)
	}
	if n := fake.CallCount(); n != 1 {
		t.Fatalf("main-turn requests = %d, want 1", n)
	}
	rpcAssertUserTranscript(t, store, "old question", "hello")
}
