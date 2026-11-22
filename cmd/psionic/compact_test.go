package main

import (
	"context"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
)

// fixedProviderFactory always returns the same provider set. One fake then
// serves the turns and the compaction summary requests in order.
type fixedProviderFactory struct {
	set *providerSet
}

func (f *fixedProviderFactory) build(_ *config.ModelsFile, _, _, _ string, _ string, _ int) (*providerSet, error) {
	return f.set, nil
}

// summaryProviderSet builds a fake with one response per expected call.
func summaryProviderSet(replies ...string) *providerSet {
	fake := &provider.Fake{}
	for _, reply := range replies {
		fake.Responses = append(fake.Responses, &provider.Response{
			Content: []provider.Block{{Type: "text", Text: reply}}, StopReason: "stop",
		})
	}
	return &providerSet{main: fake, model: provider.ModelMeta{
		ID: "test-model", API: "openai-completions", Provider: "demo",
		ContextWindow: 128000, MaxTokens: 4096, Custom: true,
	}}
}

// TestCompactCommandCompactsAConversationWithHistory proves the owner
// command runs Psi's compaction on a conversation that has work to compact.
func TestCompactCommandCompactsAConversationWithHistory(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	tr := &recordingTransport{}
	// Two turns, then a history summary and a turn-prefix summary.
	factory := &fixedProviderFactory{set: summaryProviderSet("first", "second", "summary", "prefix summary")}
	rt := testRuntime(t, workspace, state, tr, &fakeFactory{}, owner)
	rt.clients = factory.build
	// A small keep budget makes the short synthetic conversation eligible.
	rt.defaults.Compaction.KeepRecentTokens = 1
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	chat.handle(sourceTurn{message: dm, text: "hello", replyTo: dm.MessageID})
	chat.handle(sourceTurn{message: dm, text: "again", replyTo: dm.MessageID})
	if err := chat.prepare(dm); err != nil {
		t.Fatal(err)
	}
	before, err := chat.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if !chat.handleCommand(sourceTurn{message: dm, replyTo: dm.MessageID}, "/compact", "") {
		t.Fatal("compact was not handled")
	}
	replies := tr.messages()
	if len(replies) != 3 || replies[2] != "Compacted." {
		t.Fatalf("replies = %v", replies)
	}
	after, err := chat.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Flat) >= len(before.Flat) {
		t.Fatalf("the transcript did not shrink: %d -> %d", len(before.Flat), len(after.Flat))
	}
}

// TestCompactCommandFailureKeepsTheTranscript proves a failed compaction
// reports a safe category and leaves the conversation intact.
func TestCompactCommandFailureKeepsTheTranscript(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	tr := &recordingTransport{}
	// Two turns, then a failing summary request.
	factory := &fixedProviderFactory{set: summaryProviderSet("first", "second", "summary")}
	fake := factory.set.main.(*provider.Fake)
	rt := testRuntime(t, workspace, state, tr, &fakeFactory{}, owner)
	rt.clients = factory.build
	rt.defaults.Compaction.KeepRecentTokens = 1
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	chat.handle(sourceTurn{message: dm, text: "hello", replyTo: dm.MessageID})
	chat.handle(sourceTurn{message: dm, text: "again", replyTo: dm.MessageID})
	if err := chat.prepare(dm); err != nil {
		t.Fatal(err)
	}
	before, err := chat.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	// The third call is the summary request. It fails, so the compaction
	// cannot commit.
	fake.Errors = append(fake.Errors, nil, nil,
		&provider.ProviderError{Kind: provider.ErrContextLimit, Status: 400, Message: "synthetic failure"})
	if !chat.handleCommand(sourceTurn{message: dm, replyTo: dm.MessageID}, "/compact", "") {
		t.Fatal("compact was not handled")
	}
	replies := tr.messages()
	if len(replies) != 3 || replies[2] != "Compaction failed." {
		t.Fatalf("replies = %v", replies)
	}
	after, err := chat.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Flat) != len(before.Flat) {
		t.Fatalf("the transcript changed after a failed compaction: %d -> %d", len(before.Flat), len(after.Flat))
	}
	for _, message := range after.Flat {
		if message.Assistant == nil {
			continue
		}
		for _, block := range message.Assistant.Content {
			if block.Text != nil && strings.Contains(block.Text.Text, "synthetic failure") {
				t.Fatal("the failure detail leaked into the transcript")
			}
		}
	}
}
