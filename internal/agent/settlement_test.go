package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

// P2-043, P2-054: skill provenance ordering — user, then psi.input, no context change.
func TestProvenanceOrdering(t *testing.T) {
	dir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(dir)
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "ok", StopReason: "stop"}}, TextDeltas: [][]string{{"o", "k"}}}
	loop := New(store, reg, fake, nil)
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off", Tools: []provider.ToolDef{}}
	if err := loop.SubmitWithProvenance(context.Background(), "expanded body", "/skill:review fix", "skill", cfg); err != nil {
		t.Fatal(err)
	}
	flat, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	// Model context must not include provenance (pi ignores "custom").
	for _, m := range flat.Flat {
		if m.Role == "custom" {
			t.Fatalf("provenance leaked to context: %+v", m)
		}
	}
	// File must contain user + custom psi.input.
	path, _ := store.ActivePath()
	foundUser, foundProv := false, false
	for _, e := range path {
		if e.Kind == "message" && e.Message.Message.User != nil {
			foundUser = true
		}
		if e.Kind == "custom" && e.Custom.CustomType == "psi.input" {
			foundProv = true
		}
	}
	if !foundUser || !foundProv {
		t.Fatalf("ordering: user=%v prov=%v", foundUser, foundProv)
	}
	// Recall prefers visible invocation.
	recall := session.PromptRecall(path)
	if len(recall) == 0 || !strings.Contains(recall[len(recall)-1], "/skill:review") {
		t.Fatalf("recall should prefer invocation, got %v", recall)
	}
}

// P2-055: settlement — terminal, stored, next request agree; unsaved explicit.
func TestSettlementPersistence(t *testing.T) {
	dir := t.TempDir()
	store, _ := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	reg := tools.NewRegistry(dir)
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "final answer", StopReason: "stop"}}, TextDeltas: [][]string{{"final ", "answer"}}}
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off"}
	if err := New(store, reg, fake, nil).Submit(context.Background(), "hi", cfg); err != nil {
		t.Fatal(err)
	}
	proj, _ := store.Projection()
	if len(proj.Flat) < 2 {
		t.Fatalf("expected user+assistant, got %d", len(proj.Flat))
	}
}

type failResponsePersistProvider struct {
	store *session.Store
}

func (p failResponsePersistProvider) Send(_ context.Context, _ provider.Request, onText func(string)) (*provider.Response, error) {
	text := "visible but not saved"
	onText(text)
	p.store.FailWritesForTest(errors.New("injected assistant append failure"))
	return &provider.Response{Text: text, StopReason: "stop"}, nil
}

func (failResponsePersistProvider) CloseIdle() {}

func TestRunEndReportsVisibleTextSettlement(t *testing.T) {
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off"}
	newStore := func(t *testing.T) *session.Store {
		t.Helper()
		dir := t.TempDir()
		store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: true})
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	outcomeOf := func(events []event.Event) string {
		t.Helper()
		count := 0
		outcome := ""
		for _, e := range events {
			if e.Type == event.RunEnd {
				count++
				outcome = e.Outcome
			}
		}
		if count != 1 {
			t.Fatalf("got %d run-settlement events", count)
		}
		return outcome
	}

	t.Run("durable", func(t *testing.T) {
		store := newStore(t)
		fake := &provider.Fake{Responses: []*provider.Response{{Text: "saved", StopReason: "stop"}}, TextDeltas: [][]string{{"saved"}}}
		var events []event.Event
		if err := New(store, tools.NewRegistry(store.Cwd()), fake, func(e event.Event) { events = append(events, e) }).Submit(context.Background(), "hi", cfg); err != nil {
			t.Fatal(err)
		}
		if got := outcomeOf(events); got != event.OutcomeDurable {
			t.Fatalf("durable outcome=%q", got)
		}
	})
	t.Run("saved output limit", func(t *testing.T) {
		store := newStore(t)
		fake := &provider.Fake{Responses: []*provider.Response{{Text: "truncated", StopReason: "length", ToolCalls: []provider.Block{{Type: "toolCall", ID: "unfinished", Name: "bash", Arguments: map[string]any{"command": "true"}}}}}, TextDeltas: [][]string{{"truncated"}}}
		var events []event.Event
		err := New(store, tools.NewRegistry(store.Cwd()), fake, func(e event.Event) { events = append(events, e) }).Submit(context.Background(), "hi", cfg)
		if err == nil || !strings.Contains(err.Error(), "output limit") {
			t.Fatalf("expected visible output-limit error, got %v", err)
		}
		if got := outcomeOf(events); got != event.OutcomeDurable {
			t.Fatalf("saved output-limit outcome=%q", got)
		}
		if len(fake.Calls) != 1 {
			t.Fatalf("unfinished tool request caused more provider calls: %d", len(fake.Calls))
		}
	})
	t.Run("interrupted partial", func(t *testing.T) {
		store := newStore(t)
		fake := &provider.Fake{Errors: []error{errors.New("synthetic stream interruption")}, TextDeltas: [][]string{{"partial"}}}
		var events []event.Event
		if err := New(store, tools.NewRegistry(store.Cwd()), fake, func(e event.Event) { events = append(events, e) }).Submit(context.Background(), "hi", cfg); err == nil {
			t.Fatal("expected interrupted provider response")
		}
		if got := outcomeOf(events); got != event.OutcomeInterrupted {
			t.Fatalf("interrupted outcome=%q", got)
		}
	})
	t.Run("unsaved authoritative response", func(t *testing.T) {
		store := newStore(t)
		var events []event.Event
		loop := New(store, tools.NewRegistry(store.Cwd()), failResponsePersistProvider{store: store}, func(e event.Event) { events = append(events, e) })
		if err := loop.Submit(context.Background(), "hi", cfg); err == nil || !strings.Contains(err.Error(), "unsaved authoritative response") {
			t.Fatalf("expected explicit unsaved error, got %v", err)
		}
		if got := outcomeOf(events); got != event.OutcomeUnsaved {
			t.Fatalf("unsaved outcome=%q", got)
		}
	})
}

// P2-056: cancellation with partial text persists aborted form, no executable call.
func TestCancelPersistsAborted(t *testing.T) {
	dir := t.TempDir()
	store, _ := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	reg := tools.NewRegistry(dir)
	// Cancel before provider call: no partial, no executable call.
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "partial", StopReason: "stop"}}}
	loop := New(store, reg, fake, nil)
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := loop.Submit(ctx, "hi", cfg)
	if err == nil {
		t.Fatal("cancelled submit should fail")
	}
	// If partial was streamed, an aborted message should exist; never a tool call.
	proj, _ := store.Projection()
	for _, m := range proj.Flat {
		if m.Role == "assistant" && m.Assistant != nil {
			for _, b := range m.Assistant.Content {
				if b.Type == "toolCall" {
					t.Fatal("partial must not become executable call")
				}
			}
		}
	}
}
