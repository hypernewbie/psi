//go:build unix

package app

import (
	"io"
	"os"
	"testing"

	"psi/internal/session"
	"psi/internal/terminal"
)

func TestStartupPickerInputEOFReturnsThroughLifecycleCleanup(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	console := terminal.NewConsole(input, io.Discard)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	defer console.Close()
	defer input.Close()
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	lifecycle := &terminalLifecycle{console: console, signals: make(chan os.Signal, 1)}
	selector := NewSelector([]session.SessionInfo{{ID: "synthetic"}}, "")
	_, cancelled, err := runSessionPicker(console, lifecycle, selector)
	if err == nil || !cancelled {
		t.Fatalf("input EOF did not cancel startup picker: cancelled=%v err=%v", cancelled, err)
	}
}
