package terminal

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// Term manages acquisition and cleanup (Step 79).
// Detects a usable interactive terminal, saves/restores state, enables only
// required input features, restores paste/cursor on normal exit and handled
// signals, diagnoses noninteractive input. Forced kill cannot run cleanup.

type Term struct {
	fd    int
	state *term.State
}

// Acquire checks for a TTY and puts it in raw mode (when interactive).
// Returns (nil, false) for noninteractive input with a diagnostic.
func Acquire() (*Term, bool, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, false, fmt.Errorf("noninteractive input: psi needs a terminal (no print/RPC mode in this release)")
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false, err
	}
	t := &Term{fd: fd, state: st}
	// Ensure cleanup on SIGINT/SIGTERM where possible.
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-c
		t.Restore()
	}()
	return t, true, nil
}

// Restore returns the terminal to its original state.
func (t *Term) Restore() {
	if t == nil || t.state == nil {
		return
	}
	_ = term.Restore(t.fd, t.state)
	t.state = nil
}
