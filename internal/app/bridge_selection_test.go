//go:build !windows

package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/session"
)

// selectionFake streams a scripted turn through a hook, so a test can publish
// a newer selection while the request is in flight.
type selectionFake struct {
	inner  *provider.Fake
	onSend func()
}

func (p *selectionFake) Send(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	if p.onSend != nil {
		p.onSend()
	}
	return p.inner.Send(ctx, req, onDelta)
}

func (p *selectionFake) CloseIdle() {}

// selectionModels registers the second selectable model this test switches to.
func selectionModels(r *runtime) provider.ModelMeta {
	meta := provider.ModelMeta{ID: "m2", API: "anthropic-messages", Reasoning: true, MaxTokens: 16384}
	addModel(r, "anthropic", config.ModelMeta{ID: meta.ID, API: meta.API, Reasoning: true, MaxTokens: meta.MaxTokens})
	return meta
}

func liveSelection(fake provider.Provider, meta provider.ModelMeta, thinking string) agent.LiveSettings {
	return agent.LiveSettings{
		Provider:  fake,
		Overrides: config.StartupOverrides{Provider: "anthropic", Model: meta.ID},
		Model:     meta,
		Thinking:  thinking,
	}
}

// lastAssistantIdentity returns the provider and model recorded on the last
// assistant entry, which is the identity of the request that produced it.
func lastAssistantIdentity(t *testing.T, path []*session.Entry) (string, string) {
	t.Helper()
	for i := len(path) - 1; i >= 0; i-- {
		entry := path[i]
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.Assistant != nil {
			a := entry.Message.Message.Assistant
			return a.Provider, a.Model
		}
	}
	t.Fatal("session has no assistant entry")
	return "", ""
}

// modelChanges lists the durable model_change records in order.
func modelChanges(t *testing.T, path []*session.Entry) []string {
	t.Helper()
	var changes []string
	for _, entry := range path {
		if entry.Kind == "model_change" {
			changes = append(changes, entry.ModelChange.Provider+"/"+entry.ModelChange.ModelID)
		}
	}
	return changes
}

