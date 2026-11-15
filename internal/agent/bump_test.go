package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/config"
	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

func bumpSetup(t *testing.T) (*session.Store, *tools.Registry, *provider.Fake, *Loop, *[]event.Event) {
	t.Helper()
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	fake := &provider.Fake{}
	var events []event.Event
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	return store, reg, fake, loop, &events
}

func bumpConfig() Config {
	return Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}
}

func countMessageRole(store *session.Store, role string) int {
	path, err := store.ActivePath()
	if err != nil {
		return -1
	}
	count := 0
	for _, entry := range path {
		if entry.Kind != "message" || entry.Message == nil {
			continue
		}
		message := entry.Message.Message
		switch role {
		case "user":
			if message.User != nil {
				count++
			}
		case "assistant":
			if message.Assistant != nil {
				count++
			}
		case "toolResult":
			if message.ToolResult != nil {
				count++
			}
		case "custom":
			if message.Custom != nil {
				count++
			}
		}
	}
	return count
}

func assistantEntries(store *session.Store) []*session.AssistantMessage {
	path, err := store.ActivePath()
	if err != nil {
		return nil
	}
	var out []*session.AssistantMessage
	for _, entry := range path {
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.Assistant != nil {
			out = append(out, entry.Message.Message.Assistant)
		}
	}
	return out
}

func userContents(store *session.Store) []string {
	path, err := store.ActivePath()
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range path {
		if entry.Kind != "message" || entry.Message == nil || entry.Message.Message.User == nil {
			continue
		}
		out = append(out, fmt.Sprint(entry.Message.Message.User.Content))
	}
	return out
}

func requestUserTexts(req provider.Request) []string {
	var out []string
	for _, message := range req.Messages {
		if message.Role != "user" {
			continue
		}
		var text strings.Builder
		for _, block := range message.Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
			}
		}
		out = append(out, text.String())
	}
	return out
}

func hasAssistantStop(req provider.Request, stop string) bool {
	for _, message := range req.Messages {
		if message.Role == "assistant" && message.StopReason == stop {
			return true
		}
	}
	return false
}

func singleRunEnd(t *testing.T, events []event.Event) event.Event {
	t.Helper()
	var ends []event.Event
	for _, e := range events {
		if e.Type == event.RunEnd {
			ends = append(ends, e)
		}
	}
	if len(ends) != 1 {
		t.Fatalf("RunEnd count=%d; want 1", len(ends))
	}
	return ends[0]
}

func TestBumpRetriesFailedRequest(t *testing.T) {
	store, _, fake, loop, events := bumpSetup(t)
	cfg := bumpConfig()
	transportErr := errors.New("transport down")
	fake.Errors = []error{transportErr, nil}
	fake.Responses = []*provider.Response{nil, {Text: "done", StopReason: "stop"}}

	var markerAtRunEnd bool
	loop.sink = func(e event.Event) {
		if e.Type == event.RunEnd {
			path, err := store.ActivePath()
			if err == nil {
				for _, entry := range path {
					if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.Assistant != nil {
						assistant := entry.Message.Message.Assistant
						if assistant.StopReason == "error" && assistant.ErrorMessage != nil && *assistant.ErrorMessage != "" {
							markerAtRunEnd = true
						}
					}
				}
			}
		}
		*events = append(*events, e)
	}

	if err := loop.Submit(context.Background(), "hello", cfg); !errors.Is(err, transportErr) {
		t.Fatalf("Submit error=%v; want transport failure", err)
	}
	failedEnd := singleRunEnd(t, *events)
	if failedEnd.Outcome != event.OutcomeInterrupted {
		t.Fatalf("failed RunEnd outcome=%q; want interrupted", failedEnd.Outcome)
	}
	if !markerAtRunEnd {
		t.Fatal("failure marker was not durable before RunEnd")
	}
	if got := countMessageRole(store, "user"); got != 1 {
		t.Fatalf("stored users=%d; want 1", got)
	}
	if got := countMessageRole(store, "assistant"); got != 1 {
		t.Fatalf("assistants after failure=%d; want 1 marker", got)
	}
	assistants := assistantEntries(store)
	if len(assistants) != 1 {
		t.Fatalf("assistant records=%d", len(assistants))
	}
	marker := assistants[0]
	if marker.StopReason != "error" || marker.ErrorMessage == nil || *marker.ErrorMessage == "" {
		t.Fatalf("failure marker shape=%+v", marker)
	}
	if len(marker.Content) != 1 || marker.Content[0].Text == nil || marker.Content[0].Text.Text != "" {
		t.Fatalf("failure marker content=%+v; want one empty text block", marker.Content)
	}
	if marker.Usage.TotalTokens != 0 || marker.Usage.Input != 0 || marker.Usage.Output != 0 {
		t.Fatalf("failure marker copied usage: %+v", marker.Usage)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range session.CurrentView(path) {
		if item.Kind == session.KindAssistant {
			t.Fatalf("failure marker became visible: %#v", item)
		}
	}

	*events = nil
	if err := loop.Bump(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	successEnd := singleRunEnd(t, *events)
	if successEnd.Outcome != event.OutcomeDurable {
		t.Fatalf("successful bump outcome=%q; want durable", successEnd.Outcome)
	}
	assistants = assistantEntries(store)
	if len(assistants) != 2 || assistants[1].StopReason != "stop" || assistants[1].Content[0].Text == nil || assistants[1].Content[0].Text.Text != "done" {
		t.Fatalf("successful assistant shape=%+v", assistants)
	}
	if got := countMessageRole(store, "user"); got != 1 {
		t.Fatalf("bump added a user message: %d", got)
	}
	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider calls=%d; want 2", len(requests))
	}
	if !reflect.DeepEqual(requests[0].Messages, requests[1].Messages) {
		t.Fatalf("unchanged configuration produced different requests:\n%#v\n%#v", requests[0].Messages, requests[1].Messages)
	}
	if reqUsers, retryUsers := requestUserTexts(requests[0]), requestUserTexts(requests[1]); !reflect.DeepEqual(reqUsers, retryUsers) || len(retryUsers) != 1 || retryUsers[0] != "hello" {
		t.Fatalf("retry user content changed: %v -> %v", reqUsers, retryUsers)
	}
	if hasAssistantStop(requests[1], "error") {
		t.Fatal("failure assistant replayed in retry request")
	}
	path, err = store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	visible := 0
	for _, item := range session.CurrentView(path) {
		if item.Kind == session.KindAssistant {
			visible++
			if item.Text != "done" {
				t.Fatalf("unexpected visible assistant text %q", item.Text)
			}
		}
	}
	if visible != 1 {
		t.Fatalf("visible assistant rows=%d; want only the successful answer", visible)
	}
}

func TestBumpRefusesCompleted(t *testing.T) {
	assertBumpRefusedTip(t, "stop")
}

func TestBumpRefusesLength(t *testing.T) {
	assertBumpRefusedTip(t, "length")
}

func assertBumpRefusedTip(t *testing.T, stop string) {
	store, _, fake, loop, _ := bumpSetup(t)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "hello", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	assistant := session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: stop,
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "answer"}}},
	}}
	if _, err := store.AppendMessage(assistant); err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	leaf, count := store.LeafID(), store.EntryCount()
	err = loop.Bump(context.Background(), bumpConfig())
	if !errors.Is(err, ErrNotBumpable) {
		t.Fatalf("Bump error=%v; want ErrNotBumpable", err)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("refused bump called provider %d times", fake.CallCount())
	}
	afterBytes, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterBytes, beforeBytes) || store.LeafID() != leaf || store.EntryCount() != count {
		t.Fatal("refused bump changed storage")
	}
}

