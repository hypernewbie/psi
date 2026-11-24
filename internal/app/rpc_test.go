package app

// Golden JSONL tests for `psi --mode rpc`. Each test drives the runner the
// way Phi does: one command in, responses and events out, decoded in order.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/skills"
	"github.com/hypernewbie/psi/internal/tools"
)

// rpcLog captures encoded stdout lines with a lock, since agent events
// arrive on background goroutines.
type rpcLog struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	notify chan struct{}
}

func (l *rpcLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	n, err := l.buf.Write(p)
	l.mu.Unlock()
	if l.notify != nil {
		select {
		case l.notify <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (l *rpcLog) objects(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	// Live subprocess reads can end partway through the next JSONL frame.
	// Decode only complete lines while the writer is still running.
	text := l.buf.String()
	var out []map[string]any
	for _, line := range strings.Split(text[:strings.LastIndexByte(text, '\n')+1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("stdout line is not JSON: %q: %v", line, err)
		}
		out = append(out, obj)
	}
	return out
}

func (l *rpcLog) waitFor(t *testing.T, what string, match func(map[string]any) bool) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, obj := range l.objects(t) {
			if match(obj) {
				return l.objects(t)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; got %d lines", what, len(l.objects(t)))
	return nil
}

// waitObjects waits for a log notification, then evaluates the predicate on a
// fresh snapshot. The timeout is a deadlock guard, not a settle delay.
func (l *rpcLog) waitObjects(t *testing.T, what string, match func([]map[string]any) bool) []map[string]any {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		objects := l.objects(t)
		if match(objects) {
			return objects
		}
		select {
		case <-l.notify:
		case <-timer.C:
			t.Fatalf("timed out waiting for %s; got %d lines", what, len(objects))
		}
	}
}

func isType(typ string) func(map[string]any) bool {
	return func(obj map[string]any) bool { return obj["type"] == typ }
}

const rpcTestModels = `{"providers":{"anthropic":{"api":"anthropic-messages","models":[` +
	`{"id":"rpc-test-model","name":"RPC Test","contextWindow":64000,"maxTokens":1000,"reasoning":true,"input":["text"]},` +
	`{"id":"rpc-test-model-2","name":"RPC Test 2","contextWindow":64000,"maxTokens":1000,"reasoning":true,"input":["text"]}]}}}`

func newRPCTestRunner(t *testing.T, prov provider.Provider, skillList []skills.Skill, keepRecent ...int) (*rpcRunner, *session.Store, *rpcLog) {
	t.Helper()
	recent := 4000
	if len(keepRecent) > 0 {
		recent = keepRecent[0]
	}
	ws, sessDir, agentDir := t.TempDir(), t.TempDir(), t.TempDir()
	if err := writeTestFile(t, agentDir, "models.json", rpcTestModels); err != nil {
		t.Fatal(err)
	}
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: sessDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TryLock(); err != nil {
		t.Fatal(err)
	}
	models, err := config.LoadModels(agentDir + "/models.json")
	if err != nil {
		t.Fatal(err)
	}
	settings := config.Settings{DefaultTools: []string{"read"}}
	ov := config.ResolveStartup("anthropic", "rpc-test-model", "off", "", false, "", "", "", settings, "")
	meta, err := models.SelectModel("anthropic", "rpc-test-model")
	if err != nil {
		t.Fatal(err)
	}
	cfg := agent.Config{
		Model: providerModel("anthropic", meta), Thinking: "off", System: "test",
		Tools:            []provider.ToolDef{{Name: "read", Description: "read", Schema: map[string]any{"type": "object"}}},
		ReserveTokens:    1000,
		KeepRecentTokens: recent,
	}
	r := &runtime{
		store: store, settings: settings, models: models,
		skills: skillList, prompt: "test", agentDir: agentDir,
		ov: ov, cfg: cfg, prov: prov, reg: tools.NewRegistry(ws),
	}
	log := &rpcLog{}
	s := &rpcRunner{r: r}
	s.encoder = newRPCEncoder(log, nil)
	s.loop = r.newLoop(s.sink)
	t.Cleanup(func() { closeRPCTestRunner(s, prov) })
	return s, store, log
}

// closeRPCTestRunner mirrors the production EOF path: cancel the run, wait for
// it to settle, then release the store and provider. TempDir removal races the
// run's session writes if the store is unlocked first. Every step is safe to
// repeat, so a test can call this before its registered cleanup.
func closeRPCTestRunner(s *rpcRunner, prov provider.Provider) {
	s.cancelActive()
	s.waitWorkers()
	if s.r.store != nil {
		s.r.store.Unlock()
	}
	if prov != nil {
		prov.CloseIdle()
	}
}

func writeTestFile(t *testing.T, dir, name, content string) error {
	t.Helper()
	return os.WriteFile(dir+"/"+name, []byte(content), 0600)
}

func strptr(s string) *string { return &s }

func responseByID(objs []map[string]any, id string) map[string]any {
	for _, obj := range objs {
		if obj["type"] == "response" && obj["id"] == id {
			return obj
		}
	}
	return nil
}

func hasEvent(objs []map[string]any, typ string, match map[string]any) bool {
	for _, obj := range objs {
		if obj["type"] != typ {
			continue
		}
		ok := true
		for k, v := range match {
			if obj[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestRPCUnknownCommand(t *testing.T) {
	s, _, log := newRPCTestRunner(t, &provider.Fake{}, nil)
	resp := s.handle(rpcCommand{ID: strptr("7"), Type: "teleport"})
	if resp == nil || resp.Success || resp.Error != "Unknown command: teleport" || resp.Command != "teleport" {
		t.Fatalf("unknown command response = %+v", resp)
	}
	s.emit(resp)
	got := responseByID(log.objects(t), "7")
	if got == nil || got["error"] != "Unknown command: teleport" {
		t.Fatalf("encoded unknown response = %v", log.objects(t))
	}
}

func TestRPCRunLoopSkipsBlanksAndReportsParseErrors(t *testing.T) {
	s, _, log := newRPCTestRunner(t, &provider.Fake{}, nil)
	s.runLoop(strings.NewReader("\n   \n{bad json}\n"))
	objs := log.waitFor(t, "parse error", func(obj map[string]any) bool {
		return obj["type"] == "response" && obj["command"] == "parse"
	})
	if len(objs) != 1 {
		t.Fatalf("blank lines produced output: %v", objs)
	}
	if objs[0]["success"] != false || !strings.HasPrefix(objs[0]["error"].(string), "Failed to parse command:") {
		t.Fatalf("parse error shape = %v", objs[0])
	}
	if _, present := objs[0]["id"]; present {
		t.Fatalf("id-less parse error echoed an id: %v", objs[0])
	}
}

func TestRPCPromptStartsStreamsAndSettles(t *testing.T) {
	fake := &provider.Fake{
		Responses:  []*provider.Response{{Text: "hello there", StopReason: "stop"}},
		TextDeltas: [][]string{{"hello ", "there"}},
	}
	s, store, log := newRPCTestRunner(t, fake, nil)
	resp := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "hi"})
	if resp == nil || !resp.Success || resp.Data.(map[string]any)["disposition"] != "started" {
		t.Fatalf("prompt response = %+v", resp)
	}
	objs := log.waitFor(t, "agent_settled", isType("agent_settled"))

	// The caller-side message publishes first, so clients render instantly.
	userStart := -1
	for i, obj := range objs {
		if obj["type"] == "message_start" && obj["message"].(map[string]any)["role"] == "user" {
			userStart = i
			break
		}
	}
	if userStart < 0 {
		t.Fatalf("missing optimistic user message_start: %v", objs)
	}
	if objs[userStart]["message"].(map[string]any)["content"] != "hi" {
		t.Fatalf("user message content = %v", objs[userStart])
	}
	if !hasEvent(objs, "agent_start", nil) {
		t.Fatalf("missing agent_start: %v", objs)
	}
	deltas := 0
	for _, obj := range objs {
		if obj["type"] == "message_update" {
			deltas++
			ev := obj["assistantMessageEvent"].(map[string]any)
			if ev["type"] != "text_delta" || ev["delta"] == "" {
				t.Fatalf("bad text delta: %v", obj)
			}
		}
	}
	if deltas != 2 {
		t.Fatalf("text deltas = %d, want 2: %v", deltas, objs)
	}
	// The assistant start precedes deltas; the durable end closes that stream.
	start, end := -1, -1
	for i, obj := range objs {
		if msg, ok := obj["message"].(map[string]any); ok && msg["role"] == "assistant" {
			if obj["type"] == "message_start" {
				start = i
			}
			if obj["type"] == "message_end" {
				end = i
			}
		}
	}
	if start < 0 || end <= start+1 {
		t.Fatalf("assistant stream boundaries missing or out of order: %v", objs)
	}
	settled := objs[len(objs)-1]
	if settled["type"] != "agent_settled" || settled["aborted"] != false {
		t.Fatalf("settled = %v", settled)
	}
	var count int
	if proj, err := store.Projection(); err != nil {
		t.Fatal(err)
	} else {
		count = len(proj.Flat)
	}
	if count == 0 {
		t.Fatal("prompt left no durable messages")
	}
	requests := fake.Requests()
	if len(requests) != 1 || requests[0].System != s.r.prompt {
		t.Fatalf("RPC request lost the configured system prompt: %#v", requests)
	}
}

func TestRPCPromptBusyRequiresBehavior(t *testing.T) {
	block := newRPCBlockingProvider()
	s, _, log := newRPCTestRunner(t, block, nil)
	defer close(block.release)
	first := s.handle(rpcCommand{ID: strptr("b1"), Type: "prompt", Message: "first"})
	if first == nil || !first.Success {
		t.Fatalf("first prompt = %+v", first)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !s.loop.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.loop.Running() {
		t.Fatal("run did not start")
	}
	second := s.handle(rpcCommand{ID: strptr("b2"), Type: "prompt", Message: "second"})
	if second == nil || second.Success || second.Error != "Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message." {
		t.Fatalf("busy prompt without behavior = %+v", second)
	}
	queued := s.handle(rpcCommand{ID: strptr("b3"), Type: "prompt", Message: "second", StreamingBehavior: "followUp"})
	if queued == nil || !queued.Success || queued.Data.(map[string]any)["disposition"] != "queued" {
		t.Fatalf("queued prompt = %+v", queued)
	}
	aborted := s.handle(rpcCommand{ID: strptr("b4"), Type: "abort"})
	if aborted == nil || !aborted.Success {
		t.Fatalf("abort = %+v", aborted)
	}
	objs := log.waitFor(t, "agent_settled", isType("agent_settled"))
	last := objs[len(objs)-1]
	if last["aborted"] != true {
		t.Fatalf("settled after abort = %v", last)
	}
}

type rpcBlockingProvider struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newRPCBlockingProvider() *rpcBlockingProvider {
	return &rpcBlockingProvider{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *rpcBlockingProvider) Send(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return &provider.Response{Text: "done", StopReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *rpcBlockingProvider) CloseIdle() {}

func TestRPCAbortIdleSucceeds(t *testing.T) {
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	resp := s.handle(rpcCommand{ID: strptr("a"), Type: "abort"})
	if resp == nil || !resp.Success {
		t.Fatalf("idle abort = %+v", resp)
	}
}

// get_available_models offers the same credentialed, selectable catalog as
// /model and the bridge: an unlogged-in provider must not be listed, or every
// switch to it fails at request time.
func TestRPCGetAvailableModelsListsOnlyCredentialedProviders(t *testing.T) {
	clearProviderAPIKeys(t)
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	resp := s.handle(rpcCommand{ID: strptr("g"), Type: "get_available_models"})
	if resp == nil || !resp.Success {
		t.Fatalf("get_available_models = %+v", resp)
	}
	if entries := resp.Data.(map[string]any)["models"].([]rpcModel); len(entries) != 0 {
		t.Fatalf("models listed without a credential: %v", entries)
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-rpc-test")
	resp = s.handle(rpcCommand{ID: strptr("g2"), Type: "get_available_models"})
	if resp == nil || !resp.Success {
		t.Fatalf("get_available_models with a credential = %+v", resp)
	}
	entries, _ := resp.Data.(map[string]any)["models"].([]rpcModel)
	if len(entries) == 0 {
		t.Fatal("credentialed provider was not listed")
	}
	seen := map[string]rpcModel{}
	for _, entry := range entries {
		if entry.Provider != "anthropic" {
			t.Fatalf("uncredentialed provider was listed: %+v", entry)
		}
		seen[entry.ID] = entry
	}
	if seen["rpc-test-model"].Name != "RPC Test" || seen["rpc-test-model-2"].Name != "RPC Test 2" {
		t.Fatalf("configured models or display names missing: %v", entries)
	}
}

func TestRPCGetStateShape(t *testing.T) {
	s, store, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	resp := s.handle(rpcCommand{ID: strptr("g"), Type: "get_state"})
	if resp == nil || !resp.Success {
		t.Fatalf("get_state = %+v", resp)
	}
	data := resp.Data.(map[string]any)
	model := data["model"].(map[string]any)
	if model["provider"] != "anthropic" || model["id"] != "rpc-test-model" || model["name"] != "RPC Test" {
		t.Fatalf("model = %v", model)
	}
	if data["sessionFile"] != store.File() || data["sessionId"] != store.Header().ID {
		t.Fatalf("session identity = %v", data)
	}
	for _, key := range []string{"thinkingLevel", "isStreaming", "isCompacting", "steeringMode", "followUpMode", "sessionName", "autoCompactionEnabled", "messageCount", "pendingMessageCount"} {
		if _, ok := data[key]; !ok {
			t.Fatalf("get_state missing %q: %v", key, data)
		}
	}
}

// get_state reports the modes the loop delivers with, not constants: a client
// must never be told a mode the RPC runtime never applied.
func TestRPCGetStateReportsQueueModes(t *testing.T) {
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	s.r.settings.SteeringMode = "one-at-a-time"
	s.r.settings.FollowUpMode = "all"
	s.loop = s.r.newLoop(s.sink)

	resp := s.handle(rpcCommand{ID: strptr("g"), Type: "get_state"})
	if resp == nil || !resp.Success {
		t.Fatalf("get_state = %+v", resp)
	}
	data := resp.Data.(map[string]any)
	if data["steeringMode"] != "one-at-a-time" || data["followUpMode"] != "all" {
		t.Fatalf("queue modes = %v/%v, want one-at-a-time/all", data["steeringMode"], data["followUpMode"])
	}
}

func TestRPCSetModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	bad := s.handle(rpcCommand{ID: strptr("m1"), Type: "set_model", Provider: "nope", ModelID: "x"})
	if bad == nil || bad.Success || bad.Error != "Model not found: nope/x" {
		t.Fatalf("unknown provider = %+v", bad)
	}
	badModel := s.handle(rpcCommand{ID: strptr("m2"), Type: "set_model", Provider: "anthropic", ModelID: "missing"})
	if badModel == nil || badModel.Success {
		t.Fatalf("unknown model = %+v", badModel)
	}
	same := s.handle(rpcCommand{ID: strptr("m3"), Type: "set_model", Provider: "anthropic", ModelID: "rpc-test-model"})
	if same == nil || !same.Success {
		t.Fatalf("same model = %+v", same)
	}
	other := s.handle(rpcCommand{ID: strptr("m4"), Type: "set_model", Provider: "anthropic", ModelID: "rpc-test-model-2"})
	if other == nil || other.Success {
		t.Fatalf("model without credentials should fail, got %+v", other)
	}
}

func TestRPCSetThinkingLevel(t *testing.T) {
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	bad := s.handle(rpcCommand{ID: strptr("t1"), Type: "set_thinking_level", Level: "bogus"})
	if bad == nil || bad.Success || !strings.Contains(bad.Error, "unsupported thinking level") {
		t.Fatalf("bogus level = %+v", bad)
	}
	levels := provider.SupportedThinking(s.r.cfg.Model)
	if len(levels) == 0 {
		t.Fatal("fixture model supports no thinking levels")
	}
	ok := s.handle(rpcCommand{ID: strptr("t2"), Type: "set_thinking_level", Level: levels[0]})
	if ok == nil || !ok.Success {
		t.Fatalf("supported level = %+v", ok)
	}
	if s.r.ov.Thinking != levels[0] || s.r.cfg.Thinking != levels[0] {
		t.Fatalf("thinking not applied: ov=%q cfg=%q", s.r.ov.Thinking, s.r.cfg.Thinking)
	}
}

// A level the model can apply is applied even when it is not advertised: the
// provider clamps it, so no valid level is refused at this surface.
func TestRPCSetThinkingLevelAcceptsEveryValidLevel(t *testing.T) {
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	s.r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{}}
	t.Setenv("ZAI_API_KEY", "synthetic-zai-key")
	if err := s.r.configure(config.StartupOverrides{Provider: "zai", Model: "glm-5.3", Thinking: "high"}); err != nil {
		t.Fatal(err)
	}
	advertised := provider.SupportedThinking(s.r.cfg.Model)
	if len(advertised) >= len(config.ValidThinkingLevels) {
		t.Skipf("fixture model advertises every level: %v", advertised)
	}
	for _, level := range config.ValidThinkingLevels {
		res := s.handle(rpcCommand{ID: strptr("t-" + level), Type: "set_thinking_level", Level: level})
		if res == nil || !res.Success {
			t.Fatalf("valid level %q was refused: %+v", level, res)
		}
		if s.r.ov.Thinking != level {
			t.Fatalf("level %q not applied: %q", level, s.r.ov.Thinking)
		}
	}
}

func TestRPCCompactEmptyThenAfterExchange(t *testing.T) {
	long := strings.Repeat("context filler for the compaction budget. ", 60)
	fake := &provider.Fake{
		Responses: []*provider.Response{
			{Text: long, StopReason: "stop"},
			{Text: long, StopReason: "stop"},
			{Text: long, StopReason: "stop"},
			{Text: "compact summary", StopReason: "stop"},
		},
	}
	s, _, log := newRPCTestRunner(t, fake, nil, 64)
	empty := s.handle(rpcCommand{ID: strptr("c0"), Type: "compact"})
	if empty != nil {
		t.Fatalf("compact admission must be asynchronous, got %+v", empty)
	}
	log.waitFor(t, "empty compact response", func(obj map[string]any) bool {
		return obj["type"] == "response" && obj["id"] == "c0"
	})
	if got := responseByID(log.objects(t), "c0"); got == nil || got["success"] != false {
		t.Fatalf("compact of empty session should fail, got %+v", got)
	}
	for i, id := range []string{"c1a", "c1b"} {
		started := s.handle(rpcCommand{ID: strptr(id), Type: "prompt", Message: long})
		if started == nil || !started.Success {
			t.Fatalf("prompt %d = %+v", i, started)
		}
		want := i + 1
		log.waitFor(t, "agent_settled "+id, func(obj map[string]any) bool {
			if obj["type"] != "agent_settled" {
				return false
			}
			n := 0
			for _, o := range log.objects(t) {
				if o["type"] == "agent_settled" {
					n++
				}
			}
			return n >= want
		})
	}
	done := s.handle(rpcCommand{ID: strptr("c2"), Type: "compact", CustomInstructions: "focus"})
	if done != nil {
		t.Fatalf("compact admission must be asynchronous, got %+v", done)
	}
	log.waitFor(t, "compact response c2", func(obj map[string]any) bool {
		return obj["type"] == "response" && obj["id"] == "c2"
	})
	if got := responseByID(log.objects(t), "c2"); got == nil || got["success"] != true {
		t.Fatalf("compact = %+v", got)
	}
	objs := log.objects(t)
	if !hasEvent(objs, "compaction_start", map[string]any{"reason": "manual"}) {
		t.Fatalf("missing compaction_start: %v", objs)
	}
	if !hasEvent(objs, "compaction_end", nil) {
		t.Fatalf("missing compaction_end: %v", objs)
	}
}

// gatedProvider holds a run inside Send until the test releases it, so cleanup
// order is observable instead of timing-dependent.
type gatedProvider struct {
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	unlocked  chan struct{}
	store     *session.Store
}

func (p *gatedProvider) Send(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	close(p.entered)
	<-ctx.Done()
	close(p.cancelled)
	<-p.release
	// Cleanup must not release the store while this run still writes to it.
	if p.store != nil {
		if err := p.store.TryLock(); err == nil {
			p.store.Unlock()
			close(p.unlocked)
		}
	}
	return &provider.Response{Text: "hi", StopReason: "stop"}, nil
}

func (p *gatedProvider) CloseIdle() {}

func TestRPCTestRunnerCleanupWaitsForActiveRun(t *testing.T) {
	prov := &gatedProvider{
		entered: make(chan struct{}), cancelled: make(chan struct{}),
		release: make(chan struct{}), unlocked: make(chan struct{}),
	}
	s, store, _ := newRPCTestRunner(t, prov, nil)
	prov.store = store
	if resp := s.handle(rpcCommand{ID: strptr("p"), Type: "prompt", Message: "hello"}); resp == nil || !resp.Success {
		t.Fatalf("prompt = %+v", resp)
	}
	<-prov.entered
	// Let the run settle only after cleanup cancels it, so settlement happens
	// while cleanup is waiting rather than before it starts.
	go func() {
		<-prov.cancelled
		close(prov.release)
	}()
	closeRPCTestRunner(s, prov)
	if s.isRunning() {
		t.Fatal("cleanup returned while the run was still active")
	}
	select {
	case <-prov.unlocked:
		t.Fatal("cleanup released the store while the run was still active")
	default:
	}
}

func TestRPCSessionStats(t *testing.T) {
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "hi", StopReason: "stop"}}}
	s, _, _ := newRPCTestRunner(t, fake, nil)
	if resp := s.handle(rpcCommand{ID: strptr("s0"), Type: "prompt", Message: "hello"}); resp == nil || !resp.Success {
		t.Fatalf("prompt = %+v", resp)
	}
	resp := s.handle(rpcCommand{ID: strptr("s1"), Type: "get_session_stats"})
	if resp == nil || !resp.Success {
		t.Fatalf("stats = %+v", resp)
	}
	data := resp.Data.(map[string]any)
	tokens := data["tokens"].(map[string]any)
	for _, key := range []string{"input", "output", "cacheRead", "cacheWrite"} {
		if _, ok := tokens[key]; !ok {
			t.Fatalf("tokens missing %q: %v", key, data)
		}
	}
	usage := data["contextUsage"].(map[string]any)
	if usage["contextWindow"] != 64000 {
		t.Fatalf("context window = %v", usage)
	}
}

func TestRPCUtilitySurfaceRejectsConversationManagement(t *testing.T) {
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	for _, typ := range []string{"new_session", "get_commands", "set_session_name"} {
		resp := s.handle(rpcCommand{Type: typ})
		if resp == nil || resp.Success || resp.Error != "Unknown command: "+typ {
			t.Fatalf("%s should be unimplemented, got %+v", typ, resp)
		}
	}
}

func TestRPCExtensionUIResponseIsSilent(t *testing.T) {
	s, _, log := newRPCTestRunner(t, &provider.Fake{}, nil)
	if resp := s.handle(rpcCommand{ID: strptr("e"), Type: "extension_ui_response"}); resp != nil {
		t.Fatalf("extension_ui_response should be silent, got %+v", resp)
	}
	if len(log.objects(t)) != 0 {
		t.Fatalf("extension_ui_response emitted output: %v", log.objects(t))
	}
}

func TestRPCRunRPCGuards(t *testing.T) {
	if err := runRPC(Options{Mode: "rpc", Messages: []string{"hi"}}, "test"); err == nil || !strings.Contains(err.Error(), "positional messages") {
		t.Fatalf("positional messages accepted: %v", err)
	}
	if err := runRPC(Options{Mode: "rpc", Resume: true}, "test"); err == nil || !strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("resume accepted: %v", err)
	}
}

func TestRPCSetModelAppliesPerModelThinkingDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-rpc-default")
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	meta := s.r.models.Providers["anthropic"]
	for i := range meta.Models {
		if meta.Models[i].ID == "rpc-test-model-2" {
			meta.Models[i].MaxTokens = 16384
		}
	}
	s.r.models.Providers["anthropic"] = meta
	s.r.settings.ModelThinkingLevels = map[string]string{"anthropic/rpc-test-model-2": "medium"}

	resp := s.handle(rpcCommand{ID: strptr("m1"), Type: "set_model", Provider: "anthropic", ModelID: "rpc-test-model-2"})
	if resp == nil || !resp.Success {
		t.Fatalf("set_model = %+v", resp)
	}
	if s.r.ov.Model != "rpc-test-model-2" || s.r.ov.Thinking != "medium" {
		t.Fatalf("switch selected %s at %q, want medium", s.r.ov.Model, s.r.ov.Thinking)
	}
}

func rpcSkill(t *testing.T, name, body string) skills.Skill {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	content := "---\nname: " + name + "\ndescription: rpc test skill\n---\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return skills.Skill{Name: name, Description: "rpc test skill", FilePath: path, BaseDir: dir}
}

func rpcLastUserText(t *testing.T, req provider.Request) string {
	t.Helper()
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		var text strings.Builder
		for _, block := range req.Messages[i].Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
			}
		}
		return text.String()
	}
	return ""
}

