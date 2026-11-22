package main

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	// typingRefresh renews the typing indicator. Discord clears it after
	// about ten seconds, so the renewal is well inside that window.
	typingRefresh = 4 * time.Second
	// typingMaxDuration bounds one indicator, so a stuck turn cannot keep
	// the bot typing forever.
	typingMaxDuration = 15 * time.Minute
	// typingMaxFailures stops the indicator after repeated failures, so a
	// revoked permission cannot turn into a request loop.
	typingMaxFailures = 3
)

// typingIndicator keeps the Discord typing indicator alive for one channel
// while a turn runs.
type typingIndicator struct {
	channelID string
	refresh   time.Duration
	max       time.Duration
	send      func(channelID string) error
	logger    *log.Logger

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// startTyping shows the typing indicator in a channel and keeps it alive
// until the returned function is called. It never blocks the turn.
func startTyping(ctx context.Context, channelID string, refresh, max time.Duration, send func(string) error, logger *log.Logger) func() {
	if channelID == "" || send == nil {
		return func() {}
	}
	indicator := &typingIndicator{
		channelID: channelID,
		refresh:   refresh,
		max:       max,
		send:      send,
		logger:    logger,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go indicator.run(ctx)
	return indicator.stopTyping
}

func (t *typingIndicator) run(ctx context.Context) {
	defer close(t.done)
	failures := 0
	if t.sendOnce() {
		failures = 0
	} else {
		failures = 1
		if failures >= typingMaxFailures {
			return
		}
	}
	ticker := time.NewTicker(t.refresh)
	defer ticker.Stop()
	deadline := time.Now().Add(t.max)
	for {
		select {
		case <-t.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().After(deadline) {
				return
			}
			if t.sendOnce() {
				failures = 0
				continue
			}
			failures++
			if failures >= typingMaxFailures {
				return
			}
		}
	}
}

// sendOnce sends one typing request. It reports whether the request worked.
func (t *typingIndicator) sendOnce() bool {
	if err := t.send(t.channelID); err != nil {
		if t.logger != nil {
			t.logger.Printf("typing indicator failed (%T)", err)
		}
		return false
	}
	return true
}

// stopTyping ends the indicator. It is safe to call more than once.
func (t *typingIndicator) stopTyping() {
	t.stopOnce.Do(func() {
		close(t.stop)
	})
	<-t.done
}

const (
	// defaultPresenceActivity is the custom status the bot shows when the
	// settings do not name one. It tells a reader which front end answers.
	defaultPresenceActivity = "Psionic"
	// maxPresenceActivity bounds the custom status text.
	maxPresenceActivity = 128
)