func TestBumpNonPersistentCopyDoesNotTouchSource(t *testing.T) {
	source, _, fake, _, _ := bumpSetup(t)
	if _, err := source.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "hello", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source.File())
	if err != nil {
		t.Fatal(err)
	}
	copyStore, err := session.OpenFile(source.File(), source.Cwd(), false)
	if err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	loop := New(copyStore, sourceRegistry(t, source.Cwd()), fake, nil)
	if err := loop.Bump(context.Background(), bumpConfig()); err != nil {
		t.Fatal(err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("provider calls=%d; want 1", fake.CallCount())
	}
	after, err := os.ReadFile(source.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("non-persistent bump changed the source file")
	}
	if got := countMessageRole(copyStore, "assistant"); got != 1 {
		t.Fatalf("non-persistent assistant count=%d", got)
	}
}

func sourceRegistry(t *testing.T, cwd string) *tools.Registry {
	t.Helper()
	return tools.NewRegistry(cwd)
}

func TestBumpRefusesChangedStorage(t *testing.T) {
	store, _, fake, loop, _ := bumpSetup(t)
	cfg := bumpConfig()
	fake.Errors = []error{errors.New("transport down")}
	if err := loop.Submit(context.Background(), "hello", cfg); err == nil {
		t.Fatal("Submit succeeded")
	}
	if _, err := store.Projection(); err != nil {
		t.Fatal(err)
	}
	leaf, count := store.LeafID(), store.EntryCount()
	f, err := os.OpenFile(store.File(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	changed, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	err = loop.Bump(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("Bump error=%v; want storage refusal", err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("refused bump called provider %d times; want only the initial submit", fake.CallCount())
	}
	if store.LeafID() != leaf || store.EntryCount() != count {
		t.Fatal("refused bump changed logical state")
	}
	if store.WriteError() == nil {
		t.Fatal("changed storage did not set a sticky error")
	}
	after, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, changed) {
		t.Fatal("refused bump changed the externally changed file")
	}
	if err := loop.Bump(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("second bump error=%v; want sticky storage refusal", err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("second refused bump called provider: %d", fake.CallCount())
	}
}

func TestBumpRefusesInvalidRequestAndCancelledContext(t *testing.T) {
	store, _, fake, loop, _ := bumpSetup(t)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "hello", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	leaf, count := store.LeafID(), store.EntryCount()
	invalid := bumpConfig()
	invalid.Model.API = "unsupported-api"
	if err := loop.Bump(context.Background(), invalid); err == nil {
		t.Fatal("invalid request accepted")
	}
	if fake.CallCount() != 0 {
		t.Fatal("invalid request reached the provider")
	}
	if store.LeafID() != leaf || store.EntryCount() != count {
		t.Fatal("invalid request mutated the session")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := loop.Bump(cancelled, bumpConfig()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Bump error=%v", err)
	}
	if fake.CallCount() != 0 {
		t.Fatal("cancelled bump reached the provider")
	}
	afterBytes, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterBytes, beforeBytes) || store.LeafID() != leaf || store.EntryCount() != count {
		t.Fatal("cancelled bump changed storage")
	}
}

func TestBumpRepeatedFailureKeepsSingleMarker(t *testing.T) {
	store, _, fake, loop, _ := bumpSetup(t)
	cfg := bumpConfig()
	fake.Errors = []error{errors.New("first failure"), errors.New("second failure")}
	if err := loop.Submit(context.Background(), "hello", cfg); err == nil {
		t.Fatal("first submit succeeded")
	}
	if got := countMessageRole(store, "assistant"); got != 1 {
		t.Fatalf("assistants after first failure=%d", got)
	}
	if err := loop.Bump(context.Background(), cfg); err == nil {
		t.Fatal("bump succeeded")
	}
	if fake.CallCount() != 2 {
		t.Fatalf("provider calls=%d; want 2", fake.CallCount())
	}
	if got := countMessageRole(store, "assistant"); got != 1 {
		t.Fatalf("repeated failure duplicated the marker: %d assistants", got)
	}
	if got := countMessageRole(store, "user"); got != 1 {
		t.Fatalf("repeated failure added a user: %d", got)
	}
}

func TestBumpProviderFailureWithoutOutputKeepsSingleMarker(t *testing.T) {
	for _, stop := range []string{"error", "aborted"} {
		t.Run(stop, func(t *testing.T) {
			store, _, fake, loop, events := bumpSetup(t)
			cfg := bumpConfig()
			fake.Errors = []error{errors.New("transport down")}
			if err := loop.Submit(context.Background(), "hello", cfg); err == nil {
				t.Fatal("Submit succeeded")
			}
			initial := assistantEntries(store)
			if len(initial) != 1 || initial[0].ErrorMessage == nil {
				t.Fatalf("initial marker=%+v", initial)
			}
			reason := *initial[0].ErrorMessage

			*events = nil
			fake.Errors = nil
			fake.Responses = []*provider.Response{nil, {StopReason: stop}}
			err := loop.Bump(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), "provider stopped: "+stop) {
				t.Fatalf("Bump error=%v; want provider stop %q", err, stop)
			}
			final := assistantEntries(store)
			if len(final) != 1 {
				t.Fatalf("outputless retry stacked failure markers: %+v", final)
			}
			if final[0].StopReason != "error" || final[0].ErrorMessage == nil || *final[0].ErrorMessage != reason {
				t.Fatalf("existing marker changed: %+v", final[0])
			}
			if got := countMessageRole(store, "user"); got != 1 {
				t.Fatalf("outputless retry added a user: %d", got)
			}
			if end := singleRunEnd(t, *events); end.Outcome != event.OutcomeInterrupted {
				t.Fatalf("outcome=%q; want interrupted", end.Outcome)
			}
		})
	}
}

func TestBumpFailureMarkerUsage(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		wantUse bool
	}{
		{name: "empty marker", wantUse: false},
		{name: "whitespace marker", text: " \n", wantUse: false},
		{name: "visible partial", text: "partial", wantUse: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _, fake, loop, _ := bumpSetup(t)
			fake.Responses = []*provider.Response{{
				Text: tc.text, StopReason: "error",
				Usage: provider.Usage{Input: 3, Output: 4, TotalTokens: 7},
			}}
			if err := loop.Submit(context.Background(), "hello", bumpConfig()); err == nil {
				t.Fatal("Submit succeeded")
			}
			assistants := assistantEntries(store)
			if len(assistants) != 1 {
				t.Fatalf("assistant records=%d; want one failure marker", len(assistants))
			}
			marker := assistants[0]
			if marker.ErrorMessage == nil || marker.StopReason != "error" {
				t.Fatalf("failure marker shape=%+v", marker)
			}
			if tc.wantUse {
				if marker.Usage.TotalTokens != 7 {
					t.Fatalf("visible partial usage=%+v; want retained", marker.Usage)
				}
				return
			}
			if marker.Usage.TotalTokens != 0 || marker.Usage.Input != 0 || marker.Usage.Output != 0 {
				t.Fatalf("empty marker copied usage: %+v", marker.Usage)
			}
		})
	}
}

