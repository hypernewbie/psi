package app

import (
	"reflect"
	"testing"

	"psi/internal/session"
	"psi/internal/terminal"
)

func TestActivationReplayRetainsCompletedMessageSource(t *testing.T) {
	dir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	userText := "A resumed user message has enough words to wrap at a narrow width and return to one line when the terminal gets wider."
	assistantText := "The completed assistant paragraph must also reflow from its original source after activation, without a new model response."
	if _, err := store.AppendMessage(agentMessageUser(userText)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "synthetic",
		StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: assistantText}}},
	}}); err != nil {
		t.Fatal(err)
	}
	activation, err := PrepareActivation(store)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []terminal.DocumentBlock
	complete := replayActivationDocument(func(block terminal.DocumentBlock) bool {
		blocks = append(blocks, block)
		return true
	}, activation, true, "")
	if !complete {
		t.Fatal("activation replay stopped")
	}
	var user, assistant string
	for _, block := range blocks {
		switch block.Kind {
		case terminal.DocumentUser:
			user = block.Text
		case terminal.DocumentAssistant:
			assistant = block.Text
		}
	}
	if user != userText || assistant != assistantText {
		t.Fatalf("activation ledger lost message source: user=%q assistant=%q", user, assistant)
	}
	width := 100
	var messageRows []string
	for _, block := range blocks {
		if block.Kind == terminal.DocumentUser || block.Kind == terminal.DocumentAssistant {
			messageRows = append(messageRows, terminal.FormatDocumentBlock(block, width, terminal.PresentationSnapshot{NoColor: true})...)
		}
	}
	want := append(terminal.FormatUserMessageWithDisplay(userText, width, true, terminal.DefaultDisplaySettings()),
		terminal.FormatAssistantMessageWithDisplay(assistantText, width, true, terminal.DefaultDisplaySettings())...)
	if !reflect.DeepEqual(messageRows, want) {
		t.Fatalf("source layouts differ\n got %#v\nwant %#v", messageRows, want)
	}
	if len(terminal.FormatDocument(blocks, width, terminal.PresentationSnapshot{NoColor: true})) == 0 {
		t.Fatal("activation document produced no rows")
	}
}
