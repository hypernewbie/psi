package terminal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type Input struct {
	Key []byte
	Err error
}

// Console owns only the active prompt. Completed lines remain native scrollback.
// All drawing occurs in the application's event-loop goroutine.
type Console struct {
	In        *os.File
	Out       io.Writer
	Editor    *Editor
	Inputs    <-chan Input
	stop      context.CancelFunc
	done      chan struct{}
	paste     bool
	visible   bool
	cursorRow int
}

func NewConsole(in *os.File, out io.Writer) *Console {
	ctx, cancel := context.WithCancel(context.Background())
	inputs := make(chan Input, 64)
	c := &Console{In: in, Out: out, Editor: NewEditor(), Inputs: inputs, stop: cancel, done: make(chan struct{})}
	fmt.Fprint(out, "\x1b[?2004h")
	go func() { defer close(c.done); defer close(inputs); readInputs(ctx, in, inputs) }()
	return c
}

func (c *Console) Close() {
	c.stop()
	<-c.done
	if c.visible {
		fmt.Fprint(c.Out, "\r\n")
	}
	fmt.Fprint(c.Out, "\x1b[?2004l")
}

func readInputs(ctx context.Context, in *os.File, out chan<- Input) {
	send := func(input Input) bool {
		select {
		case out <- input:
			return true
		case <-ctx.Done():
			return false
		}
	}
	var pending []byte
	paste := false
	buf := make([]byte, 4096)
	for ctx.Err() == nil {
		fds := []unix.PollFd{{Fd: int32(in.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			send(Input{Err: err})
			return
		}
		if n == 0 {
			continue
		}
		n, err = in.Read(buf)
		if err != nil {
			send(Input{Err: err})
			return
		}
		if n == 0 {
			send(Input{Err: io.EOF})
			return
		}
		pending = append(pending, buf[:n]...)
		for len(pending) > 0 {
			if paste {
				end := bytes.Index(pending, []byte("\x1b[201~"))
				if end != 0 {
					n := end
					if n < 0 {
						n = len(pending) - 5
					}
					for n > 0 && n < len(pending) && !utf8.RuneStart(pending[n]) {
						n--
					}
					if n <= 0 {
						break
					}
					if !send(Input{Key: append([]byte(nil), pending[:n]...)}) {
						return
					}
					pending = pending[n:]
					continue
				}
			}
			size := 1
			if pending[0] == 27 {
				if len(pending) < 2 {
					break
				}
				if pending[1] == '[' || pending[1] == 'O' {
					size = 2
					for size < len(pending) && !(pending[size] >= 0x40 && pending[size] <= 0x7e) {
						size++
					}
					if size == len(pending) {
						if size > 256 {
							pending = nil
						}
						break
					}
					size++
				} else {
					size = 2
				}
			} else if pending[0] >= utf8.RuneSelf {
				if !utf8.FullRune(pending) {
					break
				}
				_, size = utf8.DecodeRune(pending)
			}
			key := append([]byte(nil), pending[:size]...)
			if bytes.Equal(key, []byte("\x1b[200~")) {
				paste = true
			}
			if bytes.Equal(key, []byte("\x1b[201~")) {
				paste = false
			}
			pending = pending[size:]
			if !send(Input{Key: key}) {
				return
			}
		}
	}
}

// Handle returns a submitted prompt, quit request, or cancellation request.
func (c *Console) Handle(input Input, busy bool) (text string, submitted, quit, cancel bool) {
	if input.Err != nil {
		return "", false, true, busy
	}
	key := input.Key
	if bytes.Equal(key, []byte("\x1b[200~")) {
		c.paste = true
		return
	}
	if bytes.Equal(key, []byte("\x1b[201~")) {
		c.paste = false
		return
	}
	if busy {
		if !c.paste && bytes.Equal(key, []byte{3}) {
			cancel = true
		}
		return
	}
	if c.paste {
		t := strings.ReplaceAll(string(key), "\r", "\n")
		if len(c.Editor.Text())+len(t) <= 256*1024 {
			c.Editor.Insert(Sanitize(t))
		}
		c.Draw()
		return
	}
	if bytes.Equal(key, []byte{3}) {
		if c.Editor.Text() == "" {
			return "", false, true, false
		}
		c.Editor.Clear()
		c.Draw()
		return
	}
	if bytes.Equal(key, []byte{4}) && c.Editor.Text() == "" {
		return "", false, true, false
	}
	if bytes.Equal(key, []byte{'\r'}) {
		c.Editor.End()
		c.Draw()
		fmt.Fprint(c.Out, "\r\n")
		c.visible, c.cursorRow = false, 0
		return c.Editor.Submit(), true, false, false
	}
	if len(c.Editor.Text()) < 256*1024 {
		c.Editor.HandleKey(key, false)
	}
	c.Draw()
	return
}

func (c *Console) Draw() {
	width, height, err := term.GetSize(int(c.In.Fd()))
	if err != nil || width < 4 {
		width = 80
	}
	if height < 4 {
		height = 24
	}
	// Leave the final column unused to avoid ambiguous delayed-wrap state.
	limit := width - 1
	if c.visible {
		fmt.Fprint(c.Out, "\r")
		if c.cursorRow > 0 {
			fmt.Fprintf(c.Out, "\x1b[%dA", c.cursorRow)
		}
		fmt.Fprint(c.Out, "\x1b[J")
	}
	var b strings.Builder
	b.WriteString("> ")
	row, col, cursorRow, cursorCol := 0, 2, 0, 2
	for i, r := range c.Editor.buf {
		if i == c.Editor.pos {
			cursorRow, cursorCol = row, col
		}
		if r == '\n' {
			b.WriteString("\r\n  ")
			row++
			col = 2
			continue
		}
		rw := RuneWidth(r)
		if col+rw > limit {
			b.WriteString("\r\n  ")
			row++
			col = 2
		}
		if r == '\t' {
			b.WriteString(" ")
			col++
		} else {
			b.WriteString(Sanitize(string(r)))
			col += rw
		}
	}
	if c.Editor.pos == len(c.Editor.buf) {
		cursorRow, cursorCol = row, col
	}
	// Keep a large multiline draft inside a small viewport. Do not push
	// hundreds of redraw copies of the paste into native scrollback.
	lines := strings.Split(b.String(), "\r\n")
	maxRows := max(1, height-2)
	start := max(0, cursorRow-maxRows+1)
	end := min(len(lines), start+maxRows)
	if end-start < maxRows {
		start = max(0, end-maxRows)
	}
	visible := strings.Join(lines[start:end], "\r\n")
	cursorRow -= start
	lastRow := end - start - 1
	fmt.Fprint(c.Out, visible)
	if lastRow > cursorRow {
		fmt.Fprintf(c.Out, "\x1b[%dA", lastRow-cursorRow)
	}
	fmt.Fprint(c.Out, "\r")
	if cursorCol > 0 {
		fmt.Fprintf(c.Out, "\x1b[%dC", cursorCol)
	}
	c.visible, c.cursorRow = true, cursorRow
}

// RawOutput retains terminal line endings while raw input disables OPOST.
type RawOutput struct{ W io.Writer }

func (w RawOutput) Write(p []byte) (int, error) {
	converted := bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))
	_, err := w.W.Write(converted)
	return len(p), err
}