func TestBumpWhitespaceOnlyPartialKeepsSingleMarker(t *testing.T) {
	store, _, fake, loop, events := bumpSetup(t)
	cfg := bumpConfig()
	fake.Errors = []error{errors.New("transport down"), errors.New("still down")}
	fake.TextDeltas = [][]string{nil, {" \n"}}
	if err := loop.Submit(context.Background(), "hello", cfg); err == nil {
		t.Fatal("Submit succeeded")
	}
	initial := assistantEntries(store)
	if len(initial) != 1 || initial[0].ErrorMessage == nil {
		t.Fatalf("initial marker=%+v", initial)
	}
	reason := *initial[0].ErrorMessage

	*events = nil
	if err := loop.Bump(context.Background(), cfg); err == nil {
		t.Fatal("Bump succeeded")
	}
	final := assistantEntries(store)
	if len(final) != 1 {
		t.Fatalf("whitespace-only retry stacked failure markers: %+v", final)
	}
	if final[0].ErrorMessage == nil || *final[0].ErrorMessage != reason {
		t.Fatalf("existing marker changed: %+v", final[0])
	}
	if got := countMessageRole(store, "user"); got != 1 {
		t.Fatalf("whitespace-only retry added a user: %d", got)
	}
	if end := singleRunEnd(t, *events); end.Outcome != event.OutcomeInterrupted {
		t.Fatalf("outcome=%q; want interrupted", end.Outcome)
	}
}

func TestBumpCancellationDuringProviderErrorPersistsAborted(t *testing.T) {
	store, reg, _, _, _ := bumpSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	prov := &scriptedProvider{send: func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		cancel()
		return &provider.Response{Text: "partial", StopReason: "error"}, nil
	}}
	loop := New(store, reg, prov, nil)
	err := loop.Submit(ctx, "hello", bumpConfig())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit error=%v; want cancellation", err)
	}
	assistants := assistantEntries(store)
	if len(assistants) != 1 {
		t.Fatalf("assistant records=%d; want one aborted partial", len(assistants))
	}
	assistant := assistants[0]
	if assistant.StopReason != "aborted" || assistant.ErrorMessage == nil || *assistant.ErrorMessage != context.Canceled.Error() {
		t.Fatalf("aborted partial=%+v", assistant)
	}
	if len(assistant.Content) != 1 || assistant.Content[0].Text == nil || assistant.Content[0].Text.Text != "partial" {
		t.Fatalf("partial content lost: %+v", assistant.Content)
	}
}

func TestBumpProviderFailureAppendFailurePreservesCause(t *testing.T) {
	store, reg, _, _, events := bumpSetup(t)
	storageErr := errors.New("storage unavailable")
	prov := &scriptedProvider{send: func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		store.FailWritesForTest(storageErr)
		return &provider.Response{Text: "partial", StopReason: "error"}, nil
	}}
	loop := New(store, reg, prov, func(e event.Event) { *events = append(*events, e) })
	err := loop.Submit(context.Background(), "hello", bumpConfig())
	if !errors.Is(err, storageErr) || !strings.Contains(err.Error(), "provider stopped: error") {
		t.Fatalf("error=%v; want provider and storage causes", err)
	}
	if end := singleRunEnd(t, *events); end.Outcome != event.OutcomeUnsaved {
		t.Fatalf("outcome=%q; want unsaved", end.Outcome)
	}
	if got := countMessageRole(store, "assistant"); got != 0 {
		t.Fatalf("failed authoritative append produced %d assistants", got)
	}
}

func TestFailureFinalizationMarksStickyStorageErrorUnsaved(t *testing.T) {
	store, _, _, loop, _ := bumpSetup(t)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "hello", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	reason := "transport down"
	marker := session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: "error", ErrorMessage: &reason,
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: ""}}},
	}}
	if _, err := store.AppendMessage(marker); err != nil {
		t.Fatal(err)
	}
	before := countMessageRole(store, "assistant")
	storageErr := errors.New("storage unavailable")
	store.FailWritesForTest(storageErr)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "blocked", Timestamp: 2}}); err == nil {
		t.Fatal("blocked append succeeded")
	}
	outcome := event.OutcomeDurable
	runErr := errors.New("run failed")
	cfg := bumpConfig()
	loop.finalizeFailure(context.Background(), &cfg, &outcome, &runErr)
	if outcome != event.OutcomeUnsaved {
		t.Fatalf("outcome=%q; want unsaved", outcome)
	}
	if got := countMessageRole(store, "assistant"); got != before {
		t.Fatalf("finalization changed marker count: %d -> %d", before, got)
	}
	if !strings.Contains(runErr.Error(), "run failed") {
		t.Fatalf("original error lost: %v", runErr)
	}
}

