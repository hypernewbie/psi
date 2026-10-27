package terminal

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type Input struct {
	Key []byte
	Err error
	// Paste transaction markers from the bounded decoder.
	Paste      bool
	PasteStart bool
	PasteEnd   bool
	// Rejected signals an oversized paste rejected as a whole edit.
	Rejected bool
}

// Console owns only the active prompt. Completed lines remain native scrollback.
// All drawing occurs in the application's event-loop goroutine through one Writer.
// Reference: temp/pi-mono/packages/tui/src/tui-main-screen.ts (differential,
// append-only scrollback, no screen/scrollback clear) and editor-component.ts.
type Console struct {
	In     *os.File
	Out    io.Writer
	Editor *Editor
	Inputs <-chan Input

	stop   context.CancelFunc
	done   chan struct{}
	closed bool
	err    error
	hidden bool

	writer  *Writer
	decoder *Decoder

	pasteActive     bool
	pasteBuf        strings.Builder
	pasteNotice     bool
	inputLimitShown bool

	lastDraft    string
	lastCursor   int
	lastW, lastH int
	visibleTail  []string
	busyNoticed  bool
	dirty        bool
	lastDraw     time.Time
	// Completion sources (no exec/scan/provider).
	Commands []string
	Skills   []string
}

func NewConsole(in *os.File, out io.Writer) *Console {
	c := &Console{In: in, Out: out, Editor: NewEditor(), writer: NewWriter(out)}
	c.Resume()
	return c
}

// Pause releases input ownership before the terminal returns to the shell.
func (c *Console) Pause() {
	if c.stop == nil {
		return
	}
	c.stop()
	<-c.done
	c.stop = nil
	_ = c.emit(Frame{ShowCursor: c.hidden})
	c.hidden = false
	c.visibleTail = nil
	// Cleanup bypasses the sticky frame error. A recovered output descriptor
	// still needs cursor and paste restoration even when tail state is unknown.
	c.mode("\x1b[?25h\x1b[?2004l")
}

func (c *Console) Resume() {
	if c.closed || c.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	inputs, done := make(chan Input, 64), make(chan struct{})
	decoder := NewDecoder()
	c.stop, c.done, c.Inputs, c.decoder = cancel, done, inputs, decoder
	c.pasteActive = false
	c.pasteBuf.Reset()
	c.mode("\x1b[?2004h")
	go func() { defer close(done); defer close(inputs); readInputs(ctx, c.In, inputs, decoder) }()
}

func (c *Console) Close() {
	if c.closed {
		return
	}
	c.Pause()
	c.closed = true
}

func (c *Console) mode(s string) {
	if err := WriteBounded(c.Out, []byte(s)); c.err == nil {
		c.err = err
	}
}

func (c *Console) emit(f Frame) error {
	if c.err == nil {
		c.err = c.writer.Emit(f)
	}
	return c.err
}

// Write routes transcript, command, and status output through the same owner as
// the editor. RawOutput performs newline conversion before this boundary.
func (c *Console) Write(p []byte) (int, error) {
	if err := c.emit(Frame{Append: string(p)}); err != nil {
		return 0, err
	}
	c.visibleTail = nil
	return len(p), nil
}

func (c *Console) Err() error { return c.err }

