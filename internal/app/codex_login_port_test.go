package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

type codexWarningTestLogin struct{}

func (codexWarningTestLogin) Run(context.Context, func(string)) error {
	return errors.New("unexpected non-interactive run")
}

func (codexWarningTestLogin) RunInteractive(_ context.Context, ready func(string, bool, func([]byte) error)) error {
	ready("https://auth.example/sign-in", true, func([]byte) error { return nil })
	return nil
}

func TestCodexLoginWarnsBeforeOSPortLaunch(t *testing.T) {
	for _, cancelBeforeDraw := range []bool{false, true} {
		name := "drawn"
		if cancelBeforeDraw {
			name = "cancelled before draw"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			updates := make(chan oauthLoginUpdate)
			launched := make(chan struct{}, 1)
			browser := func(context.Context, string) error {
				launched <- struct{}{}
				return nil
			}
			done := make(chan oauthLoginResult, 1)
			go func() { done <- runBrowserOAuthLogin(ctx, codexWarningTestLogin{}, browser, updates) }()
			var update oauthLoginUpdate
			select {
			case update = <-updates:
			case <-ctx.Done():
				t.Fatal("missing browser-ready update")
			}
			if !update.osPort || update.link == "" || update.shown == nil {
				t.Fatal("browser-ready update must include the temporary-port warning and its draw acknowledgment")
			}
			if cancelBeforeDraw {
				cancel()
			} else {
				close(update.shown)
			}
			select {
			case result := <-done:
				if result.err != nil {
					t.Fatal(result.err)
				}
			case <-ctx.Done():
				// Cancellation must release the worker without any draw acknowledgment.
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("worker did not return")
				}
			}
			if (len(launched) != 0) == cancelBeforeDraw {
				t.Fatalf("browser launch count = %d, cancelled before draw = %v", len(launched), cancelBeforeDraw)
			}
		})
	}
}
