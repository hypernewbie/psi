package main

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/tools"
)

func TestOfflineTurnThroughPsi(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{
		"AGENTS.md": "Answer with the workspace rule.",
		"SOUL.md":   "Be brief.",
	})
	dir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: workspace, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Unlock()
	fake := &provider.Fake{Responses: []*provider.Response{{
		Content:    []provider.Block{{Type: "text", Text: "answer"}},
		StopReason: "stop",
	}}}
	loop := agent.New(store, tools.NewRegistry(workspace), fake, nil)
	files, err := loadWorkspaceFiles(workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	sections := buildPromptSections(selectedAgent{}, files, []string{"message"}, nil, "automatic")
	rt := &runtime{defaults: config.Settings{Compaction: config.CompactionConfig{
		Enabled: true, ReserveTokens: 1024, KeepRecentTokens: 1024,
	}}}
	clients := &providerSet{main: fake, model: provider.ModelMeta{
		ID: "test-model", API: "openai-completions", Provider: "demo",
		ContextWindow: 128000, MaxTokens: 4096, Custom: true,
	}}
	if err := loop.Submit(context.Background(), "hello", rt.agentConfig(clients, toolSet{}, sections)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := assistantTextAfter(store, 0); got != "answer" {
		t.Fatalf("answer = %q", got)
	}
	full, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if got := assistantTextAfter(store, len(full.Flat)); got != "" {
		t.Fatalf("answer before the run boundary = %q", got)
	}
	if got := assistantTextAfter(store, len(full.Flat)+1); got != "" {
		t.Fatalf("answer after a compacted boundary = %q", got)
	}
	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	system := requests[0].System
	var user string
	for _, message := range requests[0].Messages {
		if message.Role != "user" {
			continue
		}
		for _, block := range message.Content {
			user += block.Text
		}
	}
	if !strings.Contains(system, "Answer with the workspace rule.") || !strings.Contains(system, "Be brief.") {
		t.Fatalf("system = %q", system)
	}
	if !strings.Contains(user, "hello") {
		t.Fatalf("user = %q", user)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, message := range projection.Flat {
		roles = append(roles, message.Role)
	}
	if !containsString(roles, "user") || !containsString(roles, "assistant") {
		t.Fatalf("roles = %v", roles)
	}
}

func TestTurnContentQuotesHistory(t *testing.T) {
	text := turnContent(sourceTurn{text: "new", history: []historyMessage{{ID: "1", Author: "Aster", Text: "old"}}})
	if !strings.Contains(text, "<conversation_history>") || !strings.HasPrefix(strings.TrimSpace(text[strings.LastIndex(text, "</conversation_history>"):]), "</conversation_history>") {
		t.Fatalf("content = %q", text)
	}
	text = turnContent(sourceTurn{
		text:      "new",
		reference: &historyMessage{ID: "2", Author: "Aster", Text: "quoted"},
		history:   []historyMessage{{ID: "1", Author: "Aster", Text: "old"}},
	})
	if strings.Index(text, "<reply_context>") > strings.Index(text, "<conversation_history>") {
		t.Fatalf("reference order = %q", text)
	}
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func TestConversationQueueIsBoundedAndSerialized(t *testing.T) {
	chat := &conversation{turns: make(chan sourceTurn, 1)}
	if !chat.enqueue(sourceTurn{text: "one"}) {
		t.Fatal("first turn rejected")
	}
	if chat.enqueue(sourceTurn{text: "two"}) {
		t.Fatal("queue accepted beyond its bound")
	}
	<-chat.turns
}

func TestConversationStopCallsTheActiveCancel(t *testing.T) {
	canceled := false
	chat := &conversation{}
	chat.runStop = func() { canceled = true }
	if !chat.stop() {
		t.Fatal("active run was not stopped")
	}
	if !canceled {
		t.Fatal("cancel function was not called")
	}
}

func TestCommandTextRequiresExactTrigger(t *testing.T) {
	for _, text := range []string{"/new", " /RESET ", "/stop", "/status", "/compact", "/model", "/model demo/large-model"} {
		if _, ok := commandText(text); !ok {
			t.Fatalf("%q not recognized", text)
		}
	}
	for _, text := range []string{"", "/new now", "please /reset", "status", "/model a b c"} {
		if _, ok := commandText(text); ok {
			t.Fatalf("%q recognized as a command", text)
		}
	}
	if command, arg, ok := commandWithArg("/model demo/small-model"); !ok || command != "/model" || arg != "demo/small-model" {
		t.Fatalf("/model arg = %q %q %v", command, arg, ok)
	}
}

func TestResetOverridePrefersExactChannel(t *testing.T) {
	settings := mustSettings(t, `{
  channels: { discord: {} },
  session: { resetByChannel: {
    discord: { mode: "daily", atHour: 6 },
    "423456789012345678": { mode: "idle", idleMinutes: 30 },
  } },
}`)
	agentDir := t.TempDir()
	rt := newRuntime(settings, selectedAgent{Workspace: t.TempDir()}, nil, agentDir, filepath.Join(agentDir, "auth.json"), nil, configDefaults(), nil, log.New(io.Discard, "", 0))
	if got := rt.resetOverride("423456789012345678"); got == nil || got.Mode != "idle" || got.IdleMinutes == nil || *got.IdleMinutes != 30 {
		t.Fatalf("exact override = %+v", got)
	}
	if got := rt.resetOverride("523456789012345678"); got == nil || got.Mode != "daily" {
		t.Fatalf("provider override = %+v", got)
	}
}

func TestResetRefusesAnActiveRun(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	tr := &recordingTransport{}
	rt := testRuntime(t, workspace, state, tr, &fakeFactory{}, "123456789012345678")
	chat := &conversation{key: "dm:one", rt: rt}
	chat.running = true
	turn := sourceTurn{message: inboundMessage{
		MessageID: "223456789012345678", ChannelID: "323456789012345678",
		AuthorID: "123456789012345678", IsDM: true,
	}}
	if !chat.handleCommand(turn, "/reset", "") {
		t.Fatal("reset was not handled")
	}
	if got := tr.messages(); len(got) != 1 || !strings.Contains(got[0], "Busy") {
		t.Fatalf("replies = %v", got)
	}
}

func TestModelCommandSwitchesPerRouteModel(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	models := testModels(t)

	factory := &modelRecordingFactory{inner: &fakeFactory{}}
	rt := testRuntime(t, workspace, state, &recordingTransport{}, factory.inner, owner)
	rt.models = models
	rt.clients = factory.build
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	if !chat.handleCommand(sourceTurn{message: dm}, "/model", "demo/small-model") {
		t.Fatal("model switch was not handled")
	}
	if got := chat.modelRef(); got != "demo/small-model" {
		t.Fatalf("effective model = %q", got)
	}
	chat.handle(sourceTurn{message: dm, text: "hello", replyTo: dm.MessageID})
	if got := factory.lastModelRef(); got != "demo/small-model" {
		t.Fatalf("turn model ref = %q", got)
	}
}

func TestModelCommandRejectsUnknownModel(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	rt := testRuntime(t, workspace, state, &recordingTransport{}, &fakeFactory{}, owner)
	rt.models = testModels(t)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()
	if !chat.handleCommand(sourceTurn{message: dm}, "/model", "demo/private-model-identifier") {
		t.Fatal("model switch was not handled")
	}
	if chat.modelRef() != rt.agent.Model {
		t.Fatal("unknown model changed the effective model")
	}
}

func TestModelListShowsCurrentAndIsBounded(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	rt := testRuntime(t, workspace, nil, &recordingTransport{}, &fakeFactory{}, "123456789012345678")
	rt.models = testModels(t)
	chat := &conversation{key: "dm:one", rt: rt}
	list := chat.modelList()
	if !strings.Contains(list, "demo/large-model  (current)") {
		t.Fatalf("list = %q", list)
	}
	if strings.Contains(list, "test-key") {
		t.Fatalf("list leaked a credential: %q", list)
	}
}

func TestCompactCommandReportsNoWorkOnAFreshConversation(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	tr := &recordingTransport{}
	rt := testRuntime(t, workspace, state, tr, &fakeFactory{}, owner)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()
	if !chat.handleCommand(sourceTurn{message: dm, replyTo: dm.MessageID}, "/compact", "") {
		t.Fatal("compact was not handled")
	}
	if got := tr.messages(); len(got) != 1 || !strings.Contains(got[0], "Nothing to compact") {
		t.Fatalf("replies = %v", got)
	}
}
