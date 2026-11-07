package app

import (
	"strings"
	"testing"

	"psi/internal/session"
	"psi/internal/terminal"
)

// TestActivationReplayKeepsThinkingBeforeText covers the resume path: the
// reasoning block is replayed as its own ledger entry before the answer, and
// the hidden display collapses it to Pi's label.
func TestActivationReplayKeepsThinkingBeforeText(t *testing.T) {
	dir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "synthetic",
		StopReason: "stop", Content: []session.ContentBlock{
			{Type: "thinking", Thinking: &session.ThinkingContent{Type: "thinking", Thinking: "weigh options"}},
			{Type: "text", Text: &session.TextContent{Type: "text", Text: "the answer"}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	activation, err := PrepareActivation(store)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []terminal.DocumentBlock
	if !replayActivationDocument(func(block terminal.DocumentBlock) bool {
		blocks = append(blocks, block)
		return true
	}, activation, true, "") {
		t.Fatal("activation replay stopped")
	}
	var thinking, assistant string
	order := -1
	for i, block := range blocks {
		switch block.Kind {
		case terminal.DocumentThinking:
			thinking = block.Text
			order = i
		case terminal.DocumentAssistant:
			assistant = block.Text
			if order > i {
				t.Fatal("the answer was replayed before the reasoning")
			}
		}
	}
	if thinking != "weigh options" {
		t.Fatalf("replayed thinking = %q", thinking)
	}
	if assistant != "the answer" {
		t.Fatalf("replayed answer = %q", assistant)
	}
	visible := strings.Join(terminal.FormatDocument(blocks, 80, terminal.PresentationSnapshot{NoColor: true}), "\n")
	if !strings.Contains(visible, "weigh options") || !strings.Contains(visible, "the answer") {
		t.Fatalf("replay document lost content:\n%s", visible)
	}
	hidden := strings.Join(terminal.FormatDocument(blocks, 80, terminal.PresentationSnapshot{NoColor: true, Display: terminal.DisplaySettings{HideThinking: true}}), "\n")
	if strings.Contains(hidden, "weigh options") {
		t.Fatalf("hidden replay leaked the reasoning:\n%s", hidden)
	}
	if !strings.Contains(hidden, "Thinking...") {
		t.Fatalf("hidden replay lost the label:\n%s", hidden)
	}
}

// TestReplayTimelineKeepsHidingReasoning guards the startup/replay behavior:
// the conversation timeline shows the answer, not the reasoning.
func TestReplayTimelineKeepsHidingReasoning(t *testing.T) {
	dir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "synthetic",
		StopReason: "stop", Content: []session.ContentBlock{
			{Type: "thinking", Thinking: &session.ThinkingContent{Type: "thinking", Thinking: "weigh options"}},
			{Type: "text", Text: &session.TextContent{Type: "text", Text: "the answer"}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	complete, err := store.Timeline(false, 0, func(item session.TranscriptItem) bool {
		if item.Kind == session.KindAssistant {
			out.WriteString(item.Text)
			out.WriteString("\n")
		}
		return true
	})
	if err != nil || !complete {
		t.Fatalf("replay timeline failed: complete=%v err=%v", complete, err)
	}
	if strings.Contains(out.String(), "weigh options") {
		t.Fatalf("replay timeline leaked the reasoning:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "the answer") {
		t.Fatalf("replay timeline lost the answer:\n%s", out.String())
	}
}
