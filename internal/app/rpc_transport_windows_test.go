//go:build windows

package app

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestRPCWindowsTransportInterruptsBlockedIO(t *testing.T) {
	t.Run("full pipe write hits its deadline", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		defer writer.Close()
		pipe, err := newWindowsRPCWriter(writer)
		if err != nil {
			t.Fatal(err)
		}
		defer pipe.Close()
		if err := pipe.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := pipe.Write([]byte(strings.Repeat("x", 1<<20)))
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("full pipe write unexpectedly succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("deadline did not interrupt the blocked pipe write")
		}
	})

	t.Run("encoder deadline fails a blocked frame", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		defer writer.Close()
		pipe, err := newWindowsRPCWriter(writer)
		if err != nil {
			t.Fatal(err)
		}
		defer pipe.Close()
		failed := make(chan error, 1)
		encoder := newRPCEncoder(pipe, func(err error) { failed <- err })
		encoder.timeout = 100 * time.Millisecond
		done := make(chan error, 1)
		go func() { done <- encoder.Encode(map[string]any{"delta": strings.Repeat("x", 1<<20)}) }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("blocked encoder frame unexpectedly succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("encoder deadline did not interrupt the blocked frame")
		}
		select {
		case <-failed:
		case <-time.After(time.Second):
			t.Fatal("encoder failure callback did not run")
		}
	})

	t.Run("input interrupt wakes a blocked read", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		defer writer.Close()
		input, err := newWindowsRPCReader(reader)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		result := make(chan error, 1)
		go func() {
			_, err := input.Read(make([]byte, 16))
			result <- err
		}()
		time.Sleep(50 * time.Millisecond)
		input.Interrupt()
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("interrupted read returned no error")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("input interrupt did not wake the blocked read")
		}
	})
}
