package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/terminal"
)

func appendBumpUser(t *testing.T, store *session.Store, text string) {
	t.Helper()
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: text, Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
}

func appendBumpAssistant(t *testing.T, store *session.Store, stop, text string) {
	t.Helper()
	message := session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: stop,
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: text}}},
	}}
	if _, err := store.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
}

func TestBumpAdmission(t *testing.T) {
	shared := []struct {
		name      string
		prepare   func(*testing.T, *runtime)
		unavail   bool
		want      error
		wantText  string
		wantState State
	}{
		{
			name: "eligible user tip",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
			},
		},
		{
			name: "pending compaction",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.compactPending = true
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "active compaction mirror",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.compactActive = true
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "busy run",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
			},
			unavail: true,
			want:    agent.ErrNotBumpable,
		},
		{
			name: "compaction state",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.state = StateCompacting
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "selector owns input",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.selector = &Selector{}
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "settings picker owns input",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.settingsPicker = &terminal.SettingsPicker{}
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "auth unavailable",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.authErr = errors.New("no synthetic credential")
			},
			wantText: "no synthetic credential",
		},
		{
			name: "offline state without auth error",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.state = StateOffline
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "storage blocked without sticky error",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.state = StateStorageBlocked
			},
			wantText:  "storage",
			wantState: StateStorageBlocked,
		},
		{
			name: "changed durable file",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				if _, err := r.store.Projection(); err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(r.store.File(), os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString("\n"); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			},
			wantText:  "storage",
			wantState: StateStorageBlocked,
		},
		{
			name: "unsafe continuation",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "old")
				appendBumpAssistant(t, r.store, "deferred", "later")
				appendBumpUser(t, r.store, "now")
			},
			wantText: "deferred",
		},
	}
	policy := []struct {
		name              string
		prepare           func(*testing.T, *runtime)
		requireUnfinished bool
		want              error
		wantText          string
	}{
		{
			name: "completed tip empty Enter",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				appendBumpAssistant(t, r.store, "stop", "done")
			},
			requireUnfinished: true,
			want:              agent.ErrNotBumpable,
		},
		{
			name: "completed tip explicit",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				appendBumpAssistant(t, r.store, "stop", "done")
			},
			requireUnfinished: false,
		},
		{
			name: "length tip empty Enter",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				appendBumpAssistant(t, r.store, "length", "cut")
			},
			requireUnfinished: true,
			want:              agent.ErrNotBumpable,
		},
		{
			name: "length tip explicit",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				appendBumpAssistant(t, r.store, "length", "cut")
			},
			requireUnfinished: false,
		},
		{
			name: "completed tip with unsafe history empty Enter",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "old")
				appendBumpAssistant(t, r.store, "deferred", "later")
				appendBumpUser(t, r.store, "now")
				appendBumpAssistant(t, r.store, "stop", "done")
			},
			requireUnfinished: true,
			want:              agent.ErrNotBumpable,
		},
		{
			name: "completed tip with unsafe history explicit",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "old")
				appendBumpAssistant(t, r.store, "deferred", "later")
				appendBumpUser(t, r.store, "now")
				appendBumpAssistant(t, r.store, "stop", "done")
			},
			requireUnfinished: false,
			wantText:          "deferred",
		},
	}
	check := func(t *testing.T, r *runtime, fake *provider.Fake, before int, err error, want error, wantText string, wantState State) {
		t.Helper()
		if want != nil {
			if !errors.Is(err, want) {
				t.Fatalf("checkBump error=%v; want %v", err, want)
			}
		} else if wantText != "" {
			if err == nil || !strings.Contains(err.Error(), wantText) {
				t.Fatalf("checkBump error=%v; want %q", err, wantText)
			}
			if errors.Is(err, agent.ErrNotBumpable) {
				t.Fatal("storage/auth refusal reported as informational no-op")
			}
		} else if err != nil {
			t.Fatalf("checkBump error=%v; want eligible", err)
		}
		if fake.CallCount() != 0 {
			t.Fatal("refused check called the provider")
		}
		if r.store.EntryCount() != before {
			t.Fatal("refused check mutated the session")
		}
		if wantState != "" && r.state != wantState {
			t.Fatalf("state=%q; want %q", r.state, wantState)
		}
	}
	for _, tc := range shared {
		t.Run(tc.name, func(t *testing.T) {
			for _, admission := range []struct {
				name              string
				requireUnfinished bool
			}{
				{"empty Enter", true},
				{"explicit", false},
			} {
				t.Run(admission.name, func(t *testing.T) {
					r := testRuntime(t)
					r.state = StateIdle
					r.authErr = nil
					fake := &provider.Fake{}
					r.prov = fake
					tc.prepare(t, r)
					before := r.store.EntryCount()
					err := r.checkBump(tc.unavail, admission.requireUnfinished)
					check(t, r, fake, before, err, tc.want, tc.wantText, tc.wantState)
				})
			}
		})
	}
	for _, tc := range policy {
		t.Run(tc.name, func(t *testing.T) {
			r := testRuntime(t)
			r.state = StateIdle
			r.authErr = nil
			fake := &provider.Fake{}
			r.prov = fake
			tc.prepare(t, r)
			before := r.store.EntryCount()
			err := r.checkBump(false, tc.requireUnfinished)
			check(t, r, fake, before, err, tc.want, tc.wantText, "")
		})
	}
}

