package terminal

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/term"
)

// The application owns signals. A terminal must not install a leaked global
// signal handler that restores raw mode underneath an active editor.
type Term struct {
	fd    int
	state *term.State
	once  sync.Once
}

func Acquire() (*Term, bool, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, false, fmt.Errorf("noninteractive input: psi needs a terminal (no print/RPC mode)")
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false, err
	}
	return &Term{fd: fd, state: st}, true, nil
}

func (t *Term) Restore() {
	if t != nil {
		t.once.Do(func() {
			if t.state != nil {
				_ = term.Restore(t.fd, t.state)
			}
		})
	}
}
