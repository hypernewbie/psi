//go:build unix

package app

import (
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRPCBashTransport(t *testing.T) {
	t.Run("a full pipe write times out and restores flags", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		defer writer.Close()
		flagsBefore, err := unix.FcntlInt(writer.Fd(), unix.F_GETFL, 0)
		if err != nil {
			t.Fatal(err)
		}
		pipe, err := newUnixRPCWriter(writer)
		if err != nil {
			t.Fatal(err)
		}
		if err := pipe.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		n, err := pipe.Write([]byte(strings.Repeat("x", 1<<20)))
		if n == 0 || err != os.ErrDeadlineExceeded {
			t.Fatalf("full pipe write = %d, %v; want partial, deadline exceeded", n, err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("write deadline took %s", elapsed)
		}
		if err := pipe.Close(); err != nil {
			t.Fatal(err)
		}
		flagsAfter, err := unix.FcntlInt(writer.Fd(), unix.F_GETFL, 0)
		if err != nil {
			t.Fatal(err)
		}
		if flagsAfter&unix.O_NONBLOCK != 0 {
			t.Fatalf("descriptor flags = %#x, O_NONBLOCK was not cleared", flagsAfter)
		}
		if flagsBefore&unix.O_NONBLOCK != 0 {
			t.Fatalf("test pipe started nonblocking: %#x", flagsBefore)
		}
	})

	t.Run("input interrupt wakes a blocked read", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		defer writer.Close()
		input, err := newUnixRPCReader(reader)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		result := make(chan error, 1)
		go func() {
			_, err := input.Read(make([]byte, 16))
			result <- err
		}()
		time.Sleep(20 * time.Millisecond)
		input.Interrupt()
		select {
		case err := <-result:
			if err != io.EOF {
				t.Fatalf("interrupted read = %v, want EOF", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("interrupted read stayed blocked")
		}
	})

	t.Run("an encoder timeout fails once and never retries the frame", func(t *testing.T) {
		writer := &failingDeadlineWriter{}
		var failures atomic.Int32
		encoder := newRPCEncoder(writer, func(error) { failures.Add(1) })
		encoder.timeout = 10 * time.Millisecond
		if err := encoder.Encode(map[string]any{"type": "response"}); err != os.ErrDeadlineExceeded {
			t.Fatalf("encode error = %v", err)
		}
		if failures.Load() != 1 || writer.writes.Load() != 1 {
			t.Fatalf("failures=%d writes=%d, want 1 and 1", failures.Load(), writer.writes.Load())
		}
		if err := encoder.Encode(map[string]any{"type": "response"}); err != os.ErrDeadlineExceeded {
			t.Fatalf("second encode error = %v", err)
		}
		if writer.writes.Load() != 1 {
			t.Fatalf("encoder retried a failed frame: %d writes", writer.writes.Load())
		}
	})
}

type failingDeadlineWriter struct {
	writes atomic.Int32
}

func (w *failingDeadlineWriter) SetWriteDeadline(time.Time) error { return nil }

func (w *failingDeadlineWriter) Write([]byte) (int, error) {
	w.writes.Add(1)
	return 0, os.ErrDeadlineExceeded
}