func TestFailureFinalizationDoesNotMutateUnsafeContinuation(t *testing.T) {
	store, _, _, loop, _ := bumpSetup(t)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "old", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: "deferred",
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "later"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "now", Timestamp: 2}}); err != nil {
		t.Fatal(err)
	}
	before := store.EntryCount()
	outcome := event.OutcomeDurable
	runErr := errors.New("deferred message blocks continuation")
	cfg := bumpConfig()
	loop.finalizeFailure(context.Background(), &cfg, &outcome, &runErr)
	if store.EntryCount() != before {
		t.Fatalf("finalization mutated unsafe continuation: %d -> %d", before, store.EntryCount())
	}
	if outcome != event.OutcomeDurable {
		t.Fatalf("outcome=%q; want durable", outcome)
	}
}

// bumpGateProvider blocks until the context is cancelled, optionally streaming a
// prefix and running a hook first.
type bumpGateProvider struct {
	started chan struct{}
	once    sync.Once
	text    string
	onSend  func()
}

func (p *bumpGateProvider) Send(ctx context.Context, _ provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	p.once.Do(func() { close(p.started) })
	if p.onSend != nil {
		p.onSend()
	}
	if p.text != "" && onDelta != nil {
		onDelta(provider.Delta{Kind: provider.DeltaText, Text: p.text})
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*bumpGateProvider) CloseIdle() {}

func waitForStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not entered")
	}
}

func TestBumpCancelBeforeTextLeavesAbortedMarker(t *testing.T) {
	store, reg, _, _, events := bumpSetup(t)
	prov := &bumpGateProvider{started: make(chan struct{})}
	loop := New(store, reg, prov, func(e event.Event) { *events = append(*events, e) })
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- loop.Submit(ctx, "hello", bumpConfig()) }()
	waitForStart(t, prov.started)
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	end := singleRunEnd(t, *events)
	if end.Outcome != event.OutcomeInterrupted {
		t.Fatalf("cancel outcome=%q", end.Outcome)
	}
	assistants := assistantEntries(store)
	if len(assistants) != 1 || assistants[0].StopReason != "aborted" || assistants[0].ErrorMessage == nil {
		t.Fatalf("aborted marker=%+v", assistants)
	}
}

func TestBumpCancelAfterPartialTextKeepsAbortedPartial(t *testing.T) {
	store, reg, _, _, _ := bumpSetup(t)
	prov := &bumpGateProvider{started: make(chan struct{}), text: "partial"}
	loop := New(store, reg, prov, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- loop.Submit(ctx, "hello", bumpConfig()) }()
	waitForStart(t, prov.started)
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	assistants := assistantEntries(store)
	if len(assistants) != 1 {
		t.Fatalf("assistant records=%d; want one partial", len(assistants))
	}
	assistant := assistants[0]
	if assistant.StopReason != "aborted" || assistant.ErrorMessage == nil || *assistant.ErrorMessage != context.Canceled.Error() {
		t.Fatalf("aborted partial=%+v", assistant)
	}
	if len(assistant.Content) != 1 || assistant.Content[0].Text == nil || assistant.Content[0].Text.Text != "partial" {
		t.Fatalf("partial content lost: %+v", assistant.Content)
	}
}

func TestBumpCancelAtRunStartBeforeProvider(t *testing.T) {
	store, _, fake, loop, events := bumpSetup(t)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "hello", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := loop.run(ctx, bumpConfig(), false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error=%v", err)
	}
	if fake.CallCount() != 0 {
		t.Fatal("cancelled run entered the provider")
	}
	end := singleRunEnd(t, *events)
	if end.Outcome != event.OutcomeInterrupted {
		t.Fatalf("RunEnd outcome=%q; want interrupted", end.Outcome)
	}
	assistants := assistantEntries(store)
	if len(assistants) != 1 || assistants[0].StopReason != "aborted" {
		t.Fatalf("aborted marker=%+v", assistants)
	}
}

// scriptedProvider runs one test-supplied Send implementation.
type scriptedProvider struct {
	mu   sync.Mutex
	send func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error)
}

func (p *scriptedProvider) Send(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	p.mu.Lock()
	send := p.send
	p.mu.Unlock()
	return send(ctx, req, onDelta)
}

func (*scriptedProvider) CloseIdle() {}

