//go:build !windows

package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A FIFO at the auth path is refused on every read path without opening
// it. The writer offers a small valid document if a reader ever opens the
// pipe, so an unsafe reader fails by accepting it rather than hanging. L4.
func TestOpenAINonRegularAuthFIFO(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := syscall.Mkfifo(authPath, 0o600); err != nil {
		t.Fatal(err)
	}
	writerDone := make(chan error, 1)
	go func() {
		w, err := os.OpenFile(authPath, os.O_WRONLY, 0)
		if err != nil {
			writerDone <- err
			return
		}
		_, err = w.WriteString(`{"openai":{"type":"api_key","key":"fifo-key"}}`)
		writerDone <- errors.Join(err, w.Close())
	}()
	t.Cleanup(func() {
		// Keep the reader open until the writer finishes. Opening and
		// closing it immediately can miss the writer's blocking open.
		r, err := os.OpenFile(authPath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if err := <-writerDone; err != nil {
			t.Errorf("FIFO test writer: %v", err)
		}
	})
	const regularFileError = "auth.json must be a regular file"
	if _, err := AuthEntries(authPath); err == nil || !strings.Contains(err.Error(), regularFileError) {
		t.Fatalf("AuthEntries FIFO error = %v", err)
	}
	auth := &OpenAIAuth{Path: authPath, Env: map[string]string{"OPENAI_API_KEY": "env-key"}}
	if err := auth.Check(); err == nil || !strings.Contains(err.Error(), regularFileError) {
		t.Fatalf("Check FIFO error = %v", err)
	}
	if _, err := auth.Get(context.Background()); err == nil || !strings.Contains(err.Error(), regularFileError) {
		t.Fatalf("Get FIFO error = %v", err)
	}
	login := &OpenAILogin{Path: authPath, DeviceID: openAILoginTestDeviceID}
	if err := login.Run(context.Background(), nil); !errors.Is(err, errOpenAIAuthUnsafe) {
		t.Fatalf("login FIFO error = %v", err)
	}
}
