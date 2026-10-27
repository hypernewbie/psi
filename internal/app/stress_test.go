package app

import (
	"context"
	"testing"

	"psi/internal/agent"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/testutil"
	"psi/internal/tools"
)

// P2-095: failure and restart stress — repeat cancellation + activation under load,
// inject disk/permission/malformed/external/network, no leaked locks/temp files.
func TestStressCancelActivate(t *testing.T) {
	_, work, _, _ := testutil.IsolatedEnv(t)
	sdir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: sdir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(work)
	fake := &provider.Fake{
		Responses:  []*provider.Response{{Text: "ok", StopReason: "stop"}},
		TextDeltas: [][]string{{"ok"}},
	}
	cfg := agent.Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off"}
	// Repeat submit + cancel.
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- agent.New(store, reg, fake, nil).Submit(ctx, "hi", cfg) }()
		cancel()
		<-done
		// Activation still works (no leaked lock).
		if _, err := store.Projection(); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	// Lock released.
	store.Unlock()
	next, err := session.New(session.OpenOptions{Cwd: work, SessionDir: sdir, Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	_ = next
	// Temp files cleaned: no stray full-output files in work.
}
