package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
)

type recordingTransport struct {
	mu      sync.Mutex
	sent    []string
	history []historyMessage
}

func (r *recordingTransport) readMessages(context.Context, string, int, string, string) ([]historyMessage, error) {
	return r.history, nil
}

func (r *recordingTransport) sendMessage(_ context.Context, _, _, text string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, text)
	return "sent", nil
}

func (r *recordingTransport) fetchAttachment(_ context.Context, _ string, _ int64) ([]byte, error) {
	return nil, fmt.Errorf("attachment fetch is unavailable")
}

func (r *recordingTransport) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

type fakeFactory struct {
	mu      sync.Mutex
	fakes   []*provider.Fake
	replies []string
	// model overrides the metadata reported for the built provider set.
	model provider.ModelMeta
}

func (f *fakeFactory) build(_ *config.ModelsFile, _, _, _ string, _ string, _ int) (*providerSet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := "reply"
	if len(f.replies) > 0 {
		reply = f.replies[0]
		f.replies = f.replies[1:]
	}
	fake := &provider.Fake{Responses: []*provider.Response{{
		Content: []provider.Block{{Type: "text", Text: reply}}, StopReason: "stop",
	}}}
	f.fakes = append(f.fakes, fake)
	model := f.model
	if model.ID == "" {
		model = testModelMeta()
	}
	return &providerSet{main: fake, model: model}, nil
}

func (f *fakeFactory) last() *provider.Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fakes[len(f.fakes)-1]
}

func testModelMeta() provider.ModelMeta {
	return provider.ModelMeta{ID: "test-model", API: "openai-completions", Provider: "demo", ContextWindow: 128000, MaxTokens: 4096, Custom: true}
}

