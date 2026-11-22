package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestModelChoiceSurvivesRestartAndLeavesPsiSettingsAlone proves the
// per-route model selection is Psionic state, not a rewrite of Psi's
// settings file.
func TestModelChoiceSurvivesRestartAndLeavesPsiSettingsAlone(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	stateDir := t.TempDir()
	state, err := openState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	owner := "123456789012345678"
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}

	// A Psi agent directory with a settings file that must not change.
	agentDir := t.TempDir()
	settingsPath := filepath.Join(agentDir, "settings.json")
	original := []byte(`{"defaultProvider":"demo","defaultModel":"large-model"}`)
	if err := os.WriteFile(settingsPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	rt := testRuntime(t, workspace, state, &recordingTransport{}, &fakeFactory{}, owner)
	rt.models = testModels(t)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()
	if !chat.handleCommand(sourceTurn{message: dm}, "/model", "demo/small-model") {
		t.Fatal("model switch was not handled")
	}
	if got := chat.modelRef(); got != "demo/small-model" {
		t.Fatalf("effective model = %q", got)
	}

	// Restart: reopen the state store and build a fresh conversation for the
	// same route.
	state.close()
	reopened, err := openState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	rt2 := testRuntime(t, workspace, reopened, &recordingTransport{}, &fakeFactory{}, owner)
	rt2.models = testModels(t)
	chat2 := &conversation{key: "dm:one", rt: rt2}
	chat2.ctx, chat2.cancel = context.WithCancel(context.Background())
	defer chat2.close()
	if err := chat2.prepare(dm); err != nil {
		t.Fatal(err)
	}
	if got := chat2.modelRef(); got != "demo/small-model" {
		t.Fatalf("model after restart = %q", got)
	}

	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("Psi settings changed: %q", after)
	}
	if strings.Contains(string(after), "small-model") {
		t.Fatal("the per-route selection leaked into the Psi settings file")
	}
}