func TestBumpFailureMatrix(t *testing.T) {
	cases := []struct {
		name      string
		prepare   func(*provider.Fake)
		submitErr string
		check     func(*testing.T, *session.AssistantMessage)
	}{
		{
			name: "nil response",
			prepare: func(fake *provider.Fake) {
				fake.Responses = []*provider.Response{nil, {Text: "done", StopReason: "stop"}}
			},
			submitErr: "provider returned no completed message",
			check: func(t *testing.T, marker *session.AssistantMessage) {
				if marker.StopReason != "error" || len(marker.Content) != 1 || marker.Content[0].Text == nil || marker.Content[0].Text.Text != "" {
					t.Fatalf("nil response marker=%+v", marker)
				}
			},
		},
		{
			name: "final text contradiction",
			prepare: func(fake *provider.Fake) {
				fake.TextDeltas = [][]string{{"prefix"}, nil}
				fake.Responses = []*provider.Response{{Text: "other", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c1", Name: "count", Arguments: map[string]any{}}}}, {Text: "done", StopReason: "stop"}}
			},
			submitErr: "protocol failure",
			check: func(t *testing.T, marker *session.AssistantMessage) {
				if len(marker.Content) != 1 || marker.Content[0].Text == nil || marker.Content[0].Text.Text != "prefix" {
					t.Fatalf("contradiction partial lost: %+v", marker.Content)
				}
			},
		},
		{
			name: "provider error stop",
			prepare: func(fake *provider.Fake) {
				fake.Responses = []*provider.Response{{Text: "partial", StopReason: "error"}, {Text: "done", StopReason: "stop"}}
			},
			submitErr: "provider stopped: error",
			check: func(t *testing.T, marker *session.AssistantMessage) {
				if marker.ErrorMessage == nil || !strings.Contains(*marker.ErrorMessage, "provider stopped: error") {
					t.Fatalf("provider error reason=%v", marker.ErrorMessage)
				}
				if len(marker.Content) != 1 || marker.Content[0].Text == nil || marker.Content[0].Text.Text != "partial" {
					t.Fatalf("provider error content=%+v", marker.Content)
				}
			},
		},
		{
			name: "provider aborted stop",
			prepare: func(fake *provider.Fake) {
				fake.Responses = []*provider.Response{{StopReason: "aborted"}, {Text: "done", StopReason: "stop"}}
			},
			submitErr: "provider stopped: aborted",
			check: func(t *testing.T, marker *session.AssistantMessage) {
				if marker.StopReason != "aborted" || marker.ErrorMessage == nil {
					t.Fatalf("aborted response marker=%+v", marker)
				}
				if len(marker.Content) != 1 || marker.Content[0].Text == nil || marker.Content[0].Text.Text != "" {
					t.Fatalf("empty aborted response content=%+v", marker.Content)
				}
			},
		},
		{
			name: "reasoning only partial",
			prepare: func(fake *provider.Fake) {
				fake.ThinkingDeltas = [][]string{{"why"}, nil}
				fake.Errors = []error{errors.New("transport down"), nil}
				fake.Responses = []*provider.Response{nil, {Text: "done", StopReason: "stop"}}
			},
			submitErr: "transport down",
			check: func(t *testing.T, marker *session.AssistantMessage) {
				if len(marker.Content) != 1 || marker.Content[0].Thinking == nil || marker.Content[0].Thinking.Thinking != "why" {
					t.Fatalf("reasoning partial lost: %+v", marker.Content)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _, fake, loop, _ := bumpSetup(t)
			cfg := bumpConfig()
			toolRuns := 0
			cfg.Tools = append(cfg.Tools, provider.ToolDef{Name: "count", Schema: map[string]any{"type": "object"}})
			cfg.InternalTools = map[string]InternalToolHandler{"count": func(context.Context, tools.Invocation) tools.Result {
				toolRuns++
				return tools.Result{Content: []tools.Block{{Type: "text", Text: "ran"}}}
			}}
			tc.prepare(fake)
			err := loop.Submit(context.Background(), "hello", cfg)
			if err == nil || !strings.Contains(err.Error(), tc.submitErr) {
				t.Fatalf("Submit error=%v; want %q", err, tc.submitErr)
			}
			assistants := assistantEntries(store)
			if len(assistants) != 1 {
				t.Fatalf("failure assistants=%d; want 1", len(assistants))
			}
			if assistants[0].ErrorMessage == nil || *assistants[0].ErrorMessage == "" {
				t.Fatal("failure assistant lost its reason")
			}
			tc.check(t, assistants[0])
			if toolRuns != 0 {
				t.Fatalf("failed turn executed %d tool calls", toolRuns)
			}
			if got := countMessageRole(store, "user"); got != 1 {
				t.Fatalf("failure added a user: %d", got)
			}
			if err := loop.Bump(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			assistants = assistantEntries(store)
			if len(assistants) != 2 {
				t.Fatalf("assistants after bump=%d; want failure plus answer", len(assistants))
			}
			requests := fake.Requests()
			if hasAssistantStop(requests[len(requests)-1], "error") || hasAssistantStop(requests[len(requests)-1], "aborted") {
				t.Fatal("failure assistant replayed in retry request")
			}
			if got := countMessageRole(store, "user"); got != 1 {
				t.Fatalf("bump added a user: %d", got)
			}
		})
	}
}

func TestBumpMarkerAppendFailurePreservesOriginalError(t *testing.T) {
	store, reg, _, _, events := bumpSetup(t)
	transportErr := errors.New("transport down")
	storageErr := errors.New("storage unavailable")
	prov := &scriptedProvider{send: func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		store.FailWritesForTest(storageErr)
		return nil, transportErr
	}}
	loop := New(store, reg, prov, func(e event.Event) { *events = append(*events, e) })
	err := loop.Submit(context.Background(), "hello", bumpConfig())
	if !errors.Is(err, transportErr) || !errors.Is(err, storageErr) {
		t.Fatalf("error=%v; want transport and storage causes", err)
	}
	end := singleRunEnd(t, *events)
	if end.Outcome != event.OutcomeUnsaved {
		t.Fatalf("outcome=%q; want unsaved", end.Outcome)
	}
	if store.WriteError() == nil {
		t.Fatal("failed marker append did not block storage")
	}
	if got := countMessageRole(store, "assistant"); got != 0 {
		t.Fatalf("failed marker produced %d assistant records", got)
	}
	if err := loop.Bump(context.Background(), bumpConfig()); err == nil || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("bump after failed marker error=%v", err)
	}
}

func TestBumpCancellationWithTransportErrorAndFailedSave(t *testing.T) {
	store, reg, _, _, events := bumpSetup(t)
	transportErr := errors.New("network reset")
	storageErr := errors.New("storage unavailable")
	ctx, cancel := context.WithCancel(context.Background())
	prov := &scriptedProvider{send: func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		cancel()
		store.FailWritesForTest(storageErr)
		return nil, transportErr
	}}
	loop := New(store, reg, prov, func(e event.Event) { *events = append(*events, e) })
	err := loop.Submit(ctx, "hello", bumpConfig())
	if !errors.Is(err, context.Canceled) || !errors.Is(err, storageErr) {
		t.Fatalf("error=%v; want cancellation and storage causes", err)
	}
	end := singleRunEnd(t, *events)
	if end.Outcome != event.OutcomeUnsaved {
		t.Fatalf("outcome=%q; want unsaved", end.Outcome)
	}
}

func TestBumpPartialAppendFailureBlocksLaterBump(t *testing.T) {
	store, reg, _, _, events := bumpSetup(t)
	transportErr := errors.New("stream cut")
	storageErr := errors.New("storage unavailable")
	prov := &scriptedProvider{send: func(_ context.Context, _ provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
		if onDelta != nil {
			onDelta(provider.Delta{Kind: provider.DeltaText, Text: "partial"})
		}
		store.FailWritesForTest(storageErr)
		return nil, transportErr
	}}
	loop := New(store, reg, prov, func(e event.Event) { *events = append(*events, e) })
	err := loop.Submit(context.Background(), "hello", bumpConfig())
	if !errors.Is(err, transportErr) || !errors.Is(err, storageErr) {
		t.Fatalf("error=%v; want original and storage causes", err)
	}
	end := singleRunEnd(t, *events)
	if end.Outcome != event.OutcomeUnsaved {
		t.Fatalf("outcome=%q; want unsaved", end.Outcome)
	}
	if got := countMessageRole(store, "assistant"); got != 0 {
		t.Fatalf("failed partial produced %d assistant records", got)
	}
	before := store.EntryCount()
	if err := loop.Bump(context.Background(), bumpConfig()); err == nil || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("bump after failed partial error=%v", err)
	}
	if store.EntryCount() != before {
		t.Fatal("refused bump mutated the session")
	}
}

