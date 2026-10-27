package terminal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

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

	stop context.CancelFunc
	done chan struct{}

	writer  *Writer
	decoder *Decoder

	pasteActive bool
	pasteBuf    strings.Builder
	pasteNotice bool

	lastDraft    string
	lastCursor   int
	lastW, lastH int
	visibleTail  []string
	busyNoticed  bool
	// Completion sources (no exec/scan/provider).
	Commands []string
	Skills   []string
}

func NewConsole(in *os.File, out io.Writer) *Console {
	ctx, cancel := context.WithCancel(context.Background())
	inputs := make(chan Input, 64)
	c := &Console{
		In: in, Out: out, Editor: NewEditor(), Inputs: inputs,
		stop: cancel, done: make(chan struct{}),
		writer: NewWriter(out), decoder: NewDecoder(),
	}
	fmt.Fprint(out, "\x1b[?2004h")
	go func() { defer close(c.done); defer close(inputs); readInputs(ctx, in, inputs, c.decoder) }()
	return c
}

func (c *Console) Close() {
	c.stop()
	<-c.done
	// Commit any visible tail to scrollback with a newline, then disable paste.
	if len(c.visibleTail) > 0 {
		fmt.Fprint(c.Out, "\r\n")
	}
	fmt.Fprint(c.Out, "\x1b[?2004l")
}

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
			if input.Rejected || c.pasteBuf.Len() > editorBudget {
				// Reject as whole edit, preserve previous draft, one notice.
				c.pasteBuf.Reset()
				c.drawIfChanged(true)
				return "", false, false, false
			}
			content := c.pasteBuf.String()
			c.pasteBuf.Reset()
			if busy {
				// Pasted controls never cancel/submit during run.
				return "", false, false, false
			}
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
		// Pasted content never cancels/submits (handled above).
		return "", false, false, false
	}
	if len(input.Key) == 1 {
		switch input.Key[0] {
		case 3: // Ctrl+C idle: clear draft first, exit when empty.
			if c.Editor.Text() == "" {
				return "", false, true, false
			}
			c.Editor.Clear()
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
			prev := c.Editor.Text()
			_ = prev
			c.Editor.End()
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
	c.Editor.HandleKey(input.Key, false)
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
		c.Editor.Insert(suffix)
		c.drawIfChanged(false)
		return
	}
	if showList {
		// Bounded ambiguity list only on explicit Tab; draft/cursor stable.
		// Printed as a notice row, not an overlay (UX-lite).
		_ = c.writer.Emit(Frame{Append: "Candidates: " + strings.Join(bounded(cands, 10), " ") + "\r\n"})
	}
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
	c.lastDraft, c.lastCursor, c.lastW, c.lastH = draft, pos, w, h
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
	_ = WriteBounded(c.Out, []byte(strings.ReplaceAll(b.String(), "\n", "\r\n")))
	// Reset tail ownership: next Draw starts fresh.
	c.visibleTail = nil
	c.writer.prevTail = nil
	c.lastDraft = ""
}

func (c *Console) size() (int, int) {
	width, height, err := term.GetSize(int(c.In.Fd()))
	if err != nil || width < 4 {
		width = 80
	}
	if height < 4 {
		height = 24
	}
	return width, height
}

func (c *Console) Draw() {
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
	f := Frame{Tail: visible, CursorRow: cursorRow, CursorCol: layout.CursorCol}
	_ = c.writer.Emit(f)
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
	converted := bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))
	_, err := w.W.Write(converted)
	return len(p), err
}