func testRuntime(t *testing.T, workspace string, state *stateStore, tr transport, factory *fakeFactory, owner string) *runtime {
	t.Helper()
	body := `{
  agents: { defaults: { workspace: ` + quoteJSON(workspace) + `, model: "demo/large-model" } },
  channels: { discord: {
    token: "test-token", dmPolicy: "allowlist", allowFrom: ["123456789012345678"],
    groupPolicy: "open", historyLimit: 0,
  } },
  commands: { ownerAllowFrom: [` + quoteJSON(owner) + `] },
  session: { reset: { mode: "none" } },
}`
	settings, err := decodeSettings([]byte(body), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	agentDir := t.TempDir()
	rt := newRuntime(settings, selectedAgent{Workspace: workspace, Model: "demo/large-model"}, nil, agentDir, filepath.Join(agentDir, "auth.json"), state, configDefaults(), tr, log.New(io.Discard, "", 0))
	rt.clients = factory.build
	return rt
}

func quoteJSON(value string) string {
	return `"` + strings.ReplaceAll(value, `\`, `\\`) + `"`
}

func TestConversationRestartAndReset(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}

	tr := &recordingTransport{}
	first := &fakeFactory{replies: []string{"first"}}
	rt1 := testRuntime(t, workspace, state, tr, first, owner)
	chat1 := &conversation{key: "dm:one", rt: rt1}
	chat1.ctx, chat1.cancel = context.WithCancel(context.Background())
	chat1.handle(sourceTurn{message: dm, text: "hello", replyTo: dm.MessageID, history: nil})
	if got := tr.messages(); len(got) != 1 || got[0] != "first" {
		t.Fatalf("sent = %v", got)
	}
	chat1.close()

	second := &fakeFactory{replies: []string{"second"}}
	rt2 := testRuntime(t, workspace, state, tr, second, owner)
	chat2 := &conversation{key: "dm:one", rt: rt2}
	chat2.ctx, chat2.cancel = context.WithCancel(context.Background())
	chat2.handle(sourceTurn{message: dm, text: "again", replyTo: dm.MessageID})
	requests := second.last().Requests()
	if len(requests) == 0 {
		t.Fatal("no request after restart")
	}
	var transcript string
	for _, message := range requests[0].Messages {
		for _, block := range message.Content {
			transcript += block.Text
		}
	}
	if !strings.Contains(transcript, "hello") || !strings.Contains(transcript, "first") {
		t.Fatalf("restart lost history: %q", transcript)
	}
	if got := tr.messages(); len(got) != 2 || got[1] != "second" {
		t.Fatalf("sent = %v", got)
	}

	if !chat2.handleCommand(sourceTurn{message: dm}, "/reset", "") {
		t.Fatal("reset was not handled")
	}
	third := &fakeFactory{replies: []string{"third"}}
	rt2.clients = third.build
	chat3 := &conversation{key: "dm:one", rt: rt2}
	chat3.ctx, chat3.cancel = context.WithCancel(context.Background())
	chat3.handle(sourceTurn{message: dm, text: "after reset"})
	requests = third.last().Requests()
	if len(requests) == 0 {
		t.Fatal("no request after reset")
	}
	transcript = ""
	for _, message := range requests[0].Messages {
		for _, block := range message.Content {
			transcript += block.Text
		}
	}
	if strings.Contains(transcript, "hello") {
		t.Fatalf("reset kept old history: %q", transcript)
	}
	chat3.close()
}

func TestSeparateConversationsHaveSeparateTranscripts(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	tr := &recordingTransport{}
	factory := &fakeFactory{replies: []string{"one", "two"}}
	rt := testRuntime(t, workspace, state, tr, factory, "123456789012345678")
	dm := func(author string) inboundMessage {
		return inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: author, IsDM: true}
	}
	first := &conversation{key: "dm:one", rt: rt}
	first.ctx, first.cancel = context.WithCancel(context.Background())
	first.handle(sourceTurn{message: dm("123456789012345678"), text: "first secret"})
	first.close()
	second := &conversation{key: "dm:two", rt: rt}
	second.ctx, second.cancel = context.WithCancel(context.Background())
	second.handle(sourceTurn{message: dm("223456789012345678"), text: "second question"})
	defer second.close()
	requests := factory.last().Requests()
	var transcript string
	for _, message := range requests[0].Messages {
		for _, block := range message.Content {
			transcript += block.Text
		}
	}
	if strings.Contains(transcript, "first secret") {
		t.Fatalf("conversation crossed: %q", transcript)
	}
}

type scriptedFactory struct {
	scripts [][]*provider.Response
}

func (f *scriptedFactory) build(_ *config.ModelsFile, _, _, _ string, _ string, _ int) (*providerSet, error) {
	fake := &provider.Fake{Responses: f.scripts[0]}
	f.scripts = f.scripts[1:]
	return &providerSet{main: fake, model: testModelMeta()}, nil
}

func TestMessageToolModeFlushesOnlyTheStagedReply(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	tr := &recordingTransport{}
	factory := &scriptedFactory{scripts: [][]*provider.Response{{
		{Content: []provider.Block{{Type: "toolCall", ID: "call-1", Name: "message", Arguments: map[string]any{"action": "send", "text": "visible"}}}, StopReason: "toolUse"},
		{Content: []provider.Block{{Type: "text", Text: "private"}}, StopReason: "stop"},
	}}}
	body := `{
  agents: { defaults: { workspace: ` + quoteJSON(workspace) + `, model: "demo/large-model" } },
  channels: { discord: { token: "test-token", dmPolicy: "allowlist", allowFrom: ["123456789012345678"], historyLimit: 0 } },
  messages: { visibleReplies: "message_tool" },
  commands: { ownerAllowFrom: ["123456789012345678"] },
  session: { reset: { mode: "none" } },
}`
	settings, err := decodeSettings([]byte(body), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	agentDir := t.TempDir()
	rt := newRuntime(settings, selectedAgent{Workspace: workspace, Model: "demo/large-model"}, nil, agentDir, filepath.Join(agentDir, "auth.json"), state, configDefaults(), tr, log.New(io.Discard, "", 0))
	rt.clients = factory.build
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: "123456789012345678", IsDM: true}
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()
	chat.handle(sourceTurn{message: dm, text: "question"})
	if got := tr.messages(); len(got) != 1 || got[0] != "visible" {
		t.Fatalf("sent = %v", got)
	}
}

