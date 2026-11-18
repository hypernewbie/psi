//go:build unix

package app

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"psi/internal/config"
	pibridgepb "psi/internal/pibridge/pb"
)

// A browser launcher may start children that keep its stdout/stderr open.
// Sending both directly to the null device avoids pipes that Go must drain
// before the login worker can continue.
func TestOpenAILoginBrowserDoesNotInheritOutputPipes(t *testing.T) {
	name := "xdg-open"
	if goruntime.GOOS == "darwin" {
		name = "open"
	}
	dir := t.TempDir()
	const fake = `#!/bin/sh
[ -c /dev/fd/1 ] && [ -c /dev/fd/2 ] && [ "$1" = 'https://example.invalid/sign-in' ]
`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := openOpenAILoginBrowser(context.Background(), "https://example.invalid/sign-in"); err != nil {
		t.Fatalf("fake browser launcher did not get null-device output: %v", err)
	}
}

// While authenticating, bridge mutations are refused before preparation
// starts, and read-only queries stay available. L6.
func TestOpenAILoginBridgeRefusesMutations(t *testing.T) {
	r := testRuntime(t)
	addModel(r, "anthropic", config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-login-bridge")
	f, h := startBridgeRuntime(t, r)
	r.state = StateAuthenticating
	before := countEntries(t, r)

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "req-login-set", Provider: "anthropic", Model: "m2"},
	}}
	h.handle(nextRequest(t, r.bridge), true)
	if h.busy() {
		t.Fatal("model switch started a preparation during login")
	}
	ack := f.waitFrame(t, "login refusal", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetModelAck() != nil
	}).GetSetModelAck()
	if ack.GetOk() {
		t.Fatalf("switch accepted during login: %+v", ack)
	}
	if r.ov.Model != "m" || countEntries(t, r) != before {
		t.Fatal("refused switch changed the runtime or the session")
	}

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_ModelsRequest{
		ModelsRequest: &pibridgepb.ModelsRequest{RequestId: "req-login-list"},
	}}
	h.handle(nextRequest(t, r.bridge), true)
	list := f.waitFrame(t, "login list", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetModelsResponse() != nil
	}).GetModelsResponse()
	if list.GetRequestId() != "req-login-list" || list.GetUnavailable() {
		t.Fatalf("read-only list refused during login: %+v", list)
	}
}

// Bounded clean-startup check on a real PTY: an unknown login argument is
// a local usage error, and the session stores no login message.
func TestOpenAILoginPTYLocalCommand(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session-dir", sessionDir)
	q.prompt()
	q.write("/login bogus\r")
	q.wait("unknown provider")
	q.prompt()
	q.write("/quit\r")
	q.finish()
}
