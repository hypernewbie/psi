package terminal

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/term"
)

// The application owns signals. A terminal must not install a leaked global
// signal handler that restores raw mode underneath an active editor.
// Suspend/resume (SIGTSTP/SIGCONT) restores the terminal before control returns
// to the shell and reacquires only the active tail afterwards (P2-034).
// No automatic provider/tool replay occurs on resume.
type Term struct {
	fd     int
	state  *term.State
	mu     sync.Mutex
	active bool
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
	return &Term{fd: fd, state: st, active: true}, true, nil
}

func (t *Term) Restore() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active && t.state != nil {
		if term.Restore(t.fd, t.state) == nil {
			t.active = false
		}
	}
}

// Reacquire does not replace the original cooked state saved by Acquire.
func (t *Term) Reacquire() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active {
		return nil
	}
	if _, err := term.MakeRaw(t.fd); err != nil {
		return err
	}
	t.active = true
	return nil
}
