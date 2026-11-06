package agent

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"psi/internal/provider"
	"psi/internal/session"
)

// gatedProvider holds the first request until released, so a test can steer
// while a run is genuinely in flight.
type gatedProvider struct {
	release chan struct{}
	once    sync.Once

	mu      sync.Mutex
	calls   []provider.Request
	started chan struct{}
}

func newGatedProvider() *gatedProvider {
	return &gatedProvider{release: make(chan struct{}), started: make(chan struct{}, 1)}
}

func (p *gatedProvider) Send(ctx context.Context, req provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	first := len(p.calls) == 1
	p.mu.Unlock()
	if !first {
		return &provider.Response{Text: "second", StopReason: "stop"}, nil
	}
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &provider.Response{Text: "first", StopReason: "stop"}, nil
}

func (p *gatedProvider) CloseIdle() {}

func (p *gatedProvider) requests() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Request(nil), p.calls...)
}

func (p *gatedProvider) unlock() { p.once.Do(func() { close(p.release) }) }

// A held run accepts a steered image, and the next provider request carries
// the same decoded bytes. No second run starts, and the queue drains.
func TestSteeredImageReachesTheNextProviderRequestMidRun(t *testing.T) {
	store, reg, _, _, _ := testSetup(t)
	fake := newGatedProvider()
	defer fake.unlock()
	loop := New(store, reg, fake, nil)
	cfg := Config{Model: provider.ModelMeta{ID: "claude-x", API: "anthropic-messages", MaxTokens: 1000, InputImage: true}, Thinking: "off", System: "base"}

	runErr := make(chan error, 1)
	go func() { runErr <- loop.Submit(context.Background(), "go", cfg) }()

	select {
	case <-fake.started:
	case <-time.After(10 * time.Second):
		t.Fatal("run never reached the provider")
	}
	loop.SteerContent(imageInput("Attached: a.png"))
	if got := loop.PendingContentBytes(); got != uint64(len(testImageBytes)) {
		t.Fatalf("pending bytes = %d, want %d", got, len(testImageBytes))
	}
	fake.unlock()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not settle")
	}

	requests := fake.requests()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want exactly the second request", len(requests))
	}
	want := base64.StdEncoding.EncodeToString(testImageBytes)
	found := false
	for _, message := range requests[1].Messages {
		for _, block := range message.Content {
			if block.Type == "image" && block.Data == want && block.MimeType == "image/png" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("steered image never reached the next provider request")
	}
	if got := loop.PendingContentBytes(); got != 0 {
		t.Fatalf("pending bytes after delivery = %d, want 0", got)
	}
	if got := loop.PendingSteering(); len(got) != 0 {
		t.Fatalf("steering still queued: %q", got)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	images := 0
	for _, entry := range path {
		if entry.Kind != "message" || entry.Message.Message.User == nil {
			continue
		}
		blocks, _ := entry.Message.Message.User.Content.([]session.UserContentBlock)
		for _, block := range blocks {
			if block.Image != nil {
				images++
			}
		}
	}
	if images != 1 {
		t.Fatalf("durable user images = %d, want exactly the steered one", images)
	}
}
