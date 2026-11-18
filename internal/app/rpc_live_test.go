package app

// Mid-run live-settings coverage for `psi --mode rpc`: a held first request
// lets the test change the model and thinking level between two provider
// requests of one run, then proves the second request uses both while every
// queued input still reaches the provider exactly once on the same loop.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"psi/internal/config"
	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
)

// rpcGatedProvider holds the first request until the test releases it, so a
// mid-run command lands deterministically between two provider requests. All
// requests are recorded for model, thinking, and content assertions.
type rpcGatedProvider struct {
	mu       sync.Mutex
	reqs     []provider.Request
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	response *provider.Response
}

func (g *rpcGatedProvider) Send(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	g.mu.Lock()
	g.reqs = append(g.reqs, req)
	first := len(g.reqs) == 1
	resp := g.response
	g.mu.Unlock()
	if first {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if resp == nil {
		resp = &provider.Response{Text: "gated answer", StopReason: "stop"}
	}
	return resp, nil
}

func (g *rpcGatedProvider) Requests() []provider.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]provider.Request, len(g.reqs))
	copy(out, g.reqs)
	return out
}

func (g *rpcGatedProvider) CloseIdle() {}

func rpcGateEntered(t *testing.T, gate *rpcGatedProvider) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("first request never reached the provider")
	}
}

func rpcLiveTextTurn(n int32) string {
	return "event: message_start\ndata: {\"message\":{\"id\":\"rpc-live-" + fmt.Sprint(n) + "\",\"model\":\"rpc-test-model-2\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer-" + fmt.Sprint(n) + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {}\n\n"
}

// rpcLastBodyUserText reads the newest user message of a decoded Anthropic
// request body. Earlier turns stay in the history, so placement is proven
// on the last message, not on whole-body containment.
func rpcLastBodyUserText(t *testing.T, body map[string]any) string {
	t.Helper()
	messages, _ := body["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		msg, _ := messages[i].(map[string]any)
		if msg["role"] != "user" {
			continue
		}
		var sb strings.Builder
		for _, block := range msg["content"].([]any) {
			part, _ := block.(map[string]any)
			if text, ok := part["text"].(string); ok {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(text)
			}
		}
		return sb.String()
	}
	t.Fatalf("request body has no user message: %v", body)
	return ""
}

func rpcAssertNoExtensionError(t *testing.T, objs []map[string]any) {
	t.Helper()
	for _, obj := range objs {
		if obj["type"] == "extension_error" {
			raw, _ := json.Marshal(obj)
			t.Fatalf("run reported an error: %s", raw)
		}
	}
}

func rpcAssertUserTranscript(t *testing.T, store *session.Store, want ...string) {
	t.Helper()
	users := rpcStoredUserTexts(store)
	if len(users) != len(want) {
		t.Fatalf("user transcript = %q, want %q", users, want)
	}
	for i, text := range want {
		if users[i] != text {
			t.Fatalf("user transcript = %q, want %q", users, want)
		}
	}
}

// TestRPCSetThinkingDuringRunAppliesToNextRequest proves a thinking level
// changed mid-run reaches the next provider request, and that the queued
// follow-up still extends the same run exactly once.
func TestRPCSetThinkingDuringRunAppliesToNextRequest(t *testing.T) {
	gate := &rpcGatedProvider{entered: make(chan struct{}), release: make(chan struct{})}
	s, store, log := newRPCTestRunner(t, gate, nil)
	old := s.loop
	first := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "hello"})
	if first == nil || !first.Success {
		t.Fatalf("prompt: %+v", first)
	}
	rpcGateEntered(t, gate)
	queued := s.handle(rpcCommand{ID: strptr("p2"), Type: "prompt", Message: "queued follow-up", StreamingBehavior: "followUp"})
	if queued == nil || !queued.Success {
		t.Fatalf("follow-up queue: %+v", queued)
	}
	switched := s.handle(rpcCommand{ID: strptr("t1"), Type: "set_thinking_level", Level: "high"})
	if switched == nil || !switched.Success {
		t.Fatalf("mid-run thinking change was refused: %+v", switched)
	}
	if s.loop != old {
		t.Fatal("thinking change replaced the agent loop")
	}
	if n := s.pendingCount(); n != 1 {
		t.Fatalf("pending = %d, want 1 queued follow-up", n)
	}
	close(gate.release)
	objs := log.waitFor(t, "settled", isType("agent_settled"))
	rpcAssertNoExtensionError(t, objs)
	reqs := gate.Requests()
	if len(reqs) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(reqs))
	}
	if reqs[0].Thinking != "off" || reqs[1].Thinking != "high" {
		t.Fatalf("thinking per request = %q/%q, want off/high", reqs[0].Thinking, reqs[1].Thinking)
	}
	if reqs[0].Model.ID != "rpc-test-model" || reqs[1].Model.ID != "rpc-test-model" {
		t.Fatalf("model per request = %q/%q, want rpc-test-model both", reqs[0].Model.ID, reqs[1].Model.ID)
	}
	if last := rpcLastUserText(t, reqs[0]); last != "hello" {
		t.Fatalf("first request newest user message = %q, want %q", last, "hello")
	}
	if last := rpcLastUserText(t, reqs[1]); last != "queued follow-up" {
		t.Fatalf("second request newest user message = %q, want %q", last, "queued follow-up")
	}
	rpcAssertUserTranscript(t, store, "hello", "queued follow-up")
}

