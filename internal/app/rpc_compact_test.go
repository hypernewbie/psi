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

// rpcCompactGateWriter forwards frames to the log but parks the terminal
// compact response until the test releases it, so the test observes whether
// the reservation released before its response went out.
type rpcCompactGateWriter struct {
	dst     *rpcLog
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *rpcCompactGateWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"command":"compact"`)) {
		w.once.Do(func() { close(w.blocked) })
		<-w.release
	}
	return w.dst.Write(p)
}

// TestRPCCompactResponsePrecedesOwnershipRelease proves the terminal compact
// response goes out before the reservation releases: shutdown waits on the
// reservation's done channel, so a release first would let the store unlock
// and the process exit before the response is written.
func TestRPCCompactResponsePrecedesOwnershipRelease(t *testing.T) {
	s, _, log := newRPCTestRunner(t, &provider.Fake{}, nil)
	gate := &rpcCompactGateWriter{dst: log, blocked: make(chan struct{}), release: make(chan struct{})}
	enc := json.NewEncoder(gate)
	enc.SetEscapeHTML(false)
	s.out = enc
	// An empty session has no compaction work, but the worker still owns
	// the runner until its terminal response: the cheapest staging that
	// exercises the release order.
	if resp := s.handle(rpcCommand{ID: strptr("c1"), Type: "compact"}); resp != nil {
		t.Fatalf("compact answers from its worker, got %+v", resp)
	}
	s.mu.Lock()
	res := s.reservation
	s.mu.Unlock()
	if res == nil {
		t.Fatal("compact was not admitted")
	}
	select {
	case <-gate.blocked:
	case <-time.After(8 * time.Second):
		t.Fatal("terminal compact response never reached the writer")
	}
	// The response is parked at the writer: a closed done here proves the
	// release ran before the response went out.
	select {
	case <-res.done:
		t.Fatal("reservation released before its terminal response")
	default:
	}
	close(gate.release)
	select {
	case <-res.done:
	case <-time.After(8 * time.Second):
		t.Fatal("reservation never released")
	}
	if resp := rpcWaitCompactResponse(t, log, "c1"); resp["success"] != false {
		t.Fatalf("empty compact terminal response: %v", resp)
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
