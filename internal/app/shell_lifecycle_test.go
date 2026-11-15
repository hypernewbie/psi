package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/terminal"
	"github.com/hypernewbie/psi/internal/tools"
)

type shellOutput struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	updates chan struct{}
}

func newShellOutput() *shellOutput { return &shellOutput{updates: make(chan struct{}, 1)} }

func (w *shellOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buffer.Write(p)
	w.mu.Unlock()
	select {
	case w.updates <- struct{}{}:
	default:
	}
	return n, err
}

func (w *shellOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func (w *shellOutput) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Len()
}

func (w *shellOutput) waitContainsFrom(t *testing.T, want string, offset int) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		text := w.String()
		if offset <= len(text) && strings.Contains(text[offset:], want) {
			return
		}
		select {
		case <-w.updates:
		case <-deadline.C:
			t.Fatalf("timeout waiting for %q after offset %d: %q", want, offset, text)
		}
	}
}

func (w *shellOutput) waitContains(t *testing.T, want string) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		if strings.Contains(w.String(), want) {
			return
		}
		select {
		case <-w.updates:
		case <-deadline.C:
			t.Fatalf("timeout waiting for %q: %q", want, w.String())
		}
	}
}

func startShellTestREPL(t *testing.T, r *runtime) (*os.File, *shellWriter, *terminal.Console, <-chan error) {
	t.Helper()
	in, input, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &shellWriter{shellOutput: newShellOutput()}
	console := terminal.NewConsole(in, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	finished := make(chan error, 1)
	go func() { finished <- r.repl(console) }()
	t.Cleanup(func() {
		console.Close()
		_ = in.Close()
		_ = input.Close()
	})
	return input, out, console, finished
}

func waitShellRepl(t *testing.T, finished <-chan error) error {
	t.Helper()
	select {
	case err := <-finished:
		return err
	case <-time.After(8 * time.Second):
		t.Fatal("REPL did not settle")
		return nil
	}
}

func waitShellSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(8 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}

func intPtr(value int) *int { return &value }

func shellMessages(store *session.Store) []*session.BashExecutionMessage {
	var out []*session.BashExecutionMessage
	for _, entry := range store.Entries() {
		if entry.Message != nil && entry.Message.Message.BashExecution != nil {
			out = append(out, entry.Message.Message.BashExecution)
		}
	}
	return out
}

func userMessages(store *session.Store) int {
	count := 0
	for _, entry := range store.Entries() {
		if entry.Message != nil && entry.Message.Message.Role == "user" {
			count++
		}
	}
	return count
}

type shellBlockingProvider struct {
	started chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (p *shellBlockingProvider) Send(ctx context.Context, _ provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *shellBlockingProvider) CloseIdle() {}

type shellExitCase struct {
	name       string
	action     func(t *testing.T, input *os.File, signals chan os.Signal, output *shellWriter)
	wantErr    bool
	suspendErr bool
}

func TestBangShellMVPLifecycle(t *testing.T) {
	t.Run("immediate fake runner saves once without provider", func(t *testing.T) {
		r := testRuntime(t)
		fake := &provider.Fake{}
		r.prov = fake
		var starts atomic.Int32
		r.shellRunner = func(_ context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
			starts.Add(1)
			onOutput([]byte("M1_MARKER\n"))
			return tools.BashRunResult{Output: "M1_MARKER\n", ExitCode: intPtr(0)}
		}
		input, out, _, finished := startShellTestREPL(t, r)
		if _, err := input.WriteString("!printf M1_MARKER\r"); err != nil {
			t.Fatal(err)
		}
		out.waitContains(t, "M1_MARKER")
		if _, err := input.WriteString("/quit\r"); err != nil {
			t.Fatal(err)
		}
		if err := waitShellRepl(t, finished); err != nil {
			t.Fatal(err)
		}
		if starts.Load() != 1 || len(fake.Calls) != 0 || userMessages(r.store) != 0 {
			t.Fatalf("starts=%d provider=%d users=%d", starts.Load(), len(fake.Calls), userMessages(r.store))
		}
		messages := shellMessages(r.store)
		if len(messages) != 1 {
			t.Fatalf("shell messages = %d", len(messages))
		}
		if messages[0].Command != "printf M1_MARKER" || messages[0].Output != "M1_MARKER\n" || messages[0].ExitCode == nil || *messages[0].ExitCode != 0 {
			t.Fatalf("saved message = %+v", messages[0])
		}
	})

	t.Run("refuses while a model run owns the runtime and restores the draft", func(t *testing.T) {
		r := testRuntime(t)
		blocking := &shellBlockingProvider{started: make(chan struct{})}
		r.prov = blocking
		r.authErr = nil
		var starts atomic.Int32
		r.shellRunner = func(_ context.Context, _ *tools.BashTool, _ string, _ func([]byte)) tools.BashRunResult {
			starts.Add(1)
			return tools.BashRunResult{ExitCode: intPtr(0)}
		}
		input, out, _, finished := startShellTestREPL(t, r)
		if _, err := input.WriteString("hold the run\r"); err != nil {
			t.Fatal(err)
		}
		waitShellSignal(t, blocking.started, "provider start")
		draftStart := out.Len()
		if _, err := input.WriteString("!echo refused\r"); err != nil {
			t.Fatal(err)
		}
		out.waitContains(t, "Finish or cancel the current operation")
		out.waitContainsFrom(t, "!echo refused", draftStart)
		if starts.Load() != 0 || len(shellMessages(r.store)) != 0 {
			t.Fatal("refused shell started or saved")
		}
		if _, err := input.WriteString("\x1b"); err != nil {
			t.Fatal(err)
		}
		out.waitContains(t, "Request cancelled")
		if _, err := input.WriteString("\x03/quit\r"); err != nil {
			t.Fatal(err)
		}
		if err := waitShellRepl(t, finished); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("hotkeys and bridge readiness are fenced while the shell owns the runtime", func(t *testing.T) {
		owned := testRuntime(t)
		owned.state, owned.shellActive = StateIdle, true
		snap := bridgeSnapshot(owned)
		if snap.Compact.Idle || snap.Reload.Idle || !snap.Compact.HasPending {
			t.Fatalf("bridge readiness ignored shell ownership: %+v %+v", snap.Compact, snap.Reload)
		}
		r := testRuntime(t)
		r.authErr = nil
		started := make(chan struct{}, 1)
		cancelled := make(chan struct{}, 1)
		release := make(chan struct{})
		r.shellRunner = func(ctx context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
			started <- struct{}{}
			<-ctx.Done()
			cancelled <- struct{}{}
			<-release
			onOutput([]byte("HOTKEY_PARTIAL\n"))
			return tools.BashRunResult{Output: "HOTKEY_PARTIAL\n", Cancelled: true}
		}
		input, out, _, finished := startShellTestREPL(t, r)
		if _, err := input.WriteString("!sleep 30\r"); err != nil {
			t.Fatal(err)
		}
		waitShellSignal(t, started, "shell start")
		model, thinking := r.ov.Model, r.ov.Thinking
		if _, err := input.WriteString("\x10\x0c\x1b[Z"); err != nil {
			t.Fatal(err)
		}
		if _, err := input.WriteString("/model other\r"); err != nil {
			t.Fatal(err)
		}
		out.waitContains(t, "Finish or cancel the shell command")
		if r.selector != nil || r.ov.Model != model || r.ov.Thinking != thinking {
			t.Fatalf("shell-owned hotkey mutated state: selector=%v model=%q thinking=%q", r.selector, r.ov.Model, r.ov.Thinking)
		}
		if _, err := input.WriteString("\x1b"); err != nil {
			t.Fatal(err)
		}
		waitShellSignal(t, cancelled, "shell cancellation")
		close(release)
		out.waitContains(t, "(cancelled)")
		if _, err := input.WriteString("\x03/quit\r"); err != nil {
			t.Fatal(err)
		}
		if err := waitShellRepl(t, finished); err != nil {
			t.Fatal(err)
		}
		if messages := shellMessages(r.store); len(messages) != 1 || !messages[0].Cancelled {
			t.Fatalf("hotkey shell message = %+v", messages)
		}
	})

	t.Run("exit paths cancel, join, consume the result, and append once", func(t *testing.T) {
		cases := []shellExitCase{
			{name: "quit", action: func(t *testing.T, input *os.File, _ chan os.Signal, _ *shellWriter) {
				if _, err := input.WriteString("/quit\r"); err != nil {
					t.Fatal(err)
				}
			}},
			{name: "eof", action: func(t *testing.T, input *os.File, _ chan os.Signal, _ *shellWriter) {
				_ = input.Close()
			}},
			{name: "ctrl-d", action: func(t *testing.T, input *os.File, _ chan os.Signal, _ *shellWriter) {
				if _, err := input.WriteString("\x04"); err != nil {
					t.Fatal(err)
				}
			}},
			{name: "shutdown", action: func(t *testing.T, _ *os.File, signals chan os.Signal, _ *shellWriter) {
				signals <- syscall.SIGTERM
			}},
			{name: "terminal write failure", action: func(t *testing.T, input *os.File, _ chan os.Signal, output *shellWriter) {
				output.Fail()
				if _, err := input.WriteString("\x14"); err != nil {
					t.Fatal(err)
				}
			}, wantErr: true},
		}
		if shellSuspendCase != nil {
			cases = append(cases, *shellSuspendCase)
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				r := testRuntime(t)
				r.signals = make(chan os.Signal, 2)
				fake := &provider.Fake{}
				r.prov = fake
				started := make(chan struct{}, 1)
				cancelled := make(chan struct{}, 1)
				release := make(chan struct{})
				var starts atomic.Int32
				r.shellRunner = func(ctx context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
					starts.Add(1)
					started <- struct{}{}
					<-ctx.Done()
					cancelled <- struct{}{}
					<-release
					onOutput([]byte("PARTIAL\n"))
					return tools.BashRunResult{Output: "PARTIAL\n", Cancelled: true}
				}
				input, out, _, finished := startShellTestREPL(t, r)
				if _, err := input.WriteString("!sleep 30\r"); err != nil {
					t.Fatal(err)
				}
				waitShellSignal(t, started, "shell start")
				writer := out
				tc.action(t, input, r.signals, writer)
				waitShellSignal(t, cancelled, "shell cancellation")
				select {
				case err := <-finished:
					t.Fatalf("REPL returned before settlement: %v", err)
				default:
				}
				if len(shellMessages(r.store)) != 0 {
					t.Fatal("shell entry saved before settlement")
				}
				close(release)
				err := waitShellRepl(t, finished)
				if tc.wantErr || tc.suspendErr {
					if err == nil {
						t.Fatal("expected settlement error")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if len(fake.Calls) != 0 || starts.Load() != 1 {
					t.Fatalf("provider=%d starts=%d", len(fake.Calls), starts.Load())
				}
				messages := shellMessages(r.store)
				if len(messages) != 1 || !messages[0].Cancelled || messages[0].Output != "PARTIAL\n" || messages[0].ExitCode != nil {
					t.Fatalf("cancelled message = %+v", messages)
				}
				if userMessages(r.store) != 0 {
					t.Fatal("cancellation wrote a user message")
				}
			})
		}
	})

	t.Run("nil provider and registry still run a shell and refuse a prompt", func(t *testing.T) {
		r := testRuntime(t)
		r.prov, r.reg = nil, nil
		r.authErr = errors.New("unsupported imported model")
		var starts atomic.Int32
		r.shellRunner = func(_ context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
			starts.Add(1)
			onOutput([]byte("NIL_PROVIDER_OK\n"))
			return tools.BashRunResult{Output: "NIL_PROVIDER_OK\n", ExitCode: intPtr(0)}
		}
		input, out, _, finished := startShellTestREPL(t, r)
		if _, err := input.WriteString("!marker\r"); err != nil {
			t.Fatal(err)
		}
		out.waitContains(t, "NIL_PROVIDER_OK")
		if _, err := input.WriteString("ordinary prompt\r"); err != nil {
			t.Fatal(err)
		}
		out.waitContains(t, "unsupported imported model")
		if _, err := input.WriteString("\x03/quit\r"); err != nil {
			t.Fatal(err)
		}
		if err := waitShellRepl(t, finished); err != nil {
			t.Fatal(err)
		}
		if starts.Load() != 1 || len(shellMessages(r.store)) != 1 || userMessages(r.store) != 0 {
			t.Fatalf("starts=%d shells=%d users=%d", starts.Load(), len(shellMessages(r.store)), userMessages(r.store))
		}
	})

	t.Run("failed durable append blocks later shells", func(t *testing.T) {
		r := testRuntime(t)
		var starts atomic.Int32
		r.shellRunner = func(_ context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
			starts.Add(1)
			onOutput([]byte("SAVED_FIRST\n"))
			return tools.BashRunResult{Output: "SAVED_FIRST\n", ExitCode: intPtr(0)}
		}
		input, out, _, finished := startShellTestREPL(t, r)
		if _, err := input.WriteString("!first\r"); err != nil {
			t.Fatal(err)
		}
		out.waitContains(t, "SAVED_FIRST")
		leaf := r.store.LeafID()
		file, err := os.OpenFile(r.store.File(), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("{}\n"); err != nil {
			t.Fatal(err)
		}
		file.Close()
		if _, err := input.WriteString("!second\r"); err != nil {
			t.Fatal(err)
		}
		out.waitContains(t, "UNSAVED")
		if r.store.WriteError() == nil {
			t.Fatal("append failure was not sticky")
		}
		if r.store.LeafID() != leaf {
			t.Fatal("failed append moved the leaf")
		}
		before := starts.Load()
		if _, err := input.WriteString("!third\r"); err != nil {
			t.Fatal(err)
		}
		out.waitContains(t, "Finish or cancel the current operation")
		if starts.Load() != before {
			t.Fatal("blocked storage started another shell")
		}
		if _, err := input.WriteString("\x03/quit\r"); err != nil {
			t.Fatal(err)
		}
		_ = waitShellRepl(t, finished)
		if len(shellMessages(r.store)) != 1 {
			t.Fatalf("shell entries = %d", len(shellMessages(r.store)))
		}
	})
}

type shellWriter struct {
	*shellOutput
	fail atomic.Bool
}

func (w *shellWriter) Write(p []byte) (int, error) {
	if w.fail.Load() {
		return 0, fmt.Errorf("synthetic terminal write failure")
	}
	return w.shellOutput.Write(p)
}

func (w *shellWriter) Fail() { w.fail.Store(true) }