// TestBridgeLiveSettingsPreserveDurableSelection proves that a model change
// committed during a run survives that run's last response, even when no next
// request could repair the metadata. The response keeps its own request
// identity; the saved selection stays the newest one.
func TestBridgeLiveSettingsPreserveDurableSelection(t *testing.T) {
	t.Run("turn-boundary sync", func(t *testing.T) {
		r := testRuntime(t)
		next := selectionModels(r)
		fake := &provider.Fake{}
		// The submitted configuration is already behind the published
		// selection: the guarded sync must record m2, never the snapshot.
		cfg := r.cfg
		cfg.LiveSettings = func() agent.LiveSettings { return liveSelection(fake, next, "high") }
		cfg.SelectionMu = &r.live.selection
		fake.Responses = []*provider.Response{{Text: "final", StopReason: "stop"}}
		if err := agent.New(r.store, r.reg, fake, nil).Submit(context.Background(), "question", cfg); err != nil {
			t.Fatal(err)
		}
		path, err := r.store.ActivePath()
		if err != nil {
			t.Fatal(err)
		}
		changes := modelChanges(t, path)
		if len(changes) != 1 || changes[0] != "anthropic/m2" {
			t.Fatalf("model_change records = %v, want one anthropic/m2", changes)
		}
		if user := fake.Requests()[0]; user.Model.ID != "m2" {
			t.Fatalf("request model = %q, want the refreshed m2", user.Model.ID)
		}
	})

	t.Run("final turn", func(t *testing.T) {
		r := testRuntime(t)
		next := selectionModels(r)
		fake := &provider.Fake{}
		switched := new(atomic.Bool)
		hook := &selectionFake{inner: fake, onSend: func() { switched.Store(true) }}
		submitted := r.cfg.Model
		cfg := r.cfg
		cfg.LiveSettings = func() agent.LiveSettings {
			if switched.Load() {
				return liveSelection(hook, next, "high")
			}
			return liveSelection(hook, submitted, "off")
		}
		cfg.SelectionMu = &r.live.selection
		fake.Responses = []*provider.Response{{Text: "final", StopReason: "stop"}}
		if err := agent.New(r.store, r.reg, hook, nil).Submit(context.Background(), "question", cfg); err != nil {
			t.Fatal(err)
		}
		if got := fake.CallCount(); got != 1 {
			t.Fatalf("provider requests = %d, want 1 (no repair request)", got)
		}
		if req := fake.Requests()[0]; req.Model.ID != "m" {
			t.Fatalf("request model = %q, want the submitted m", req.Model.ID)
		}
		path, err := r.store.ActivePath()
		if err != nil {
			t.Fatal(err)
		}
		providerID, modelID := lastAssistantIdentity(t, path)
		if providerID != "anthropic" || modelID != "m" {
			t.Fatalf("assistant identity = %s/%s, want the request's anthropic/m", providerID, modelID)
		}
		changes := modelChanges(t, path)
		if len(changes) == 0 || changes[len(changes)-1] != "anthropic/m2" {
			t.Fatalf("model_change records = %v, want the newest anthropic/m2 last", changes)
		}
		reopened, err := session.OpenFile(r.store.File(), r.store.Cwd(), false)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := reopened.ModelState()
		if err != nil {
			t.Fatal(err)
		}
		if saved.Provider != "anthropic" || saved.ModelID != "m2" || saved.ThinkingLevel != "high" {
			t.Fatalf("saved selection = %s/%s thinking %q, want anthropic/m2 thinking high",
				saved.Provider, saved.ModelID, saved.ThinkingLevel)
		}
	})

	t.Run("interrupted output", func(t *testing.T) {
		r := testRuntime(t)
		next := selectionModels(r)
		fake := &provider.Fake{TextDeltas: [][]string{{"PARTIAL"}}, Errors: []error{errors.New("stream reset")}}
		switched := new(atomic.Bool)
		hook := &selectionFake{inner: fake, onSend: func() { switched.Store(true) }}
		submitted := r.cfg.Model
		cfg := r.cfg
		cfg.LiveSettings = func() agent.LiveSettings {
			if switched.Load() {
				return liveSelection(hook, next, "high")
			}
			return liveSelection(hook, submitted, "off")
		}
		cfg.SelectionMu = &r.live.selection
		err := agent.New(r.store, r.reg, hook, nil).Submit(context.Background(), "question", cfg)
		if err == nil || !strings.Contains(err.Error(), "stream reset") {
			t.Fatalf("run error = %v, want the provider failure", err)
		}
		path, err := r.store.ActivePath()
		if err != nil {
			t.Fatal(err)
		}
		providerID, modelID := lastAssistantIdentity(t, path)
		if providerID != "anthropic" || modelID != "m" {
			t.Fatalf("partial identity = %s/%s, want the request's anthropic/m", providerID, modelID)
		}
		state, err := r.store.ModelState()
		if err != nil {
			t.Fatal(err)
		}
		if state.Provider != "anthropic" || state.ModelID != "m2" || state.ThinkingLevel != "high" {
			t.Fatalf("selection after interrupted output = %s/%s thinking %q", state.Provider, state.ModelID, state.ThinkingLevel)
		}
	})

	t.Run("append failure", func(t *testing.T) {
		r := testRuntime(t)
		next := selectionModels(r)
		injected := errors.New("injected append failure")
		fake := &provider.Fake{}
		hook := &selectionFake{inner: fake, onSend: func() { r.store.FailWritesForTest(injected) }}
		cfg := r.cfg
		cfg.LiveSettings = func() agent.LiveSettings { return liveSelection(hook, next, "high") }
		cfg.SelectionMu = &r.live.selection
		fake.Responses = []*provider.Response{{Text: "final", StopReason: "stop"}}
		err := agent.New(r.store, r.reg, hook, nil).Submit(context.Background(), "question", cfg)
		if err == nil || !strings.Contains(err.Error(), "storage failure") {
			t.Fatalf("run error = %v, want a storage failure", err)
		}
		if !errors.Is(r.store.WriteError(), injected) {
			t.Fatalf("store write error = %v, want the injected failure", r.store.WriteError())
		}
		if got := fake.CallCount(); got != 1 {
			t.Fatalf("provider requests = %d, want 1 (no resend after storage failure)", got)
		}
	})
}