// rpcRetirementGate holds set_model inside the synchronous dispatch while
// it retires the old provider.
type rpcRetirementGate struct {
	provider.Fake
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *rpcRetirementGate) CloseIdle() {
	p.once.Do(func() { close(p.entered) })
	<-p.release
}

func TestRPCSignalWaitsForSynchronousSetter(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")
	synctest.Test(t, func(t *testing.T) {
		gate := &rpcRetirementGate{entered: make(chan struct{}), release: make(chan struct{})}
		var once sync.Once
		release := func() { once.Do(func() { close(gate.release) }) }
		defer release()
		s, store, log := newRPCTestRunner(t, gate, nil)
		s.shutdown = make(chan struct{})
		in, client := io.Pipe()
		defer client.Close()
		signals := make(chan os.Signal, 1)
		cleaned := make(chan struct{})
		done := make(chan bool, 1)
		go func() {
			done <- s.serve(in, signals, func() {
				store.Unlock()
				close(cleaned)
			})
		}()
		if _, err := io.WriteString(client, `{"type":"set_model","provider":"anthropic","modelId":"rpc-test-model-2"}`+"\n"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-gate.entered:
		default:
			t.Fatal("setter did not reach provider retirement")
		}
		signals <- syscall.SIGTERM
		<-s.shutdown
		synctest.Wait()
		select {
		case <-cleaned:
			t.Fatal("released the session lock while set_model was still dispatching")
		default:
		}
		release()
		if signalled := <-done; !signalled {
			t.Fatal("shutdown lost the signal exit status")
		}
		select {
		case <-cleaned:
		default:
			t.Fatal("signal shutdown did not release the lock")
		}
		frames := log.objects(t)
		if len(frames) != 1 || frames[0]["command"] != "set_model" || frames[0]["success"] != true {
			t.Fatalf("shutdown lost the setter response: %+v", frames)
		}
	})
}

func TestRPCFailedSelectionAppendDoesNotPublish(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")
	for _, cmd := range []rpcCommand{
		{Type: "set_model", Provider: "anthropic", ModelID: "rpc-test-model-2"},
		{Type: "set_thinking_level", Level: "high"},
	} {
		t.Run(cmd.Type, func(t *testing.T) {
			fake := &provider.Fake{}
			s, store, _ := newRPCTestRunner(t, fake, nil)
			if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
				t.Fatal(err)
			}
			// Move the durable file away so the next append fails before
			// updating any in-memory metadata. Both paths are test-owned.
			if err := os.Rename(store.File(), store.File()+".backup"); err != nil {
				t.Fatal(err)
			}
			if resp := s.handle(cmd); resp == nil || resp.Success || !strings.Contains(resp.Error, "cannot inspect active session") {
				t.Fatalf("expected append failure, got %+v", resp)
			}
			live := s.r.live.snapshot()
			if live.Model.ID != "rpc-test-model" || live.Thinking != "off" || live.Provider != fake || s.r.ov.Model != "rpc-test-model" || s.r.ov.Thinking != "off" {
				t.Fatalf("failed selection reached live settings: %s/%s", live.Model.ID, live.Thinking)
			}
		})
	}
}