func waitBumpOutput(t *testing.T, out *notifyingBuffer, want string) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		if strings.Contains(out.String(), want) {
			return
		}
		select {
		case <-out.notify:
		case <-deadline:
			t.Fatalf("timeout waiting for %q: %q", want, out.String())
		}
	}
}

// startBumpREPL owns the console and REPL goroutine. EOF cancels an active
// provider on failed assertions; cleanup waits before closing the console or
// allowing testRuntime to release the session lock.
func startBumpREPL(t *testing.T, r *runtime) (*terminal.Console, *notifyingBuffer, func(string), func()) {
	t.Helper()
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &notifyingBuffer{notify: make(chan struct{}, 1)}
	console := terminal.NewConsole(input, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	done := make(chan struct{})
	var replErr error
	go func() {
		defer close(done)
		replErr = r.repl(console)
	}()
	t.Cleanup(func() {
		_ = writer.Close()
		select {
		case <-done:
			console.Close()
		case <-time.After(8 * time.Second):
			t.Error("REPL did not exit after EOF")
		}
		_ = input.Close()
	})
	send := func(text string) {
		t.Helper()
		if _, err := writer.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	finish := func() {
		t.Helper()
		select {
		case <-done:
			if replErr != nil {
				t.Fatalf("REPL exit error=%v", replErr)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("REPL did not exit")
		}
	}
	waitBumpOutput(t, out, "(anthropic) m")
	return console, out, send, finish
}

func TestBumpAdmissionRefusalsStaySilent(t *testing.T) {
	for _, refusal := range []string{"auth", "storage"} {
		for _, trigger := range []string{"\r", "/b\r", "/bump\r"} {
			t.Run(refusal+"/"+strings.TrimSuffix(trigger, "\r"), func(t *testing.T) {
				r := testRuntime(t)
				r.state = StateIdle
				r.authErr = nil
				fake := &provider.Fake{}
				r.prov = fake
				appendBumpUser(t, r.store, "hello")
				refusalErr := errors.New("synthetic-bump-admission-refusal")
				if refusal == "auth" {
					r.authErr = refusalErr
				}
				_, out, send, finish := startBumpREPL(t, r)
				if refusal == "storage" {
					r.store.FailWritesForTest(refusalErr)
				}
				beforeEntries := r.store.EntryCount()
				beforeOutput := out.String()
				send(trigger + "/hotkeys\r")
				waitBumpOutput(t, out, "Keys (fixed, no Kitty required):")
				segment := strings.TrimPrefix(out.String(), beforeOutput)
				// The refusal type can occur in a footer path. Only the injected
				// error reason proves that an admission notice escaped.
				if strings.Contains(segment, refusalErr.Error()) {
					t.Fatalf("%q reported %q; want silence: %q", trigger, refusalErr, segment)
				}
				send("/quit\r")
				finish()
				if fake.CallCount() != 0 || r.store.EntryCount() != beforeEntries {
					t.Fatal("refused bump reached provider or mutated the session")
				}
				if refusal == "storage" && r.state != StateStorageBlocked {
					t.Fatalf("state=%q; want storage blocked", r.state)
				}
			})
		}
	}
}

// settleNotifyProvider wraps a fake provider and notifies when the REPL clears
// the retry observer at settlement entry. The notification proves the done
// branch started, not that settlement completed; a following sentinel proves
// the REPL processed the next command.
type settleNotifyProvider struct {
	provider.Provider
	notified chan struct{}
}

func (p *settleNotifyProvider) SetRetryObserver(observer func(provider.RetryInfo)) {
	if observer == nil {
		select {
		case p.notified <- struct{}{}:
		default:
		}
	}
}

func waitBumpSettlement(t *testing.T, notified <-chan struct{}) {
	t.Helper()
	select {
	case <-notified:
	case <-time.After(8 * time.Second):
		t.Fatal("bump settlement did not start")
	}
}

func TestBumpAsyncInvalidRequestStaysSilent(t *testing.T) {
	r := testRuntime(t)
	r.state = StateIdle
	r.authErr = nil
	appendBumpUser(t, r.store, "hello")
	r.cfg.Model.API = "unsupported-api"
	fake := &provider.Fake{}
	prov := &settleNotifyProvider{Provider: fake, notified: make(chan struct{}, 1)}
	r.prov = prov
	_, out, send, finish := startBumpREPL(t, r)
	beforeEntries := r.store.EntryCount()
	beforeOutput := out.String()
	send("/b\r")
	waitBumpSettlement(t, prov.notified)
	send("/hotkeys\r")
	waitBumpOutput(t, out, "Keys (fixed, no Kitty required):")
	segment := strings.TrimPrefix(out.String(), beforeOutput)
	if strings.Contains(strings.ToLower(segment), "unsupported") {
		t.Fatalf("async bump refusal reported an error; want silence: %q", segment)
	}
	if fake.CallCount() != 0 || r.store.EntryCount() != beforeEntries {
		t.Fatal("async bump reached provider or mutated the session")
	}
	send("second try\r")
	waitBumpOutput(t, out, "unsupported-api")
	send("/quit\r")
	finish()
	if fake.CallCount() != 0 || r.store.EntryCount() != beforeEntries {
		t.Fatal("invalid prompt reached provider or mutated the session")
	}
}

func TestBumpProviderFailureAfterRunStartShowsError(t *testing.T) {
	r := testRuntime(t)
	r.state = StateIdle
	r.authErr = nil
	appendBumpUser(t, r.store, "hello")
	appendBumpAssistant(t, r.store, "stop", "done")
	fake := &provider.Fake{Errors: []error{errors.New("synthetic-provider-down")}}
	r.prov = fake
	_, out, send, finish := startBumpREPL(t, r)
	send("/bump\r")
	waitBumpOutput(t, out, "synthetic-provider-down")
	send("/quit\r")
	finish()
	if fake.CallCount() != 1 {
		t.Fatalf("failed bump called provider %d times; want 1", fake.CallCount())
	}
}

// blockingBumpProvider blocks one run until released, proving busy aliases are
// discarded while the provider stays occupied.
type blockingBumpProvider struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
	enter   sync.Once
	free    sync.Once
}

func (p *blockingBumpProvider) Send(ctx context.Context, _ provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	p.enter.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return &provider.Response{Text: "ANSWER", StopReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *blockingBumpProvider) CloseIdle() {}

func (p *blockingBumpProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *blockingBumpProvider) freeRun() {
	p.free.Do(func() { close(p.release) })
}

func TestBumpBusyAliasesAreDiscarded(t *testing.T) {
	r := testRuntime(t)
	r.state = StateIdle
	r.authErr = nil
	prov := &blockingBumpProvider{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(prov.freeRun)
	settlement := &settleNotifyProvider{Provider: prov, notified: make(chan struct{}, 1)}
	r.prov = settlement
	console, out, send, finish := startBumpREPL(t, r)
	send("hello\r")
	select {
	case <-prov.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("run did not enter the provider")
	}
	// The fresh status sentinel proves both aliases were consumed while the
	// provider stayed blocked, without a restored draft corrupting the command.
	send("/b\r/bump\r/ac status\r")
	waitBumpOutput(t, out, "pi-ac:")
	prov.freeRun()
	waitBumpSettlement(t, settlement.notified)
	// ANSWER alone is not settlement: its delta can precede the done branch.
	send("/hotkeys\r")
	waitBumpOutput(t, out, "Keys (fixed, no Kitty required):")
	send("/quit\r")
	finish()
	if got := prov.CallCount(); got != 1 {
		t.Fatalf("provider calls=%d; want only the original prompt", got)
	}
	users, customs := 0, 0
	path, err := r.store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range path {
		if entry.Kind == "custom" {
			customs++
			continue
		}
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.User != nil {
			users++
		}
	}
	if users != 1 || customs != 0 {
		t.Fatalf("busy bumps changed session: users=%d provenance=%d; want 1/0", users, customs)
	}
	if got := console.Editor.Text(); got != "" {
		t.Fatalf("settled editor holds %q; want empty", got)
	}
}
