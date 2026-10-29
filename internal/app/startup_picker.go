package app

import (
	"errors"
	"fmt"
	"os"
	"os/signal"

	"psi/internal/session"
	"psi/internal/terminal"
)

// terminalLifecycle is the one owner of raw mode, the input reader, and the
// application signal stream from bootstrap through the interactive loop.
var errStartupCancelled = errors.New("session selection cancelled")

type terminalLifecycle struct {
	tty     *terminal.Term
	console *terminal.Console
	signals chan os.Signal
}

func (l *terminalLifecycle) Suspend() error {
	if l == nil || l.tty == nil || l.console == nil {
		return fmt.Errorf("terminal lifecycle unavailable")
	}
	l.console.Pause()
	l.tty.Restore()
	continued := make(chan os.Signal, 1)
	signal.Notify(continued, resumeSignal())
	defer signal.Stop(continued)
	if err := stopSelf(); err != nil {
		return err
	}
	<-continued
	if err := l.tty.Reacquire(); err != nil {
		return err
	}
	l.console.Resume()
	return nil
}

func (l *terminalLifecycle) redrawPicker(rows func(width int) []string) {
	l.console.SetPickerChoiceRows(rows(l.console.StatusWidth()))
	l.console.Draw()
}

func handleBootstrapSignal(lifecycle *terminalLifecycle, sig os.Signal, redraw func()) (done bool, err error) {
	switch {
	case isResize(sig), isResume(sig):
		redraw()
	case isSuspend(sig):
		if err := lifecycle.Suspend(); err != nil {
			return true, err
		}
		redraw()
	case sig == os.Interrupt:
		return true, nil
	case isShutdown(sig):
		return true, fmt.Errorf("startup interrupted by %s", sig)
	}
	return false, nil
}

// runChoicePicker displays a fixed decision in the picker region. Cancellation
// returns no selection; callers must apply their explicit deny/default policy.
func runChoicePicker(console *terminal.Console, lifecycle *terminalLifecycle, picker *terminal.ChoicePicker) (index int, cancelled bool, err error) {
	draw := func() { lifecycle.redrawPicker(func(width int) []string { return picker.Rows(width, false) }) }
	console.Editor.Clear()
	draw()
	if err := console.Err(); err != nil {
		return -1, true, err
	}
	defer func() {
		console.ClearPickerRows()
		console.Editor.Clear()
		console.Draw()
	}()
	for {
		select {
		case input, ok := <-console.Inputs:
			if !ok {
				return -1, true, errors.New("terminal input closed during startup choice")
			}
			if input.Err != nil {
				return -1, true, input.Err
			}
			if input.Resize {
				draw()
				continue
			}
			if input.Paste || input.PasteStart || input.PasteEnd {
				continue
			}
			switch string(input.Key) {
			case "\x1b", string([]byte{3}), string([]byte{4}):
				return -1, true, nil
			case "\x1b[A":
				picker.Move(-1)
				draw()
			case "\x1b[B":
				picker.Move(1)
				draw()
			case "\x1b[5~":
				picker.Move(-5)
				draw()
			case "\x1b[6~":
				picker.Move(5)
				draw()
			case "\r":
				return picker.Selected, false, nil
			}
			if err := console.Err(); err != nil {
				return -1, true, err
			}
		case sig := <-lifecycle.signals:
			done, signalErr := handleBootstrapSignal(lifecycle, sig, draw)
			if signalErr != nil {
				return -1, true, signalErr
			}
			if err := console.Err(); err != nil {
				return -1, true, err
			}
			if done {
				return -1, true, nil
			}
		}
	}
}

// runSessionPicker gives startup --resume the same searchable rows and key
// behavior as /resume. Search text remains editor state and never enters recall.
func runSessionPicker(console *terminal.Console, lifecycle *terminalLifecycle, selector *Selector) (*session.SessionInfo, bool, error) {
	draw := func() {
		width, rows := console.PickerSize()
		console.SetPickerRows(selector.RenderPickerRows(width, rows, false))
		console.Draw()
	}
	console.Editor.Clear()
	draw()
	if err := console.Err(); err != nil {
		return nil, true, err
	}
	defer func() {
		console.ClearPickerRows()
		console.Editor.Clear()
		console.Draw()
	}()
	for {
		select {
		case input, ok := <-console.Inputs:
			if !ok {
				return nil, true, errors.New("terminal input closed during session selection")
			}
			if input.Err != nil {
				return nil, true, input.Err
			}
			if input.Resize {
				draw()
				continue
			}
			if !input.Paste && !input.PasteStart && !input.PasteEnd {
				switch string(input.Key) {
				case "\x1b", string([]byte{3}):
					return nil, true, nil
				case "\x1b[A":
					selector.Move(-1)
					draw()
					continue
				case "\x1b[B":
					selector.Move(1)
					draw()
					continue
				case "\x1b[5~":
					selector.MovePage(-1)
					draw()
					continue
				case "\x1b[6~":
					selector.MovePage(1)
					draw()
					continue
				case "\r":
					if selected := selector.SelectedItem(); selected != nil {
						return selected, false, nil
					}
					draw()
					continue
				}
			}
			before := console.Editor.Text()
			if string(input.Key) == "\x04" {
				// Ctrl+D stays with the focused picker. It deletes forward in the
				// query and never ends the startup selection.
				console.Editor.Delete()
			} else {
				_, _, quit, _ := console.Handle(input, false)
				if quit {
					return nil, true, nil
				}
			}
			if query := console.Editor.Text(); query != before {
				selector.SetFilter(query)
				draw()
			}
			if err := console.Err(); err != nil {
				return nil, true, err
			}
		case sig := <-lifecycle.signals:
			done, signalErr := handleBootstrapSignal(lifecycle, sig, draw)
			if signalErr != nil {
				return nil, true, signalErr
			}
			if err := console.Err(); err != nil {
				return nil, true, err
			}
			if done {
				return nil, true, nil
			}
		}
	}
}