func TestBumpKnownInterruptedUsagePersistedOnce(t *testing.T) {
	store, _, fake, loop, _ := bumpSetup(t)
	cfg := bumpConfig()
	fake.Errors = []error{&provider.StreamInterrupt{
		Partial: &provider.Response{Usage: provider.Usage{Input: 3, Output: 4, TotalTokens: 7}},
		Err:     errors.New("stream cut"),
	}, nil}
	fake.Responses = []*provider.Response{nil, {Text: "done", StopReason: "stop"}}
	if err := loop.Submit(context.Background(), "hello", cfg); err == nil {
		t.Fatal("Submit succeeded")
	}
	countUsage := func() (int, float64) {
		path, err := store.ActivePath()
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		total := 0.0
		for _, entry := range path {
			if entry.Kind == "usage" && entry.Usage != nil && entry.Usage.Kind2 == "stream-interrupt" {
				count++
				total += entry.Usage.Usage.TotalTokens
			}
		}
		return count, total
	}
	if count, total := countUsage(); count != 1 || total != 7 {
		t.Fatalf("interrupted usage count=%d total=%v; want 1/7", count, total)
	}
	for _, assistant := range assistantEntries(store) {
		if assistant.Usage.TotalTokens != 0 {
			t.Fatalf("failure marker copied usage: %+v", assistant.Usage)
		}
	}
	if err := loop.Bump(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if count, total := countUsage(); count != 1 || total != 7 {
		t.Fatalf("usage after bump count=%d total=%v; want 1/7", count, total)
	}
}

func TestBumpPreservesCompletedSettlementAfterFollowOnStorageFailure(t *testing.T) {
	store, _, fake, loop, events := bumpSetup(t)
	cfg := bumpConfig()
	storageErr := errors.New("continuation write failed")
	continued := false
	cfg.ContinueOnEnd = func() (string, string, bool) {
		if continued {
			return "", "", false
		}
		continued = true
		store.FailWritesForTest(storageErr)
		return "test.continue", "next", true
	}
	fake.Responses = []*provider.Response{{Text: "answer", StopReason: "stop"}}
	err := loop.Submit(context.Background(), "hello", cfg)
	if !errors.Is(err, storageErr) {
		t.Fatalf("Submit error=%v; want storage cause", err)
	}
	end := singleRunEnd(t, *events)
	if end.Outcome != event.OutcomeDurable {
		t.Fatalf("settlement=%q; want durable saved answer", end.Outcome)
	}
	assistants := assistantEntries(store)
	if len(assistants) != 1 || assistants[0].Content[0].Text == nil || assistants[0].Content[0].Text.Text != "answer" {
		t.Fatalf("saved answer lost: %+v", assistants)
	}
	before := store.EntryCount()
	if err := loop.Bump(context.Background(), bumpConfig()); err == nil || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("bump after follow-on storage failure error=%v", err)
	}
	if store.EntryCount() != before {
		t.Fatal("bump mutated storage after follow-on failure")
	}
	if fake.CallCount() != 1 {
		t.Fatalf("bump reached provider after storage block: calls=%d", fake.CallCount())
	}
}

func TestBumpRefusesActiveRun(t *testing.T) {
	store, reg, _, _, _ := bumpSetup(t)
	prov := &bumpGateProvider{started: make(chan struct{})}
	loop := New(store, reg, prov, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- loop.Submit(ctx, "hello", bumpConfig()) }()
	waitForStart(t, prov.started)
	err := loop.Bump(context.Background(), bumpConfig())
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("Bump during run error=%v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit error=%v", err)
	}
}

func TestBumpAfterReopenKeepsReasonAndOmitsMarker(t *testing.T) {
	store, _, fake, loop, _ := bumpSetup(t)
	cfg := bumpConfig()
	fake.Errors = []error{errors.New("transport down")}
	if err := loop.Submit(context.Background(), "hello", cfg); err == nil {
		t.Fatal("Submit succeeded")
	}
	assistants := assistantEntries(store)
	if len(assistants) != 1 || assistants[0].ErrorMessage == nil {
		t.Fatalf("failed marker=%+v", assistants)
	}
	reason := *assistants[0].ErrorMessage
	reopened, err := session.OpenFile(store.File(), store.Cwd(), true)
	if err != nil {
		t.Fatal(err)
	}
	reopenedFake := &provider.Fake{Responses: []*provider.Response{{Text: "done", StopReason: "stop"}}}
	reopenedLoop := New(reopened, sourceRegistry(t, reopened.Cwd()), reopenedFake, nil)
	if err := reopenedLoop.Bump(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	assistants = assistantEntries(reopened)
	if len(assistants) != 2 || assistants[0].ErrorMessage == nil || *assistants[0].ErrorMessage != reason {
		t.Fatalf("reopened failure reason lost: %+v", assistants)
	}
	if got := countMessageRole(reopened, "user"); got != 1 {
		t.Fatalf("reopened bump added a user: %d", got)
	}
	requests := reopenedFake.Requests()
	if len(requests) != 1 || hasAssistantStop(requests[0], "error") {
		t.Fatalf("reopened retry request=%+v", requests)
	}
	if users := requestUserTexts(requests[0]); len(users) != 1 || users[0] != "hello" {
		t.Fatalf("reopened retry user content=%v", users)
	}
	path, err := reopened.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range session.CurrentView(path) {
		if item.Kind == session.KindAssistant && item.Text != "done" {
			t.Fatalf("reopened failure marker became visible: %#v", item)
		}
	}
}

func TestBumpPreMarkerCrashStates(t *testing.T) {
	toolCall := session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: "toolUse",
		Content: []session.ContentBlock{{Type: "toolCall", Call: &session.ToolCall{Type: "toolCall", ID: "c1", Name: "read", Arguments: map[string]any{}}}},
	}}
	cases := []struct {
		name string
		tip  session.AgentMessage
	}{
		{name: "user", tip: session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "hello", Timestamp: 1}}},
		{name: "tool result", tip: session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{Role: "toolResult", ToolCallID: "c0", ToolName: "read", Content: []session.UserContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "ok"}}}, Timestamp: 1}}},
		{name: "custom", tip: session.AgentMessage{Role: "custom", Custom: &session.CustomMessage{Role: "custom", CustomType: "ext", Content: "note", Timestamp: 1}}},
		{name: "tool use", tip: toolCall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _, fake, loop, _ := bumpSetup(t)
			if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "older", Timestamp: 1}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(tc.tip); err != nil {
				t.Fatal(err)
			}
			usersBefore := countMessageRole(store, "user")
			provenanceBefore := 0
			path, err := store.ActivePath()
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range path {
				if entry.Kind == "custom" {
					provenanceBefore++
				}
			}
			fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
			if err := loop.Bump(context.Background(), bumpConfig()); err != nil {
				t.Fatal(err)
			}
			if fake.CallCount() != 1 {
				t.Fatalf("provider calls=%d; want 1", fake.CallCount())
			}
			if got := countMessageRole(store, "user"); got != usersBefore {
				t.Fatalf("bump changed user count: %d -> %d", usersBefore, got)
			}
			path, err = store.ActivePath()
			if err != nil {
				t.Fatal(err)
			}
			provenanceAfter := 0
			toolResults := 0
			for _, entry := range path {
				if entry.Kind == "custom" {
					provenanceAfter++
				}
				if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.ToolResult != nil {
					toolResults++
				}
			}
			if provenanceAfter != provenanceBefore {
				t.Fatal("bump added provenance")
			}
			if hasAssistantStop(fake.Requests()[0], "error") {
				t.Fatal("bump request contained a synthetic failure marker")
			}
			if tc.name == "tool use" {
				if toolResults != 0 {
					t.Fatalf("synthetic tool result was written to the session: %d", toolResults)
				}
				found := false
				for _, message := range fake.Requests()[0].Messages {
					if message.Role == "toolResult" && message.ToolCallID == "c1" && message.IsError && len(message.Content) == 1 && message.Content[0].Text == "No result provided" {
						found = true
					}
				}
				if !found {
					t.Fatal("synthetic missing tool result absent from request context")
				}
			}
		})
	}
}

