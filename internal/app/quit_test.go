package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"psi/internal/provider"
	"psi/internal/terminal"
)

const quitReleaseMarker = "\x1b[?2004l"

// gatedCancelProvider exposes the context before streaming. It acknowledges
// cancellation, then holds settlement until the test opens the gate.
type gatedCancelProvider struct {
	started   chan context.Context
	cancelled chan struct{}
	settle    chan struct{}
}

func (p *gatedCancelProvider) Send(ctx context.Context, _ provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	p.started <- ctx
	onDelta(provider.Delta{Kind: provider.DeltaText, Text: "QUIT_PARTIAL"})
	<-ctx.Done()
	close(p.cancelled)
	<-p.settle
	return nil, ctx.Err()
}

func (*gatedCancelProvider) CloseIdle() {}

type quitOutput struct {
	synchronizedBuffer
	started <-chan context.Context
	notify  chan struct{}

	// Accessed only by the console owner until it returns.
	released        bool
	sawRunAtRelease bool
	cancelAtRelease error
}

func (w *quitOutput) Write(p []byte) (int, error) {
	if !w.released && strings.Contains(string(p), quitReleaseMarker) {
		w.released = true
		select {
		case ctx := <-w.started:
			w.sawRunAtRelease = true
			w.cancelAtRelease = ctx.Err()
		default:
		}
	}
	n, err := w.synchronizedBuffer.Write(p)
	select {
	case w.notify <- struct{}{}:
	default:
	}
	return n, err
}

func (w *quitOutput) wait(t *testing.T, marker string) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for !strings.Contains(w.String(), marker) {
		select {
		case <-w.notify:
		case <-deadline.C:
			t.Fatalf("terminal never showed %q", marker)
		}
	}
}

func TestQuitDuringRunReleasesTerminalBeforeSettlement(t *testing.T) {
	r := testRuntime(t)
	r.authErr = nil
	p := &gatedCancelProvider{started: make(chan context.Context, 1), cancelled: make(chan struct{}), settle: make(chan struct{})}
	r.prov = p
	input, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &quitOutput{started: p.started, notify: make(chan struct{}, 1)}
	console := terminal.NewConsole(input, out)
	geometryReads := 0
	console.SizeFunc = func() (int, int) {
		if out.released {
			geometryReads++
		}
		return 80, 24
	}
	result := make(chan error, 1)
	go func() {
		err := r.repl(console)
		console.Close()
		_ = input.Close()
		result <- err
	}()
	joined, settling := false, false
	defer func() {
		_ = inputWrite.Close()
		if !settling {
			close(p.settle)
		}
		if !joined {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Error("REPL did not stop during cleanup")
			}
		}
	}()

	out.wait(t, "(anthropic) m")
	if _, err := inputWrite.WriteString("question\r"); err != nil {
		t.Fatal(err)
	}
	out.wait(t, "QUIT_PARTIAL")
	if _, err := inputWrite.WriteString("/quit\r"); err != nil {
		t.Fatal(err)
	}
	out.wait(t, quitReleaseMarker)
	select {
	case <-p.cancelled:
	case <-time.After(8 * time.Second):
		t.Fatal("provider never acknowledged cancellation")
	}
	settling = true
	close(p.settle)
	select {
	case err := <-result:
		joined = true
		if err != nil {
			t.Fatalf("REPL after mid-run quit: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("REPL did not return after terminal release")
	}
	if !out.sawRunAtRelease {
		t.Fatal("terminal was released without an owned run")
	}
	if out.cancelAtRelease != nil {
		t.Fatalf("provider was cancelled before terminal restoration: %v", out.cancelAtRelease)
	}
	_, tail, _ := strings.Cut(out.String(), quitReleaseMarker)
	if tail != "" {
		t.Errorf("writes after terminal release: %q", tail)
	}
	if geometryReads != 0 {
		t.Errorf("queried terminal geometry %d times after release", geometryReads)
	}
	data, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "QUIT_PARTIAL") {
		t.Fatalf("aborted partial was not persisted: %s", r.store.File())
	}
}