func rpcStoredUserTexts(store *session.Store) []string {
	var out []string
	for _, entry := range store.Entries() {
		if entry.Kind != "message" || entry.Message == nil || entry.Message.Message.User == nil {
			continue
		}
		if text, ok := entry.Message.Message.User.Content.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func TestRPCSkill(t *testing.T) {
	t.Run("known skill sends the exact block", func(t *testing.T) {
		skill := rpcSkill(t, "doer", "SKILL_BODY")
		fake := &provider.Fake{Responses: []*provider.Response{{Text: "done", StopReason: "stop"}}, TextDeltas: [][]string{{"done"}}}
		s, store, log := newRPCTestRunner(t, fake, []skills.Skill{skill})
		log.notify = make(chan struct{}, 1)
		resp := s.handle(rpcCommand{ID: strptr("s1"), Type: "prompt", Message: "/skill:doer run-it"})
		if resp == nil || !resp.Success || resp.Data.(map[string]any)["disposition"] != "started" {
			t.Fatalf("prompt response = %+v", resp)
		}
		log.waitObjects(t, "agent_settled", func(objs []map[string]any) bool {
			return len(objs) > 0 && objs[len(objs)-1]["type"] == "agent_settled"
		})
		want := "<skill name=\"doer\" location=\"" + skill.FilePath + "\">\n" +
			"References are relative to " + skill.BaseDir + ".\n\nSKILL_BODY\n</skill>\n\nrun-it"
		requests := fake.Requests()
		if len(requests) != 1 {
			t.Fatalf("provider calls = %d", len(requests))
		}
		if got := rpcLastUserText(t, requests[0]); got != want {
			t.Fatalf("provider content = %q, want %q", got, want)
		}
		stored := rpcStoredUserTexts(store)
		if len(stored) != 1 || stored[0] != want {
			t.Fatalf("stored user texts = %q", stored)
		}
	})

	t.Run("ordinary prompt is unchanged", func(t *testing.T) {
		fake := &provider.Fake{Responses: []*provider.Response{{Text: "done", StopReason: "stop"}}, TextDeltas: [][]string{{"done"}}}
		s, store, log := newRPCTestRunner(t, fake, []skills.Skill{rpcSkill(t, "doer", "SKILL_BODY")})
		log.notify = make(chan struct{}, 1)
		resp := s.handle(rpcCommand{ID: strptr("o1"), Type: "prompt", Message: "ordinary hello"})
		if resp == nil || !resp.Success {
			t.Fatalf("prompt response = %+v", resp)
		}
		log.waitObjects(t, "agent_settled", func(objs []map[string]any) bool {
			return len(objs) > 0 && objs[len(objs)-1]["type"] == "agent_settled"
		})
		if got := rpcLastUserText(t, fake.Requests()[0]); got != "ordinary hello" {
			t.Fatalf("provider content = %q", got)
		}
		if stored := rpcStoredUserTexts(store); len(stored) != 1 || stored[0] != "ordinary hello" {
			t.Fatalf("stored user texts = %q", stored)
		}
	})

	refusals := []struct {
		name  string
		build func(t *testing.T) ([]skills.Skill, string)
		cause string
	}{
		{
			name: "empty name",
			build: func(t *testing.T) ([]skills.Skill, string) {
				return nil, "/skill:"
			},
			cause: "not found",
		},
		{
			name: "unknown name",
			build: func(t *testing.T) ([]skills.Skill, string) {
				return []skills.Skill{rpcSkill(t, "doer", "BODY")}, "/skill:missing"
			},
			cause: "not found",
		},
		{
			name: "deleted file",
			build: func(t *testing.T) ([]skills.Skill, string) {
				skill := rpcSkill(t, "doer", "BODY")
				if err := os.Remove(skill.FilePath); err != nil {
					t.Fatal(err)
				}
				return []skills.Skill{skill}, "/skill:doer"
			},
			cause: "unreadable",
		},
		{
			name: "oversized file",
			build: func(t *testing.T) ([]skills.Skill, string) {
				return []skills.Skill{rpcSkill(t, "doer", strings.Repeat("x", 256*1024+1))}, "/skill:doer"
			},
			cause: "exceeds 256 KiB",
		},
	}
	for _, tc := range refusals {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			list, message := tc.build(t)
			fake := &provider.Fake{}
			s, store, log := newRPCTestRunner(t, fake, list)
			log.notify = make(chan struct{}, 1)
			before := len(store.Entries())
			pending := s.pendingCount()
			resp := s.handle(rpcCommand{ID: strptr("r1"), Type: "prompt", Message: message})
			if resp == nil || resp.Success || resp.Command != "prompt" || resp.ID == nil || *resp.ID != "r1" {
				t.Fatalf("refusal response = %+v", resp)
			}
			if !strings.Contains(resp.Error, tc.cause) {
				t.Fatalf("error = %q, want cause %q", resp.Error, tc.cause)
			}
			if len(store.Entries()) != before || s.pendingCount() != pending {
				t.Fatal("refused expansion changed the store or queue count")
			}
			if len(s.loop.PendingSteering()) != 0 || len(s.loop.PendingFollowUp()) != 0 {
				t.Fatal("refused expansion queued a message")
			}
			if fake.CallCount() != 0 {
				t.Fatalf("provider calls = %d, want none", fake.CallCount())
			}
			for _, obj := range log.objects(t) {
				if obj["type"] == "message_start" {
					t.Fatalf("refused expansion emitted a user message: %v", obj)
				}
			}
		})
	}

	t.Run("refused while busy", func(t *testing.T) {
		fake := &provider.Fake{}
		s, store, log := newRPCTestRunner(t, fake, nil)
		log.notify = make(chan struct{}, 1)
		s.mu.Lock()
		s.running = true
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		before := len(store.Entries())
		resp := s.handle(rpcCommand{ID: strptr("b1"), Type: "prompt", Message: "/skill:missing", StreamingBehavior: "steer"})
		if resp == nil || resp.Success || !strings.Contains(resp.Error, "not found") {
			t.Fatalf("busy refusal = %+v", resp)
		}
		if len(store.Entries()) != before || len(s.loop.PendingSteering()) != 0 || len(s.loop.PendingFollowUp()) != 0 {
			t.Fatal("busy refusal changed the store or queues")
		}
		if fake.CallCount() != 0 {
			t.Fatalf("provider calls = %d, want none", fake.CallCount())
		}
		for _, obj := range log.objects(t) {
			if obj["type"] == "message_start" {
				t.Fatalf("busy refusal emitted a user message: %v", obj)
			}
		}
	})
}