// The final queue poll precedes RunEnd. A prompt acknowledged in that gap
// must reach the provider before RPC emits agent_settled, not the next run.
func TestRPCLateFollowUpSettlesWithItsRun(t *testing.T) {
	fake := &provider.Fake{Responses: []*provider.Response{
		{Text: "initial answer", StopReason: "stop"},
		{Text: "follow-up answer", StopReason: "stop"},
	}}
	s, store, log := newRPCTestRunner(t, fake, nil)
	polled, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	resume := func() { unblock.Do(func() { close(release) }) }
	defer resume()
	var firstEnd sync.Once
	s.loop = s.r.newLoop(func(ev event.Event) {
		if ev.Type == event.RunEnd {
			firstEnd.Do(func() {
				close(polled)
				<-release
			})
		}
		s.sink(ev)
	})
	if resp := s.handle(rpcCommand{Type: "prompt", Message: "initial question"}); resp == nil || !resp.Success {
		t.Fatalf("initial prompt: %+v", resp)
	}
	select {
	case <-polled:
	case <-time.After(8 * time.Second):
		t.Fatal("agent did not reach the final queue poll")
	}
	if resp := s.handle(rpcCommand{Type: "prompt", Message: "late question", StreamingBehavior: "followUp"}); resp == nil || !resp.Success {
		t.Fatalf("late follow-up: %+v", resp)
	}
	resume()
	frames := log.waitFor(t, "agent_settled", isType("agent_settled"))
	starts := 0
	for _, frame := range frames {
		if frame["type"] == "agent_start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("one RPC run should emit one agent_start, got %d", starts)
	}
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if n := fake.CallCount(); n != 2 || len(s.loop.PendingFollowUp()) != 0 {
		t.Fatalf("settled before draining the accepted follow-up: %d provider requests", n)
	}
	last := session.LastConversationalMessage(proj.Flat)
	if last == nil || last.Assistant == nil || len(last.Assistant.Content) != 1 || last.Assistant.Content[0].Text == nil || last.Assistant.Content[0].Text.Text != "follow-up answer" {
		t.Fatalf("late follow-up answer not durable at settlement: %+v", last)
	}
}

// TestRPCSelectionSurvivesOlderRequestSettlement pauses after the worker has
// taken its next-request snapshot but before it synchronizes selection metadata.
// The acknowledged setting must survive even if this is the last response.
func TestRPCSelectionSurvivesOlderRequestSettlement(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "complete", StopReason: "stop"}}}
	s, store, _ := newRPCTestRunner(t, fake, nil)
	cfg := s.runConfig()
	dynamic := cfg.DynamicConfig
	snapshotted, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(resume) }) }
	defer unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	calls := 0
	cfg.DynamicConfig = func() (string, []config.PromptSection, []provider.ToolDef) {
		calls++
		if calls == 2 {
			close(snapshotted)
			select {
			case <-resume:
			case <-ctx.Done():
			}
		}
		return dynamic()
	}
	done := make(chan error, 1)
	go func() { done <- s.loop.Submit(ctx, "hello", cfg) }()
	select {
	case <-snapshotted:
	case <-ctx.Done():
		t.Fatal("turn did not take a request snapshot")
	}
	for _, cmd := range []rpcCommand{
		{Type: "set_model", Provider: "anthropic", ModelID: "rpc-test-model-2"},
		{Type: "set_thinking_level", Level: "high"},
	} {
		if resp := s.handle(cmd); resp == nil || !resp.Success {
			t.Fatalf("%s: %+v", cmd.Type, resp)
		}
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("turn did not settle")
	}
	reqs := fake.Requests()
	if len(reqs) != 1 || reqs[0].Model.ID != "rpc-test-model" || reqs[0].Thinking != "off" {
		t.Fatalf("request snapshot changed: %+v", reqs)
	}
	reopened, err := session.OpenFile(store.File(), store.Cwd(), false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopened.ModelState()
	if err != nil {
		t.Fatal(err)
	}
	if state.ModelID != "rpc-test-model-2" || state.ThinkingLevel != "high" {
		t.Fatalf("reopened selection = %+v, want rpc-test-model-2/high", state)
	}
}