func readInputs(ctx context.Context, in *os.File, out chan<- Input, dec *Decoder) {
	send := func(input Input) bool {
		select {
		case out <- input:
			return true
		case <-ctx.Done():
			return false
		}
	}
	buf := make([]byte, 4096)
	for ctx.Err() == nil {
		fds := []unix.PollFd{{Fd: int32(in.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 20)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			send(Input{Err: err})
			return
		}
		if n == 0 {
			// Timeout tick: flush incomplete Escape prefix (50ms rule).
			for _, d := range dec.Feed(nil) {
				if d.Reply {
					continue
				}
				if !send(Input{Key: d.Key}) {
					return
				}
			}
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
		for _, d := range dec.Feed(buf[:n]) {
			if d.Reply {
				continue
			}
			if d.PasteStart {
				if !send(Input{Paste: true, PasteStart: true}) {
					return
				}
				continue
			}
			if d.PasteEnd {
				if !send(Input{Paste: true, PasteEnd: true, Rejected: d.Rejected}) {
					return
				}
				continue
			}
			if d.Paste {
				if !send(Input{Key: d.Key, Paste: true}) {
					return
				}
				continue
			}
			if !send(Input{Key: d.Key}) {
				return
			}
		}
	}
}

// Handle returns submitted prompt, quit, or cancellation.
// State first, draw only when visible state changes (P2 flashing fix).
// Busy input is cancellation-only with one quiet notice per run.
func (c *Console) Handle(input Input, busy bool) (text string, submitted, quit, cancel bool) {
	if input.Err != nil {
		return "", false, true, busy
	}
	// Bracketed paste transaction: buffer separately, single insert+draw on end marker.
	if input.PasteStart {
		c.pasteActive = true
		c.pasteBuf.Reset()
		return "", false, false, false
	}
	if c.pasteActive && (input.Paste || input.PasteEnd) {
		if len(input.Key) > 0 {
			c.pasteBuf.Write(input.Key)
		}
		if input.PasteEnd {
			c.pasteActive = false
			if input.Rejected || c.pasteBuf.Len()+len(c.Editor.Text()) > editorBudget {
				c.pasteBuf.Reset()
				if !busy {
					_, _ = c.Write([]byte("Paste exceeds 256 KiB. Draft unchanged.\r\n"))
					c.Draw()
				}
				return "", false, false, false
			}
			content := c.pasteBuf.String()
			c.pasteBuf.Reset()
			if busy {
				// Pasted controls never cancel/submit during run.
				return "", false, false, false
			}
			c.inputLimitShown = false
			prev := c.Editor.Text()
			if content != "" {
				c.Editor.Insert(Sanitize(content))
			}
			if c.Editor.Text() != prev {
				c.drawIfChanged(false)
			}
		}
		return "", false, false, false
	}
	if busy {
		// Cancellation only. Ctrl+C cancels; everything else ignored with one quiet notice per run.
		if len(input.Key) == 1 && input.Key[0] == 3 {
			return "", false, false, true
		}
		return "", false, false, false
	}
	if len(input.Key) == 1 {
		switch input.Key[0] {
		case 3: // Ctrl+C idle: clear draft first, exit when empty.
			if c.Editor.Text() == "" {
				return "", false, true, false
			}
			c.Editor.Clear()
			c.inputLimitShown = false
			c.drawIfChanged(false)
			return "", false, false, false
		case 4: // Ctrl+D: delete at cursor when nonempty, exit when empty.
			if c.Editor.Text() == "" {
				return "", false, true, false
			}
			c.Editor.Delete()
			c.drawIfChanged(false)
			return "", false, false, false
		case 27: // lone Escape: cancel selector/completion state, never block.
			c.pasteActive = false
			c.pasteBuf.Reset()
			return "", false, false, false
		case '\r': // Enter submits outside paste.
			c.inputLimitShown = false
			c.commitAccepted()
			return c.Editor.Submit(), true, false, false
		}
	}
	// Tab: complete slash-command/skill names (P2-022). No overlay, no exec.
	if len(input.Key) == 1 && input.Key[0] == 9 {
		c.complete()
		return "", false, false, false
	}
	// Up/Down with real width: visual rows first, history at outer rows (3.4).
	w, _ := c.size()
	s := string(input.Key)
	if s == "\x1b[A" {
		if c.Editor.MoveUp(w) {
			c.drawIfChanged(false)
		} else {
			c.Editor.PrevHistory()
			c.drawIfChanged(false)
		}
		return "", false, false, false
	} else if s == "\x1b[B" {
		if c.Editor.MoveDown(w) {
			c.drawIfChanged(false)
		} else {
			c.Editor.NextHistory()
			c.drawIfChanged(false)
		}
		return "", false, false, false
	} else if len(input.Key) == 1 && input.Key[0] == 16 { // Ctrl+P
		c.Editor.PrevHistory()
		c.drawIfChanged(false)
		return "", false, false, false
	} else if len(input.Key) == 1 && input.Key[0] == 14 { // Ctrl+N
		c.Editor.NextHistory()
		c.drawIfChanged(false)
		return "", false, false, false
	}
	prevDraft, prevPos := c.Editor.Text(), c.Editor.Pos()
	c.Editor.rejectedInsert = false
	c.Editor.HandleKey(input.Key, false)
	if c.Editor.rejectedInsert {
		c.inputLimit()
	}
	if c.Editor.Text() != prevDraft {
		c.inputLimitShown = false
	}
	if c.Editor.Text() != prevDraft || c.Editor.Pos() != prevPos {
		c.drawIfChanged(false)
	}
	return "", false, false, false
}

// complete implements lightweight Tab completion (P2-022).
func (c *Console) complete() {
	if c.Commands == nil {
		c.Commands = []string{"help", "new", "resume", "model", "thinking", "compact", "session", "history", "quit"}
	}
	draft, pos := c.Editor.Text(), c.Editor.Pos()
	cands, showList := Complete(draft, pos, c.Commands, c.Skills)
	if len(cands) == 1 {
		// Single match: complete in place, stable cursor.
		runes := []rune(draft)
		start := pos
		for start > 0 && runes[start-1] != ' ' && runes[start-1] != '\n' && runes[start-1] != '\t' {
			start--
		}
		token := string(runes[start:pos])
		suffix := cands[0][len(token):] + " "
		if !c.Editor.Insert(suffix) {
			c.inputLimit()
		} else {
			c.inputLimitShown = false
		}
		c.drawIfChanged(false)
		return
	}
	if showList {
		// Bounded ambiguity list only on explicit Tab; draft/cursor stable.
		// Printed as a notice row, not an overlay (UX-lite).
		_, _ = c.Write([]byte("Candidates: " + strings.Join(bounded(cands, 10), " ") + "\r\n"))
		c.Draw()
	}
}

func (c *Console) inputLimit() {
	if c.inputLimitShown {
		return
	}
	c.inputLimitShown = true
	_, _ = c.Write([]byte("Input exceeds 256 KiB. Draft unchanged.\r\n"))
	c.Draw()
}

func bounded(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// drawIfChanged draws only when visible state changes.
func (c *Console) drawIfChanged(force bool) {
	draft, pos := c.Editor.Text(), c.Editor.Pos()
	w, h := c.size()
	if !force && draft == c.lastDraft && pos == c.lastCursor && w == c.lastW && h == c.lastH {
		return
	}
	if !force && time.Since(c.lastDraw) < time.Second/maxFPS {
		c.dirty = true
		return
	}
	c.Draw()
}

// commitAccepted commits the complete accepted user message once (P2-029).
// The viewport may show only the end, but scrollback gets the full message and
// the session gets one entry. No second draft copy as the accepted message.
func (c *Console) commitAccepted() {
	// Render full draft to scrollback as one message (append-only).
	full := c.Editor.Text()
	lines := strings.Split(full, "\n")
	var b strings.Builder
	b.WriteString("> ")
	for i, ln := range lines {
		if i > 0 {
			b.WriteString("\n  ")
		}
		b.WriteString(Sanitize(ln))
	}
	b.WriteString("\n")
	_, _ = c.Write([]byte(strings.ReplaceAll(b.String(), "\n", "\r\n")))
	c.lastDraft = ""
	c.dirty = false
}

func (c *Console) size() (int, int) {
	width, height, err := term.GetSize(int(c.In.Fd()))
	if err != nil || width <= 0 {
		width = 80
	}
	if err != nil || height <= 0 {
		height = 24
	}
	return width, height
}

func (c *Console) NeedsDraw() bool { return c.dirty }

func (c *Console) Flush() {
	if c.dirty {
		c.Draw()
	}
}

// Status is an owned transient row, replaced by the eventual settled output.
func (c *Console) Status(text string) {
	w, _ := c.size()
	rows := []string{TruncateToWidth(Sanitize(text), max(1, w-1))}
	_ = c.emit(Frame{Tail: rows, HideCursor: true})
	c.hidden = true
	c.visibleTail = rows
}

func (c *Console) Draw() {
	c.dirty = false
	c.lastDraw = time.Now()
	width, height := c.size()
	// Build viewport from shared layout (P2-016): same function as tests.
	layout := LayoutDraft(c.Editor.buf, c.Editor.pos, width)
	rows := layout.Rows
	// Sanitize content rows (trusted cursor bytes stay separate).
	for i, r := range rows {
		rows[i] = sanitizeRow(r)
	}
	// Viewport: keep active rows within terminal height, preserve logical cursor.
	maxRows := max(1, height-2)
	cursorRow := layout.CursorRow
	start := max(0, cursorRow-maxRows+1)
	end := min(len(rows), start+maxRows)
	if end-start < maxRows {
		start = max(0, end-maxRows)
	}
	visible := rows[start:end]
	cursorRow -= start
	// Emit one atomic frame (P2-025): no separate clear, changed-row only, no ED.
	f := Frame{Tail: visible, CursorRow: cursorRow, CursorCol: layout.CursorCol, ShowCursor: c.hidden}
	_ = c.emit(f)
	c.hidden = false
	c.visibleTail = visible
	c.lastDraft, c.lastCursor = c.Editor.Text(), c.Editor.Pos()
	c.lastW, c.lastH = width, height
}

func sanitizeRow(s string) string {
	// Rows already contain "> " prefix and spaces; sanitize printable content.
	// Sanitize strips ANSI/OSC/bidi but preserves our spaces.
	return Sanitize(s)
}

// ResetBusyNotice clears the one-notice flag at run start.
func (c *Console) ResetBusyNotice() { c.busyNoticed = false }

// RawOutput retains terminal line endings while raw input disables OPOST.
type RawOutput struct{ W io.Writer }

func (w RawOutput) Write(p []byte) (int, error) {
	converted := bytes.ReplaceAll(p, []byte("\r\n"), []byte("\n"))
	converted = bytes.ReplaceAll(converted, []byte("\n"), []byte("\r\n"))
	if err := WriteBounded(w.W, converted); err != nil {
		return 0, err
	}
	return len(p), nil
}
