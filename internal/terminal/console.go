package terminal

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/rivo/uniseg"
	"golang.org/x/term"
)

type Input struct {
	Key []byte
	Err error
	// Resize is a viewport change, never an editor key or pasted content.
	Resize bool
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
const maxActiveFragmentBytes = 64 * 1024

type Console struct {
	In     *os.File
	Out    io.Writer
	Editor *Editor
	Inputs <-chan Input
	// SizeFunc is a deterministic layout seam for VT tests. Production consoles
	// use the terminal descriptor size.
	SizeFunc func() (int, int)

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

	lastDraft           string
	lastCursor          int
	lastW, lastH        int
	visibleTail         []string
	presentation        PresentationSnapshot
	activity            string
	streamFragment      []byte
	streamRows          []string
	markdownStream      *MarkdownStream
	markdownWidth       int
	completionItems     []string
	completionRows      []string
	completionSelected  int
	completionDismissed bool
	picker              *pickerRegion
	busyNoticed         bool
	dirty               bool
	frameDepth          int
	pendingAppend       strings.Builder
	lastDraw            time.Time
	// Completion sources (no exec/scan/provider).
	Commands               []string
	Skills                 []string
	CompletionDescriptions map[string]string
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
	resizeDone := make(chan struct{})
	if runtime.GOOS == "windows" {
		fd := c.In.Fd()
		if output, ok := c.Out.(*os.File); ok {
			fd = output.Fd()
		}
		go func() {
			defer close(resizeDone)
			watchConsoleResize(ctx, func() (int, int, error) { return term.GetSize(int(fd)) }, inputs, 100*time.Millisecond)
		}()
	} else {
		close(resizeDone)
	}
	go func() {
		defer close(done)
		defer close(inputs)
		readInputs(ctx, c.In, inputs, decoder)
		cancel()
		<-resizeDone
	}()
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
	if err := c.emitComposed(string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// StreamWrite keeps the final incomplete assistant line in the owned tail.
// Complete lines move to native scrollback, while the editor/footer stay live.
func (c *Console) StreamWrite(p []byte) (int, error) {
	text := Sanitize(string(p))
	text = strings.ReplaceAll(text, "\r\n", "\n")
	width := c.currentWidth()
	var stable strings.Builder
	commitLine := func() {
		if len(c.streamFragment) > 0 {
			stable.WriteString("  ")
			stable.Write(c.streamFragment)
		}
		stable.WriteString("\r\n")
		c.streamFragment = c.streamFragment[:0]
	}
	for len(text) > 0 {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			c.streamFragment = append(c.streamFragment, text[:newline]...)
			text = text[newline+1:]
			commitLine()
		} else {
			c.streamFragment = append(c.streamFragment, text...)
			text = ""
		}
		for len(c.streamFragment) > maxActiveFragmentBytes {
			end := streamPrefixLen(c.streamFragment, width)
			if end <= 0 || end > len(c.streamFragment) {
				end = len(c.streamFragment)
			}
			stable.WriteString("  ")
			stable.Write(c.streamFragment[:end])
			stable.WriteString("\r\n")
			c.streamFragment = c.streamFragment[end:]
		}
	}
	var err error
	if stable.Len() > 0 {
		err = c.emitComposed(stable.String())
	} else {
		c.Draw()
		err = c.err
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *Console) currentWidth() int {
	width, _ := c.size()
	return width
}

func streamPrefixLen(fragment []byte, width int) int {
	text := string(fragment)
	limit, cells, end := max(1, width-1), CellWidth("  "), 0
	gr := uniseg.NewGraphemes(text)
	for gr.Next() {
		cluster := gr.Str()
		clusterWidth := GraphemeWidth(cluster)
		if cluster == "\t" {
			clusterWidth = 8 - cells%8
		}
		if cells > CellWidth("  ") && cells+clusterWidth > limit {
			break
		}
		cells += clusterWidth
		_, end = gr.Positions()
	}
	return end
}

func (c *Console) Err() error { return c.err }

// SetPresentation installs an application-computed snapshot. Draw never reads
// session files, credentials, or provider state.
func (c *Console) SetPresentation(snapshot PresentationSnapshot) {
	c.presentation = snapshot
	if c.markdownStream != nil {
		c.markdownStream.noColor = snapshot.NoColor
	}
}

// PickerSize returns the width and row budget available to the picker list.
// The budget matches the region that the picker replaces.
func (c *Console) PickerSize() (width, rows int) {
	width, height := c.size()
	return width, EditorComponentRowBudget(height)
}

// pickerRegion is the application-owned picker that replaces the editor
// region, matching Pi's showSelector behavior.
type pickerRegion struct {
	rows []string
	mode PickerMode
}

// SetPicker installs the app-owned picker in place of the editor region.
// The picker owns its borders, and a search picker also owns the query row.
func (c *Console) SetPicker(rows []string, mode PickerMode) {
	if len(rows) == 0 {
		c.picker = nil
		return
	}
	c.picker = &pickerRegion{rows: append([]string(nil), rows...), mode: mode}
	c.clearCompletion(false)
}

// SetPickerRows installs the searchable picker that owns a query row.
func (c *Console) SetPickerRows(rows []string) { c.SetPicker(rows, PickerSearch) }

// SetPickerChoiceRows installs the startup choice picker, which has no query.
func (c *Console) SetPickerChoiceRows(rows []string) { c.SetPicker(rows, PickerChoice) }

func (c *Console) ClearPickerRows() {
	c.picker = nil
}

// RefreshCompletion recomputes suggestions after an application-owned draft restore.
func (c *Console) RefreshCompletion() {
	c.completionDismissed = false
	c.refreshCompletion()
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
		ready, err := waitInputReady(in, 20)
		if err != nil {
			send(Input{Err: err})
			return
		}
		if !ready {
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
		n, err := in.Read(buf)
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
				c.completionDismissed = false
				c.refreshCompletion()
				c.drawIfChanged(false)
			}
		}
		return "", false, false, false
	}
	if busy {
		// Cancellation only. Pi's Escape and psi's Ctrl+C cancel; all other input is ignored.
		if len(input.Key) == 1 && (input.Key[0] == 3 || input.Key[0] == 27) {
			return "", false, false, true
		}
		return "", false, false, false
	}
	if len(input.Key) == 1 {
		switch input.Key[0] {
		case 3: // Ctrl+C idle: clear draft first, exit when empty.
			if c.Editor.Text() == "" && len(c.completionItems) == 0 {
				return "", false, true, false
			}
			c.Editor.Clear()
			c.inputLimitShown = false
			c.completionDismissed = false
			c.refreshCompletion()
			c.drawIfChanged(false)
			return "", false, false, false
		case 4: // Ctrl+D: delete at cursor when nonempty, exit when empty.
			if c.Editor.Text() == "" {
				return "", false, true, false
			}
			c.Editor.Delete()
			c.drawIfChanged(false)
			return "", false, false, false
		case 27: // Escape dismisses completion before it reaches the editor.
			c.pasteActive = false
			c.pasteBuf.Reset()
			if len(c.completionItems) > 0 {
				c.clearCompletion(true)
				c.Draw()
			}
			return "", false, false, false
		case '\r': // Enter accepts an active completion, then classifies/submits.
			if len(c.completionItems) > 0 {
				c.applyCompletion()
			}
			c.inputLimitShown = false
			text := c.Editor.Submit()
			c.clearCompletion(false)
			c.Draw()
			return text, true, false, false
		}
	}
	// Tab: complete slash-command/skill names (P2-022). No overlay, no exec.
	if len(input.Key) == 1 && input.Key[0] == 9 {
		c.complete()
		return "", false, false, false
	}
	// Completion navigation precedes editor visual/history navigation.
	s := string(input.Key)
	if len(c.completionItems) > 0 && (s == "\x1b[A" || s == "\x1b[B") {
		delta := -1
		if s == "\x1b[B" {
			delta = 1
		}
		c.moveCompletion(delta)
		c.Draw()
		return "", false, false, false
	}
	// Up/Down with real width: visual rows first, history at outer rows (3.4).
	w, _ := c.size()
	if s == "\x1b[A" {
		if c.Editor.MoveUp(w) {
			c.drawIfChanged(false)
		} else {
			c.Editor.PrevHistory()
			c.recallLimit()
			c.drawIfChanged(false)
		}
		return "", false, false, false
	} else if s == "\x1b[B" {
		if c.Editor.MoveDown(w) {
			c.drawIfChanged(false)
		} else {
			c.Editor.NextHistory()
			c.recallLimit()
			c.drawIfChanged(false)
		}
		return "", false, false, false
	} else if len(input.Key) == 1 && input.Key[0] == 16 { // Ctrl+P
		c.Editor.PrevHistory()
		c.recallLimit()
		c.drawIfChanged(false)
		return "", false, false, false
	} else if len(input.Key) == 1 && input.Key[0] == 14 { // Ctrl+N
		c.Editor.NextHistory()
		c.recallLimit()
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
		if c.Editor.Text() != prevDraft {
			c.completionDismissed = false
		}
		c.refreshCompletion()
		c.drawIfChanged(false)
	}
	return "", false, false, false
}

// complete accepts the selected slash-command or skill suggestion in place.
func (c *Console) complete() {
	if len(c.completionItems) == 0 {
		c.completionDismissed = false
		c.refreshCompletion()
	}
	if c.applyCompletion() {
		c.Draw()
	}
}

func (c *Console) hasCompletion() bool { return len(c.completionItems) > 0 }

func (c *Console) refreshCompletion() {
	if c.Commands == nil {
		c.Commands = CommandNames()
	}
	if c.completionDismissed || c.picker != nil {
		c.clearCompletion(false)
		return
	}
	previous := ""
	if c.completionSelected >= 0 && c.completionSelected < len(c.completionItems) {
		previous = c.completionItems[c.completionSelected]
	}
	items, _ := Complete(c.Editor.Text(), c.Editor.Pos(), c.Commands, c.Skills)
	clear(c.completionItems)
	c.completionItems = append(c.completionItems[:0], items...)
	if len(items) == 0 {
		c.completionSelected = 0
		c.completionRows = nil
		return
	}
	c.completionSelected = 0
	for i, item := range items {
		if item == previous {
			c.completionSelected = i
			break
		}
	}
	c.renderCompletionRows()
}

func (c *Console) renderCompletionRows() {
	const visible = 5
	c.completionRows = c.completionRows[:0]
	start := max(0, min(c.completionSelected-visible/2, len(c.completionItems)-visible))
	end := min(len(c.completionItems), start+visible)
	for i := start; i < end; i++ {
		selected := i == c.completionSelected
		marker, token := "  ", TokenMuted
		if selected {
			marker, token = "› ", TokenAccent
		}
		item := c.completionItems[i]
		spans := []StyledSpan{{Text: marker, Token: token}, {Text: item, Token: token}}
		if description := c.CompletionDescriptions[item]; description != "" {
			spans = append(spans, StyledSpan{Text: "  " + description, Token: TokenMuted})
		}
		row := StyledRow{Spans: spans}
		c.completionRows = append(c.completionRows, FormatStyledRow(row, c.currentWidth(), c.presentation.NoColor))
	}
}

func (c *Console) moveCompletion(delta int) {
	if len(c.completionItems) == 0 {
		return
	}
	c.completionSelected = (c.completionSelected + delta + len(c.completionItems)) % len(c.completionItems)
	c.renderCompletionRows()
}

func (c *Console) applyCompletion() bool {
	if !c.hasCompletion() {
		return false
	}
	item := c.completionItems[c.completionSelected]
	if !c.Editor.ReplaceCurrentToken(item) {
		c.inputLimit()
		return false
	}
	c.inputLimitShown = false
	c.clearCompletion(true)
	return true
}

func (c *Console) clearCompletion(dismiss bool) {
	clear(c.completionItems)
	clear(c.completionRows)
	c.completionItems, c.completionRows = nil, nil
	c.completionSelected = 0
	if dismiss {
		c.completionDismissed = true
	}
}

// recallLimit surfaces a skipped oversized recall entry once (F11).
func (c *Console) recallLimit() {
	if !c.Editor.rejectedRecall {
		return
	}
	c.Editor.rejectedRecall = false
	c.inputLimit()
}

func (c *Console) inputLimit() {
	if c.inputLimitShown {
		return
	}
	c.inputLimitShown = true
	_, _ = c.Write([]byte("Input exceeds 256 KiB. Draft unchanged.\r\n"))
	c.Draw()
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

func (c *Console) StatusWidth() int {
	width, _ := c.size()
	return width
}

func (c *Console) size() (int, int) {
	if c.SizeFunc != nil {
		width, height := c.SizeFunc()
		return max(1, width), max(1, height)
	}
	fd := c.In.Fd()
	if runtime.GOOS == "windows" {
		// GetConsoleScreenBufferInfo reports ConPTY's resized viewport from
		// the output handle; the input handle can retain the old dimensions.
		if output, ok := c.Out.(*os.File); ok {
			fd = output.Fd()
		}
	}
	width, height, err := term.GetSize(int(fd))
	if err != nil || width <= 0 {
		width = 80
	}
	if err != nil || height <= 0 {
		height = 24
	}
	return width, height
}

func (c *Console) NeedsDraw() bool { return c.dirty }

// FrameStats reports the writer's cumulative frame and write counters.
func (c *Console) FrameStats() (writes, frames, bytes, unchanged int) {
	if c.writer == nil {
		return 0, 0, 0, 0
	}
	return c.writer.Stats()
}

func (c *Console) Flush() {
	if c.dirty {
		c.Draw()
	}
}

// Status is the activity shown in Pi's editor top border. It remains owned by
// the composed tail until the application clears or replaces it.
func (c *Console) Status(text string) {
	c.activity = SanitizeStatusRow(text)
	c.Draw()
}

func (c *Console) Draw() {
	c.dirty = false
	_ = c.emitComposed("")
}

// MarkdownWrite streams assistant source through the bounded shared Markdown
// renderer. Stable rows append to native scrollback; only its active block is
// retained in the composed tail.
func (c *Console) MarkdownWrite(p []byte) (int, error) {
	if c.markdownStream == nil {
		c.markdownWidth = c.currentWidth()
		c.markdownStream = NewMarkdownStream(c.markdownWidth, c.presentation.NoColor)
	}
	stable, _ := c.markdownStream.Feed(string(p))
	c.streamRows = c.markdownStream.DisplayTail()
	appendText := markdownRowsAppend(stable)
	if err := c.emitComposed(appendText); err != nil {
		return 0, err
	}
	return len(p), nil
}

// CloseMarkdown settles the current assistant message at a tool or run boundary.
func (c *Console) CloseMarkdown() error {
	if c.markdownStream == nil {
		return c.err
	}
	hadOutput := c.markdownStream.HasOutput() || len(c.markdownStream.Tail()) > 0
	rows := c.markdownStream.Finish()
	clear(c.streamRows)
	c.streamRows = nil
	appendText := markdownRowsAppend(rows)
	if hadOutput {
		appendText += "\r\n" // Pi-style message padding before the next block.
	}
	return c.emitComposed(appendText)
}

func markdownRowsAppend(rows []string) string {
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, "\r\n") + "\r\n"
}

func (c *Console) BeginFrame() { c.frameDepth++ }

// EndFrame emits one combined append-and-tail update for the current batch.
func (c *Console) EndFrame() error {
	if c.frameDepth == 0 {
		return c.err
	}
	c.frameDepth--
	if c.frameDepth > 0 {
		return c.err
	}
	appendText := c.pendingAppend.String()
	c.pendingAppend.Reset()
	if !c.dirty && appendText == "" {
		return c.err
	}
	c.dirty = false
	return c.emitComposed(appendText)
}

func (c *Console) emitComposed(appendText string) error {
	if c.frameDepth > 0 {
		if appendText != "" {
			c.pendingAppend.WriteString(appendText)
		}
		c.dirty = true
		return c.err
	}
	c.lastDraw = time.Now()
	width, height := c.size()
	var view EditorView
	var fragmentRows []string
	hasStream := false
	if c.markdownStream != nil && len(c.streamRows) > 0 {
		if c.markdownWidth != width {
			c.markdownWidth = width
			c.markdownStream.SetWidth(width)
			c.streamRows = c.markdownStream.DisplayTail()
		}
		fragmentRows = c.streamRows
		hasStream = true
	}
	switch {
	case c.picker != nil:
		// Pi replaces the editor component with the selector. The picker owns
		// the region, so the editor block is not rendered underneath it.
		view = ComposeConsoleViewPicker(width, height, c.Editor.buf, c.Editor.pos, c.presentation, c.activity, fragmentRows, c.picker.rows, c.picker.mode)
	case len(c.completionRows) > 0:
		if !hasStream {
			fragmentRows = wrapAssistantFragment(string(c.streamFragment), width)
		}
		view = ComposeConsoleViewComponents(width, height, c.Editor.buf, c.Editor.pos, c.presentation, c.activity, fragmentRows, c.completionRows)
	case hasStream:
		view = ComposeConsoleViewComponents(width, height, c.Editor.buf, c.Editor.pos, c.presentation, c.activity, fragmentRows, nil)
	default:
		view = ComposeConsoleView(width, height, c.Editor.buf, c.Editor.pos, c.presentation, c.activity, string(c.streamFragment))
	}
	hideCursor := c.activity != "" || (c.picker != nil && c.picker.mode == PickerChoice)
	f := Frame{Append: appendText, Tail: view.Rows, TailCellWidth: view.CellWidth,
		CursorRow: view.CursorRow, CursorCol: view.CursorCol,
		ShowCursor: c.hidden && !hideCursor, HideCursor: hideCursor}
	if c.lastW > 0 && (c.lastW != width || c.lastH != height) {
		c.writer.InvalidateTail()
	}
	err := c.emit(f)
	c.hidden = hideCursor
	c.visibleTail = view.Rows
	c.lastDraft, c.lastCursor = c.Editor.Text(), c.Editor.Pos()
	c.lastW, c.lastH = width, height
	return err
}

// ResetBusyNotice clears the one-notice flag at run start.
func (c *Console) ResetBusyNotice() { c.busyNoticed = false }

// RawOutput retains terminal line endings while raw input disables OPOST.
type RawOutput struct{ W io.Writer }

func (w RawOutput) StatusWidth() int {
	if sized, ok := w.W.(interface{ StatusWidth() int }); ok {
		return sized.StatusWidth()
	}
	return 80
}

func (w RawOutput) Write(p []byte) (int, error) {
	converted := bytes.ReplaceAll(p, []byte("\r\n"), []byte("\n"))
	converted = bytes.ReplaceAll(converted, []byte("\n"), []byte("\r\n"))
	if err := WriteBounded(w.W, converted); err != nil {
		return 0, err
	}
	return len(p), nil
}
