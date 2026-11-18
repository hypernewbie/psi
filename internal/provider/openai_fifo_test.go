//go:build !windows

package provider

import (
	"context"
	"os"
	"path/filepath"
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
	released := make(chan struct{})
	go func() {
		defer close(released)
		w, err := os.OpenFile(authPath, os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer w.Close()
		_, _ = w.WriteString(`{"openai":{"type":"api_key","key":"fifo-key"}}`)
	}()
	t.Cleanup(func() {
		// Release the unused writer without blocking: a non-blocking
		// reader rendezvous is enough for the small buffered write.
		if r, err := os.OpenFile(authPath, os.O_RDONLY|syscall.O_NONBLOCK, 0o600); err == nil {
			_ = r.Close()
		}
		<-released
	})
	if _, err := AuthEntries(authPath); err == nil {
		t.Fatal("FIFO accepted by AuthEntries")
	}
	auth := &OpenAIAuth{Path: authPath, Env: map[string]string{"OPENAI_API_KEY": "env-key"}}
	if err := auth.Check(); err == nil {
		t.Fatal("FIFO accepted by Check")
	}
	if _, err := auth.Get(context.Background()); err == nil {
		t.Fatal("FIFO accepted by Get")
	}
	login := &OpenAILogin{Path: authPath, DeviceID: openAILoginTestDeviceID}
	if err := login.Run(context.Background(), nil); err == nil {
		t.Fatal("FIFO accepted by login")
	}
}
