package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestTypingIndicatorKeepsRenewing proves the indicator is sent at once and
// renewed while the turn runs.
func TestTypingIndicatorKeepsRenewing(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	stop := startTyping(context.Background(), "channel-1", 10*time.Millisecond, time.Minute, func(channelID string) error {
		mu.Lock()
		calls = append(calls, channelID)
		mu.Unlock()
		return nil
	}, nil)
	time.Sleep(60 * time.Millisecond)
	stop()
	mu.Lock()
	got := len(calls)
	mu.Unlock()
	if got < 2 {
		t.Fatalf("typing requests = %d", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, channelID := range calls {
		if channelID != "channel-1" {
			t.Fatalf("channel = %q", channelID)
		}
	}
}

// TestTypingIndicatorStopsAfterFailures proves a revoked permission cannot
// turn into a request loop.
func TestTypingIndicatorStopsAfterFailures(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	stop := startTyping(context.Background(), "channel-1", 5*time.Millisecond, time.Minute, func(string) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return errors.New("synthetic failure")
	}, nil)
	time.Sleep(80 * time.Millisecond)
	stop()
	mu.Lock()
	got := calls
	mu.Unlock()
	if got > typingMaxFailures {
		t.Fatalf("typing requests = %d", got)
	}
}

// TestTypingIndicatorEndsWithTheContext proves a cancelled turn stops the
// indicator.
func TestTypingIndicatorEndsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	calls := 0
	stop := startTyping(ctx, "channel-1", 5*time.Millisecond, time.Minute, func(string) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}, nil)
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)
	stop()
	mu.Lock()
	before := calls
	mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	after := calls
	mu.Unlock()
	if after != before {
		t.Fatalf("typing continued after the cancellation: %d -> %d", before, after)
	}
}

// TestTypingIndicatorIsBoundedByItsDeadline proves the indicator gives up
// after its maximum duration.
func TestTypingIndicatorIsBoundedByItsDeadline(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	stop := startTyping(context.Background(), "channel-1", 5*time.Millisecond, 40*time.Millisecond, func(string) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}, nil)
	time.Sleep(120 * time.Millisecond)
	stop()
	mu.Lock()
	got := calls
	mu.Unlock()
	if got > 20 {
		t.Fatalf("typing requests = %d", got)
	}
}

// TestTypingIndicatorIgnoresAnEmptyChannel proves no request is made without
// a channel.
func TestTypingIndicatorIgnoresAnEmptyChannel(t *testing.T) {
	called := false
	stop := startTyping(context.Background(), "", time.Millisecond, time.Minute, func(string) error {
		called = true
		return nil
	}, nil)
	stop()
	if called {
		t.Fatal("a typing request was sent without a channel")
	}
}

// TestConversationShowsTheWorkingIndicator proves an ordinary turn starts
// the indicator in its channel.
func TestConversationShowsTheWorkingIndicator(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	tr := &recordingTransport{}
	rt := testRuntime(t, workspace, state, tr, &fakeFactory{replies: []string{"reply"}}, owner)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	chat.handle(sourceTurn{message: dm, text: "hello", replyTo: dm.MessageID})

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.typing) != 1 || tr.typing[0] != dm.ChannelID {
		t.Fatalf("typing = %v", tr.typing)
	}
}

// TestCommandTurnsDoNotShowTheWorkingIndicator proves a command reply, which
// is immediate, does not flash the indicator.
func TestCommandTurnsDoNotShowTheWorkingIndicator(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	tr := &recordingTransport{}
	rt := testRuntime(t, workspace, state, tr, &fakeFactory{}, owner)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	chat.handle(sourceTurn{message: dm, text: "/status"})
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.typing) != 0 {
		t.Fatalf("typing = %v", tr.typing)
	}
}
