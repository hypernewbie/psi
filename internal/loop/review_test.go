package loop

import (
	"errors"
	"testing"

	"psi/internal/session"
)

func TestContinueDoesNotAdvanceWhenStateCannotBeSaved(t *testing.T) {
	for _, checking := range []bool{false, true} {
		name := "working"
		if checking {
			name = "checking"
		}
		t.Run(name, func(t *testing.T) {
			store, controller := newTestController(t)
			if _, err := controller.Start("goal complete work"); err != nil {
				t.Fatal(err)
			}
			// Setup-only state entries are in memory until the first conversation message.
			if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "start"}}); err != nil {
				t.Fatal(err)
			}
			if checking {
				if result := controller.HandleDone(map[string]any{"reason": "claim"}); result.Error {
					t.Fatal(result.Text)
				}
			}
			before := controller.Snapshot()
			storageErr := errors.New("injected storage failure")
			store.FailWritesForTest(storageErr)
			_, _, ok, err := controller.ContinuePrompt()
			if ok || !errors.Is(err, storageErr) {
				t.Fatalf("ContinuePrompt: ok=%v err=%v", ok, err)
			}
			if after := controller.Snapshot(); after != before {
				t.Fatalf("state advanced without persistence: before=%+v after=%+v", before, after)
			}
		})
	}
}
