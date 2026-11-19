package app

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/provider"
	"psi/internal/terminal"
)

// gatedCancelProvider streams one partial turn, then holds it: the provider
// acknowledges cancellation but cannot finish until the test opens the
// release gate. That gate opens only after the test observes terminal
// release, so settlement completing proves it ran behind the release.
type gatedCancelProvider struct {
	partial     chan struct{}
	cancelled   chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (p *gatedCancelProvider) Send(ctx context.Context, _ provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	onDelta(provider.Delta{Kind: provider.DeltaText, Text: "QUIT_PARTIAL"})
	close(p.partial)
	<-ctx.Done()
	close(p.cancelled)
	<-p.release
	return nil, ctx.Err()
}

func (*gatedCancelProvider) CloseIdle() {}

func (p *gatedCancelProvider) openRelease() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func TestQuitDuringRunReleasesTerminalBeforeSettlement(t *testing.T) {
	r := testRuntime(t)
	r.authErr = nil
	p := &gatedCancelProvider{partial: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	r.prov = p
	input, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &synchronizedBuffer{}
	console := terminal.NewConsole(input, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	result := make(chan error, 1)
	go func() { result <- r.repl(console) }()
	replDone := false
	defer func() {
		// On failure, unblock the held provider and input, then join the
		// owner before closing the console; closing it under the REPL would
		// make the test race its own cleanup.
		p.openRelease()
		_ = inputWrite.Close()
		if !replDone {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Error("repl did not stop during cleanup")
			}
		}
		console.Close()
		_ = input.Close()
	}()

	startupDeadline := time.After(3 * time.Second)
	for !strings.Contains(out.String(), "(anthropic) m") {
		select {
		case <-startupDeadline:
			t.Fatalf("application did not reach the prompt: %q", out.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := inputWrite.Write([]byte("question\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.partial:
	case <-time.After(3 * time.Second):
		t.Fatalf("synthetic provider did not stream visible text: %q", out.String())
	}
	partialDeadline := time.After(3 * time.Second)
	for !strings.Contains(out.String(), "QUIT_PARTIAL") {
		select {
		case <-partialDeadline:
			t.Fatalf("partial response never became visible: %q", out.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := inputWrite.Write([]byte("/quit\r")); err != nil {
		t.Fatal(err)
	}
	const releaseMarker = "\x1b[?2004l"
	releaseDeadline := time.After(3 * time.Second)
	for !strings.Contains(out.String(), releaseMarker) {
		select {
		case <-releaseDeadline:
			t.Fatalf("terminal was never released: %q", out.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	// Settlement can only complete after the observed release: the
	// cancelled run waits on this gate.
	p.openRelease()
	select {
	case err := <-result:
		replDone = true
		if err != nil {
			t.Fatalf("repl after mid-run quit: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("repl did not return after terminal release")
	}
	select {
	case <-p.cancelled:
	default:
		t.Fatal("provider never acknowledged cancellation")
	}
	raw := out.String()
	idx := strings.LastIndex(raw, releaseMarker)
	if tail := raw[idx+len(releaseMarker):]; tail != "" {
		t.Fatalf("writes after terminal release: %q", tail)
	}
	data, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "QUIT_PARTIAL") {
		t.Fatalf("aborted partial was not persisted: %s", r.store.File())
	}
}
