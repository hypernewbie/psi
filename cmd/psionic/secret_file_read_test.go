package main

import (
	"io"
	"sync"
	"testing"
	"time"
)

type blockedSecretReader struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (r *blockedSecretReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.closed
	return 0, io.EOF
}

func (r *blockedSecretReader) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func TestSecretReadTimeoutClosesTheHandle(t *testing.T) {
	reader := &blockedSecretReader{started: make(chan struct{}), closed: make(chan struct{})}
	timedOut := make(chan time.Time)
	result := make(chan error, 1)
	go func() {
		_, err := readSecretHandleUntil(reader, 100, timedOut, func() bool {
			t.Error("timed-out read checked final identity")
			return true
		})
		result <- err
	}()
	<-reader.started
	close(timedOut) // Explicitly signal the timer; this test does not wait for a clock.
	if err := <-result; err == nil {
		t.Error("timed-out read succeeded")
	}
	<-reader.closed
}