func TestNoReplySendsNothing(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	tr := &recordingTransport{}
	factory := &fakeFactory{replies: []string{"NO_REPLY"}}
	rt := testRuntime(t, workspace, state, tr, factory, "123456789012345678")
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: "123456789012345678", IsDM: true}
	chat := &conversation{key: "dm:none", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()
	chat.handle(sourceTurn{message: dm, text: "quiet"})
	if got := tr.messages(); len(got) != 0 {
		t.Fatalf("sent = %v", got)
	}
}

func TestGroupReplyModeDoesNotChangeDirectMessages(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	tr := &recordingTransport{}
	factory := &fakeFactory{replies: []string{"direct", "group"}}
	body := `{
  agents: { defaults: { workspace: ` + quoteJSON(workspace) + `, model: "demo/large-model" } },
  channels: { discord: { token: "test-token", dmPolicy: "allowlist", allowFrom: ["123456789012345678"], groupPolicy: "open", historyLimit: 0 } },
  messages: { groupChat: { visibleReplies: "message_tool" } },
  commands: { ownerAllowFrom: ["123456789012345678"] },
  session: { reset: { mode: "none" } },
}`
	settings, err := decodeSettings([]byte(body), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	agentDir := t.TempDir()
	rt := newRuntime(settings, selectedAgent{Workspace: workspace, Model: "demo/large-model"}, nil, agentDir, filepath.Join(agentDir, "auth.json"), state, configDefaults(), tr, log.New(io.Discard, "", 0))
	rt.clients = factory.build
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: "123456789012345678", IsDM: true}
	direct := &conversation{key: "dm:direct", rt: rt}
	direct.ctx, direct.cancel = context.WithCancel(context.Background())
	direct.handle(sourceTurn{message: dm, text: "hello"})
	direct.close()
	if got := tr.messages(); len(got) != 1 || got[0] != "direct" {
		t.Fatalf("direct sent = %v", got)
	}
	group := inboundMessage{MessageID: "323456789012345678", GuildID: "423456789012345678", ChannelID: "523456789012345678", AuthorID: "123456789012345678"}
	room := &conversation{key: "guild:room", rt: rt}
	room.ctx, room.cancel = context.WithCancel(context.Background())
	defer room.close()
	room.handle(sourceTurn{message: group, text: "hello room"})
	if got := tr.messages(); len(got) != 1 {
		t.Fatalf("group reply mode sent the private answer: %v", got)
	}
}