// TestRPCSetModelDuringRunPreservesLoopAndQueue proves a model changed
// mid-run reaches the next provider request on the same loop: steering and
// follow-up queued while the first request is held each reach the provider
// exactly once, and the loop object is never replaced.
func TestRPCSetModelDuringRunPreservesLoopAndQueue(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")
	bodies := make(chan map[string]any, 8)
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/messages" || req.Header.Get("x-api-key") != "test" {
			t.Errorf("endpoint/auth not used: %s", req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, rpcLiveTextTurn(turns.Add(1)))
	}))
	defer server.Close()
	gate := &rpcGatedProvider{entered: make(chan struct{}), release: make(chan struct{})}
	s, store, log := newRPCTestRunner(t, gate, nil)
	// The first request stays on the gate; the switched-to model answers
	// over loopback so the test observes the live request bodies.
	catalogDir := t.TempDir()
	if err := writeTestFile(t, catalogDir, "models.json", `{"providers":{"anthropic":{"models":[`+
		`{"id":"rpc-test-model","name":"RPC Test","contextWindow":64000,"maxTokens":16384,"reasoning":true,"input":["text"]},`+
		`{"id":"rpc-test-model-2","name":"RPC Test 2","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":64000,"maxTokens":16384,"reasoning":true,"input":["text"]}]}}}`); err != nil {
		t.Fatal(err)
	}
	models, err := config.LoadModels(catalogDir + "/models.json")
	if err != nil {
		t.Fatal(err)
	}
	s.r.models = models
	old := s.loop
	first := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "hello"})
	if first == nil || !first.Success {
		t.Fatalf("prompt: %+v", first)
	}
	rpcGateEntered(t, gate)
	steer := s.handle(rpcCommand{ID: strptr("p2"), Type: "prompt", Message: "queued steer", StreamingBehavior: "steer"})
	if steer == nil || !steer.Success {
		t.Fatalf("steer queue: %+v", steer)
	}
	follow := s.handle(rpcCommand{ID: strptr("p3"), Type: "prompt", Message: "queued follow-up", StreamingBehavior: "followUp"})
	if follow == nil || !follow.Success {
		t.Fatalf("follow-up queue: %+v", follow)
	}
	switched := s.handle(rpcCommand{ID: strptr("m1"), Type: "set_model", Provider: "anthropic", ModelID: "rpc-test-model-2"})
	if switched == nil || !switched.Success {
		t.Fatalf("mid-run model change was refused: %+v", switched)
	}
	thinking := s.handle(rpcCommand{ID: strptr("t1"), Type: "set_thinking_level", Level: "high"})
	if thinking == nil || !thinking.Success {
		t.Fatalf("mid-run thinking change was refused: %+v", thinking)
	}
	if s.loop != old {
		t.Fatal("model switch replaced the agent loop and dropped its queues")
	}
	if n := s.pendingCount(); n != 2 {
		t.Fatalf("pending = %d, want 2 queued inputs", n)
	}
	state := s.handle(rpcCommand{ID: strptr("g1"), Type: "get_state"})
	model, _ := state.Data.(map[string]any)["model"].(map[string]any)
	if model["id"] != "rpc-test-model-2" || state.Data.(map[string]any)["thinkingLevel"] != "high" {
		t.Fatalf("state after switch: %+v", state.Data)
	}
	close(gate.release)
	objs := log.waitFor(t, "settled", isType("agent_settled"))
	rpcAssertNoExtensionError(t, objs)
	held := gate.Requests()
	if len(held) != 1 {
		t.Fatalf("held provider requests = %d, want 1", len(held))
	}
	if held[0].Model.ID != "rpc-test-model" || held[0].Thinking != "off" {
		t.Fatalf("first request = %s/%s, want rpc-test-model/off", held[0].Model.ID, held[0].Thinking)
	}
	var live []map[string]any
	for len(live) < 2 {
		select {
		case body := <-bodies:
			live = append(live, body)
		case <-time.After(8 * time.Second):
			t.Fatalf("live requests = %d, want 2", len(live))
		}
	}
	select {
	case body := <-bodies:
		raw, _ := json.Marshal(body)
		t.Fatalf("unexpected third live request: %s", raw)
	case <-time.After(200 * time.Millisecond):
	}
	for i, body := range live {
		if body["model"] != "rpc-test-model-2" {
			t.Fatalf("live request %d model = %v, want rpc-test-model-2", i+2, body["model"])
		}
		thinkingBlock, ok := body["thinking"].(map[string]any)
		if !ok || thinkingBlock["budget_tokens"] != float64(8192) {
			t.Fatalf("live request %d thinking = %v, want budget 8192", i+2, body["thinking"])
		}
	}
	rawSecond, _ := json.Marshal(live[0])
	if strings.Contains(string(rawSecond), "queued follow-up") {
		t.Fatalf("second request carried the undelivered follow-up: %s", rawSecond)
	}
	if last := rpcLastBodyUserText(t, live[0]); last != "queued steer" {
		t.Fatalf("second request newest user message = %q, want %q", last, "queued steer")
	}
	if last := rpcLastBodyUserText(t, live[1]); last != "queued follow-up" {
		t.Fatalf("third request newest user message = %q, want %q", last, "queued follow-up")
	}
	rpcAssertUserTranscript(t, store, "hello", "queued steer", "queued follow-up")
}