func TestBumpDoesNotReexecuteCompletedTool(t *testing.T) {
	store, _, fake, loop, _ := bumpSetup(t)
	cfg := bumpConfig()
	cfg.InternalTools = map[string]InternalToolHandler{
		"count": func(context.Context, tools.Invocation) tools.Result {
			return tools.Result{Content: []tools.Block{{Type: "text", Text: "counted"}}}
		},
	}
	cfg.Tools = append(cfg.Tools, provider.ToolDef{Name: "count", Schema: map[string]any{"type": "object"}})
	calls := 0
	cfg.InternalTools["count"] = func(context.Context, tools.Invocation) tools.Result {
		calls++
		return tools.Result{Content: []tools.Block{{Type: "text", Text: "counted"}}}
	}
	fake.Responses = []*provider.Response{
		{StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c1", Name: "count", Arguments: map[string]any{}}}},
		nil,
		{Text: "final", StopReason: "stop"},
	}
	fake.Errors = []error{nil, errors.New("transport down"), nil}
	if err := loop.Submit(context.Background(), "hello", cfg); err == nil {
		t.Fatal("Submit succeeded")
	}
	if calls != 1 {
		t.Fatalf("tool calls after failure=%d; want 1", calls)
	}
	if got := countMessageRole(store, "toolResult"); got != 1 {
		t.Fatalf("durable tool results=%d; want 1", got)
	}
	if err := loop.Bump(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("bump re-executed the completed tool: calls=%d", calls)
	}
	requests := fake.Requests()
	retry := requests[len(requests)-1]
	results := 0
	for _, message := range retry.Messages {
		if message.Role == "toolResult" && message.ToolCallID == "c1" {
			results++
		}
	}
	if results != 1 {
		t.Fatalf("retry context tool results=%d; want 1 durable result", results)
	}
	if hasAssistantStop(retry, "error") {
		t.Fatal("retry context replayed the failure marker")
	}
}

func TestBumpUsesCurrentConfiguration(t *testing.T) {
	_, _, fake, loop, _ := bumpSetup(t)
	cfg := bumpConfig()
	fake.Errors = []error{errors.New("transport down")}
	if err := loop.Submit(context.Background(), "hello", cfg); err == nil {
		t.Fatal("Submit succeeded")
	}
	next := bumpConfig()
	next.Model = provider.ModelMeta{ID: "new-model", API: "anthropic-messages", MaxTokens: 16000, Reasoning: true}
	next.Thinking = "medium"
	next.DynamicConfig = func() (string, []config.PromptSection, []provider.ToolDef) {
		sections := []config.PromptSection{{Name: "psi", Body: "dynamic system"}}
		return config.RenderSections(sections), sections, []provider.ToolDef{{Name: "dynamic_tool", Schema: map[string]any{"type": "object"}}}
	}
	fake.Responses = []*provider.Response{nil, {Text: "done", StopReason: "stop"}}
	if err := loop.Bump(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	request := requests[len(requests)-1]
	if request.Model.ID != "new-model" || request.Thinking != "medium" {
		t.Fatalf("current model/thinking not used: %+v", request.Model)
	}
	if !strings.Contains(request.System, "dynamic system") {
		t.Fatalf("current dynamic system not used: %q", request.System)
	}
	foundTool := false
	for _, tool := range request.Tools {
		if tool.Name == "dynamic_tool" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatalf("current dynamic tools not used: %+v", request.Tools)
	}
}

func TestBumpPreservesStructuredUserContent(t *testing.T) {
	store, _, fake, loop, _ := bumpSetup(t)
	content := []session.UserContentBlock{
		{Type: "text", Text: &session.TextContent{Type: "text", Text: "look"}},
		{Type: "image", Image: &session.ImageContent{Type: "image", Data: "abc", MimeType: "image/png"}},
	}
	uid, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: content, Timestamp: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendProvenance(uid, "/skill:view", "skill"); err != nil {
		t.Fatal(err)
	}
	cfg := bumpConfig()
	cfg.Model.InputImage = true
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	if err := loop.Bump(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	request := fake.Requests()[0]
	imageFound := false
	for _, message := range request.Messages {
		if message.Role != "user" {
			continue
		}
		for _, block := range message.Content {
			if block.Type == "image" && block.Data == "abc" && block.MimeType == "image/png" {
				imageFound = true
			}
		}
	}
	if !imageFound {
		t.Fatal("structured image content was rebuilt or lost")
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	provenance := 0
	for _, entry := range path {
		if entry.Kind == "custom" {
			provenance++
		}
	}
	if provenance != 1 {
		t.Fatalf("bump changed provenance count: %d", provenance)
	}
}

func TestBumpRefusesUnsafeContinuation(t *testing.T) {
	t.Run("deferred earlier message", func(t *testing.T) {
		store, _, fake, loop, _ := bumpSetup(t)
		if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "old", Timestamp: 1}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
			Role: "assistant", StopReason: "deferred",
			Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "later"}}},
		}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "now", Timestamp: 2}}); err != nil {
			t.Fatal(err)
		}
		before := store.EntryCount()
		if err := loop.Bump(context.Background(), bumpConfig()); err == nil || !strings.Contains(err.Error(), "deferred") {
			t.Fatalf("Bump error=%v", err)
		}
		if fake.CallCount() != 0 || store.EntryCount() != before {
			t.Fatal("unsafe continuation reached provider or mutated the session")
		}
	})

	t.Run("completed tip with unsafe history", func(t *testing.T) {
		store, _, fake, loop, _ := bumpSetup(t)
		if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "old", Timestamp: 1}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
			Role: "assistant", StopReason: "deferred",
			Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "later"}}},
		}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "now", Timestamp: 2}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
			Role: "assistant", StopReason: "stop",
			Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "done"}}},
		}}); err != nil {
			t.Fatal(err)
		}
		before := store.EntryCount()
		if err := loop.Bump(context.Background(), bumpConfig()); !errors.Is(err, ErrNotBumpable) {
			t.Fatalf("Bump error=%v; want ErrNotBumpable", err)
		}
		if fake.CallCount() != 0 || store.EntryCount() != before {
			t.Fatal("completed tip reached provider or mutated the session")
		}
	})

	t.Run("malformed earlier content", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")
		lines := []string{
			`{"type":"session","version":3,"id":"raw","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/ws"}`,
			`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"old","timestamp":1}}`,
			`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"mystery"}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":2}}`,
			`{"type":"message","id":"u2","parentId":"a1","timestamp":"2026-01-01T00:00:03.000Z","message":{"role":"user","content":"now","timestamp":3}}`,
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		store, err := session.OpenFile(path, "/tmp/ws", true)
		if err != nil {
			t.Fatal(err)
		}
		fake := &provider.Fake{}
		loop := New(store, tools.NewRegistry("/tmp/ws"), fake, nil)
		before := store.EntryCount()
		if err := loop.Bump(context.Background(), bumpConfig()); err == nil || !strings.Contains(err.Error(), "mystery") {
			t.Fatalf("Bump error=%v", err)
		}
		if fake.CallCount() != 0 || store.EntryCount() != before {
			t.Fatal("malformed continuation reached provider or mutated the session")
		}
	})
}