func TestStaleRouteStartsFreshAfterRestart(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	old := time.Now().Add(-2 * time.Hour)
	if err := state.saveRoute("dm:stale", routeState{SessionID: "old", StartedAt: old.UnixMilli(), LastInteraction: old.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	factory := &fakeFactory{}
	rt := testRuntime(t, workspace, state, &recordingTransport{}, factory, "123456789012345678")
	rt.settings.Session = &sessionSettings{Reset: &resetSettings{Mode: "idle", IdleMinutes: intPtr(1)}}
	chat := &conversation{key: "dm:stale", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()
	if err := chat.prepare(inboundMessage{IsDM: true, ChannelID: "323456789012345678", AuthorID: "123456789012345678"}); err != nil {
		t.Fatal(err)
	}
	if chat.store.Header().ID == "old" {
		t.Fatal("stale session was reopened")
	}
}

func TestWorkspaceChangeStartsAFreshTranscript(t *testing.T) {
	firstWorkspace := writeWorkspace(t, map[string]string{"AGENTS.md": "first"})
	secondWorkspace := writeWorkspace(t, map[string]string{"AGENTS.md": "second"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: "123456789012345678", IsDM: true}
	first := &conversation{key: "dm:one", rt: testRuntime(t, firstWorkspace, state, &recordingTransport{}, &fakeFactory{}, "123456789012345678")}
	first.ctx, first.cancel = context.WithCancel(context.Background())
	if err := first.prepare(dm); err != nil {
		t.Fatal(err)
	}
	oldID := first.store.Header().ID
	first.retireLocked()
	second := &conversation{key: "dm:one", rt: testRuntime(t, secondWorkspace, state, &recordingTransport{}, &fakeFactory{}, "123456789012345678")}
	second.ctx, second.cancel = context.WithCancel(context.Background())
	defer second.close()
	if err := second.prepare(dm); err != nil {
		t.Fatal(err)
	}
	if second.store.Header().ID == oldID {
		t.Fatal("workspace change reused the old transcript")
	}
}

func TestToolScopeChangeStartsAFreshTranscript(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: "123456789012345678", IsDM: true}
	first := &conversation{key: "dm:one", rt: testRuntime(t, workspace, state, &recordingTransport{}, &fakeFactory{}, "123456789012345678")}
	first.rt.agent.Tools = toolPolicy{Deny: []string{"message"}}
	first.ctx, first.cancel = context.WithCancel(context.Background())
	if err := first.prepare(dm); err != nil {
		t.Fatal(err)
	}
	oldID := first.store.Header().ID
	first.retireLocked()
	second := &conversation{key: "dm:one", rt: testRuntime(t, workspace, state, &recordingTransport{}, &fakeFactory{}, "123456789012345678")}
	second.rt.agent.Tools = toolPolicy{}
	second.ctx, second.cancel = context.WithCancel(context.Background())
	defer second.close()
	if err := second.prepare(dm); err != nil {
		t.Fatal(err)
	}
	if second.store.Header().ID == oldID {
		t.Fatal("tool-scope change reused the old transcript")
	}
}

func TestPrivateScopeChangeStartsAFreshTranscript(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules", "MEMORY.md": "private"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	tr := &recordingTransport{}
	factory := &fakeFactory{replies: []string{"private", "shared"}}
	rt := testRuntime(t, workspace, state, tr, factory, owner)
	chat := &conversation{key: "dm:scope", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	chat.handle(sourceTurn{message: dm, text: "hello", replyTo: dm.MessageID})
	firstID := chat.store.Header().ID
	rt.policy.ownerAllowFrom = map[string]bool{}
	chat.handle(sourceTurn{message: dm, text: "again", replyTo: dm.MessageID})
	if chat.store.Header().ID == firstID {
		t.Fatal("a now-shared route reused its private transcript")
	}
	chat.close()
}

func TestUnauthorizedCommandNeverReachesTheModel(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	tr := &recordingTransport{}
	factory := &fakeFactory{}
	rt := testRuntime(t, workspace, state, tr, factory, "999999999012345678")
	chat := &conversation{key: "dm:unauthorized", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	chat.handle(sourceTurn{message: inboundMessage{
		MessageID: "223456789012345678", ChannelID: "323456789012345678",
		AuthorID: "123456789012345678", IsDM: true,
	}, text: "/status"})
	if len(factory.fakes) != 0 || len(tr.messages()) != 0 {
		t.Fatalf("fakes = %d, sent = %v", len(factory.fakes), tr.messages())
	}
	chat.close()
}

// modelRecordingFactory remembers the model reference of each build.
type modelRecordingFactory struct {
	inner *fakeFactory
	mu    sync.Mutex
	refs  []string
}

func (f *modelRecordingFactory) build(models *config.ModelsFile, agentDir, modelRef, compactionRef, sessionID string, maxRetries int) (*providerSet, error) {
	f.mu.Lock()
	f.refs = append(f.refs, modelRef)
	f.mu.Unlock()
	return f.inner.build(models, agentDir, modelRef, compactionRef, sessionID, maxRetries)
}

func (f *modelRecordingFactory) lastModelRef() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.refs) == 0 {
		return ""
	}
	return f.refs[len(f.refs)-1]
}
