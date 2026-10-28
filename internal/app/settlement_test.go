package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/terminal"
)

type failingPersistProvider struct {
	store    *session.Store
	produced chan struct{}
}

func (p *failingPersistProvider) Send(_ context.Context, _ provider.Request, onText func(string)) (*provider.Response, error) {
	text := "visible before write failure"
	onText(text)
	p.store.FailWritesForTest(errors.New("injected response append failure"))
	close(p.produced)
	return &provider.Response{Text: text, StopReason: "stop"}, nil
}

func (*failingPersistProvider) CloseIdle() {}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestVisibleDeltasThatFailPersistenceShowUnsavedNotice(t *testing.T) {
	r := testRuntime(t)
	r.authErr = nil
	provider := &failingPersistProvider{store: r.store, produced: make(chan struct{})}
	r.prov = provider
	input, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputWrite.Close()
	defer input.Close()
	out := &synchronizedBuffer{}
	console := terminal.NewConsole(input, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	if !console.Editor.Insert("question") {
		t.Fatal("could not install synthetic draft")
	}
	defer console.Close()

	result := make(chan error, 1)
	go func() { result <- r.repl(console) }()
	startupDeadline := time.After(3 * time.Second)
	for !strings.Contains(out.String(), "(anthropic) m") {
		select {
		case <-startupDeadline:
			t.Fatalf("application did not reach the prompt: %q", out.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := inputWrite.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.produced:
	case <-time.After(3 * time.Second):
		select {
		case runErr := <-result:
			t.Fatalf("repl returned before provider start: err=%v output=%q", runErr, out.String())
		default:
		}
		t.Fatalf("synthetic provider did not stream visible text: state=%s output=%q", r.state, out.String())
	}
	deadline := time.After(3 * time.Second)
	for !strings.Contains(out.String(), "UNSAVED output") {
		select {
		case <-deadline:
			t.Fatalf("missing explicit unsaved notice; output=%q", out.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !strings.Contains(out.String(), "do not continue this session") || strings.Contains(out.String(), "operation did not complete") {
		t.Fatalf("notice did not distinguish unsaved output: %q", out.String())
	}
	if _, err := inputWrite.Write([]byte("/quit\r")); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl after storage failure: %v", err)
	}
	if r.state != StateStorageBlocked {
		t.Fatalf("storage failure left app in state %q", r.state)
	}
}