func TestBumpUsesCurrentCompactionPolicy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			store, _, fake, loop, events := bumpSetup(t)
			base := bumpConfig()
			base.Model = provider.ModelMeta{ID: "m", API: "anthropic-messages", ContextWindow: 10_000, MaxTokens: 1000}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "old question", Timestamp: 1}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
				Role: "assistant", StopReason: "stop",
				Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "old answer"}}},
			}}); err != nil {
				t.Fatal(err)
			}
			fake.Errors = []error{errors.New("transport down"), nil, nil}
			fake.Responses = []*provider.Response{nil, {Text: "summary of history", StopReason: "stop"}, {Text: "final", StopReason: "stop"}}
			if err := loop.Submit(context.Background(), "current question", base); err == nil {
				t.Fatal("Submit succeeded")
			}
			next := base
			next.AutoCompact = enabled
			next.KeepRecentTokens = 1
			next.ReserveTokens = next.Model.ContextWindow
			prepared, err := loop.CheckCompaction(next)
			if err != nil {
				t.Fatal(err)
			}
			if prepared.Disposition != CompactionEligible {
				t.Fatalf("compaction disposition=%v; want eligible", prepared.Disposition)
			}
			path, err := store.ActivePath()
			if err != nil {
				t.Fatal(err)
			}
			currentUser := -1
			cut := -1
			for i, entry := range path {
				if entry.ID() == prepared.FirstKeptEntryID {
					cut = i
				}
				if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.User != nil && entry.Message.Message.User.Content == "current question" {
					currentUser = i
				}
			}
			if cut < 0 || currentUser < 0 || currentUser < cut {
				t.Fatalf("compaction cut=%d current user=%d", cut, currentUser)
			}
			usersBefore := countMessageRole(store, "user")
			callsBefore := fake.CallCount()
			*events = nil
			if err := loop.Bump(context.Background(), next); err != nil {
				t.Fatal(err)
			}
			usersAfter := countMessageRole(store, "user")
			if usersAfter != usersBefore {
				t.Fatalf("bump added a user: %d -> %d", usersBefore, usersAfter)
			}
			bumpCalls := fake.CallCount() - callsBefore
			compactions := 0
			for _, e := range *events {
				if e.Type == event.CompactionStart && e.State == event.CompactionThreshold {
					compactions++
				}
			}
			requests := fake.Requests()[callsBefore:]
			if enabled {
				if compactions != 1 || bumpCalls != 2 {
					t.Fatalf("enabled bump compactions=%d calls=%d; want 1/2", compactions, bumpCalls)
				}
				if !requestContainsText(requests[len(requests)-1], "summary of history") {
					t.Fatal("saved summary absent from answer request")
				}
			} else {
				if compactions != 0 || bumpCalls != 1 {
					t.Fatalf("disabled bump compactions=%d calls=%d; want 0/1", compactions, bumpCalls)
				}
			}
			path, err = store.ActivePath()
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range path {
				if entry.Kind == "compaction" {
					if !enabled {
						t.Fatal("disabled bump created a compaction entry")
					}
				}
				if entry.Kind == "custom" && entry.Custom != nil {
					t.Fatalf("bump added a custom/provenance entry: %+v", entry.Custom)
				}
			}
			if !requestContainsUserText(fake.Requests()[len(fake.Requests())-1], "current question") {
				t.Fatal("answer request lost the current user content")
			}
		})
	}
}

func requestContainsText(req provider.Request, want string) bool {
	for _, message := range req.Messages {
		for _, block := range message.Content {
			if strings.Contains(block.Text, want) {
				return true
			}
		}
	}
	return false
}

func requestContainsUserText(req provider.Request, want string) bool {
	for _, message := range req.Messages {
		if message.Role != "user" {
			continue
		}
		for _, block := range message.Content {
			if block.Text == want {
				return true
			}
		}
	}
	return false
}
