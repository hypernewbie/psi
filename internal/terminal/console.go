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

	pitheme "psi/internal/theme"
)

type Input struct {
	Key      []byte
	FollowUp bool   // Alt+Enter; when idle, behaves like Enter
	Reply    []byte // bounded terminal reply, never interpreted as editor input
	Err      error
	// Resize is a viewport change, never an editor key or pasted content.
	Resize bool
	// Paste transaction markers from the bounded decoder.
	Paste      bool
	PasteStart bool
	PasteEnd   bool
	// Rejected signals an oversized paste rejected as a whole edit.
	Rejected bool
}

// Console owns the inline terminal document and its active prompt. Completed
// display blocks retain safe source so a resize can rebuild the main screen.
// All drawing occurs in the application's event-loop goroutine through one Writer.
const maxActiveFragmentBytes = 64 * 1024

// liveThinkingTailRows keeps only a small rendered tail in the composed
// fragment. Older rows are appended to native scrollback as they complete.
const liveThinkingTailRows = 4

type Console struct {
	In     *os.File
	Out    io.Writer
	Editor *Editor
	Inputs <-chan Input
	// SizeFunc is a deterministic layout seam for VT tests. Production consoles
	// use the terminal descriptor size.
	SizeFunc func() (int, int)

	stop               context.CancelFunc
	done               chan struct{}
	closed             bool
	err                error
	hidden             bool
	deferDraw          bool
	themeNotifications bool
	heldInputs         []Input

	writer  *Writer
	decoder *Decoder

	pasteActive         bool
	pasteBuf            strings.Builder
	pasteNotice         bool
	inputLimitShown     bool
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
	toolRow             string
	steeringRows        []string
	steeringTexts       []string
	followUpTexts       []string
	loopRows            []string
	loopText            string
	thinking            bool
	thinkingStream      *MarkdownStream
	thinkingWidth       int
	thinkingRows        []string
	thinkingLive        bool
	thinkingLabelShown  bool
	thinkingBlockIndex  int
	dirty               bool
	frameDepth          int
	pendingAppend       strings.Builder
	transcriptSpacing   transcriptSpacing
	documentBlocks      []DocumentBlock
	documentToolPending bool
	fullDocumentRedraw  bool
	streamDocumentIndex int
	streamDocumentLive  bool
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
	if c.themeNotifications {
		c.mode("\x1b[?2031l")
	}
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
	if c.themeNotifications {
		c.mode("\x1b[?2031h")
	}
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
		if c.err == nil {
			c.transcriptSpacing.observe(f.Append)
		}
	}
	return c.err
}

// Write routes transcript, command, and status output through the same owner as
// the editor. RawOutput performs newline conversion before this boundary.
func (c *Console) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	text := string(p)
	block := DocumentBlock{Kind: DocumentRaw, Text: text}
	if strings.Trim(text, "\r\n") == "" && strings.ContainsAny(text, "\r\n") {
		block = DocumentBlock{Kind: DocumentGap}
	}
	if err := c.emitDocumentBlock(block, text); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *Console) emitDocumentBlock(block DocumentBlock, text string) error {
	start, wasToolPending := len(c.documentBlocks), c.documentToolPending
	prefix := c.recordDocumentBlock(block)
	if err := c.emitComposed(prefix + text); err != nil {
		c.documentBlocks = c.documentBlocks[:start]
		c.documentToolPending = wasToolPending
		return err
	}
	return nil
}

// AppendTranscriptBlock adds one semantic display occurrence and emits its
// current layout. Resize rebuilds use the retained source and latest geometry.
func (c *Console) AppendTranscriptBlock(block DocumentBlock) error {
	rows := FormatDocumentBlock(block, c.currentWidth(), c.presentation)
	return c.emitDocumentBlock(block, rowsAppend(rows))
}

// FinishTranscriptGroup records the blank separator after a compact tool batch.
func (c *Console) FinishTranscriptGroup() error {
	return c.AppendTranscriptBlock(DocumentBlock{Kind: DocumentGap})
}

func (c *Console) recordDocumentBlock(block DocumentBlock) string {
	prefix := ""
	if block.Kind != DocumentTool && block.Kind != DocumentGap && c.documentToolPending {
		c.documentBlocks = append(c.documentBlocks, DocumentBlock{Kind: DocumentGap})
		c.documentToolPending = false
		prefix = "\r\n"
	}
	c.documentBlocks = append(c.documentBlocks, block)
	if block.Kind == DocumentTool {
		c.documentToolPending = true
	} else if block.Kind == DocumentGap {
		c.documentToolPending = false
	}
	return prefix
}

func rowsAppend(rows []string) string {
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, "\r\n") + "\r\n"
}

// AppendDocumentBlock is the structured counterpart to Write for callers that
// own a RawOutput wrapper.
func (w RawOutput) AppendDocumentBlock(block DocumentBlock) error {
	if sink, ok := w.W.(interface{ AppendTranscriptBlock(DocumentBlock) error }); ok {
		return sink.AppendTranscriptBlock(block)
	}
	rows := FormatDocumentBlock(block, w.StatusWidth(), PresentationSnapshot{NoColor: true})
	_, err := io.WriteString(w, rowsAppend(rows))
	return err
}

// StreamWrite keeps the final incomplete assistant line in the active tail.
// Semantic Markdown streams retain their complete display source for resize reflow.
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
func (c *Console) GetPresentation() PresentationSnapshot { return c.presentation }

func (c *Console) SetDeferDraw(deferDraw bool) { c.deferDraw = deferDraw }

func (c *Console) SetTerminalColorSchemeNotifications(enabled bool) {
	if c.themeNotifications == enabled {
		return
	}
	c.themeNotifications = enabled
	if c.stop != nil {
		if enabled {
			c.mode("\x1b[?2031h")
		} else {
			c.mode("\x1b[?2031l")
		}
	}
}

func (c *Console) QueryTerminalColors() error {
	return WriteBounded(c.Out, pitheme.ColorQuery())
}

func (c *Console) HoldInput(input Input) { c.heldInputs = append(c.heldInputs, input) }

func (c *Console) TakeHeldInputs() []Input {
	inputs := append([]Input(nil), c.heldInputs...)
	c.heldInputs = nil
	return inputs
}

func (c *Console) SetPresentation(snapshot PresentationSnapshot) {
	snapshot.Display = snapshot.Display.normalized()
	c.presentation = snapshot
	if c.markdownStream != nil {
		c.markdownStream.noColor = snapshot.NoColor
		c.markdownStream.SetTheme(snapshot.Theme)
		c.markdownStream.SetDisplay(snapshot.Display)
		c.streamRows = c.markdownStream.DisplayTail()
	}
	if c.thinkingStream != nil {
		c.thinkingStream.noColor = snapshot.NoColor
		c.thinkingStream.SetTheme(snapshot.Theme)
		c.thinkingStream.SetDisplay(snapshot.Display)
		c.thinkingStream.SetStyle(mdStyle{token: TokenThinkingText, italic: true})
		if snapshot.Display.HideThinking {
			c.thinkingRows = nil
			// The redraw renders the ledger block as the label; a later delta
			// must not append a second one for the same run.
			c.thinkingLabelShown = true
		} else {
			c.thinkingRows = c.thinkingStream.DisplayTail()
		}
	}
	if len(c.completionItems) > 0 {
		c.renderCompletionRows()
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
					if !send(Input{Reply: append([]byte(nil), d.Key...)}) {
						return
					}
					continue
				}
				if !send(Input{Key: d.Key, FollowUp: d.FollowUp}) {
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
				if !send(Input{Reply: append([]byte(nil), d.Key...)}) {
					return
				}
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
			if !send(Input{Key: d.Key, FollowUp: d.FollowUp}) {
				return
			}
		}
	}
}

// Handle returns submitted prompt, quit, or cancellation.
// State first, draw only when visible state changes (P2 flashing fix).
// Busy input remains editable; Enter submits text for steering. Esc/Ctrl+C cancel.
func (c *Console) Handle(input Input, busy bool) (text string, submitted, quit, cancel bool) {
	if input.Err != nil {
		return "", false, true, busy
	}
	if input.Resize {
		c.RequestFullRedraw()
		return "", false, false, false
	}
	if len(input.Reply) > 0 {
		return "", false, false, false
	}
	// Bracketed paste transaction: buffer separately, single insert+draw on end marker.
	// Bracketed paste transaction: buffer separately, single insert+draw on end marker.
	// A paste is a whole edit and never submits, so it applies during a run too.
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
	if input.FollowUp {
		if busy {
			c.inputLimitShown = false
			text := c.Editor.Submit()
			c.clearCompletion(false)
			c.Draw()
			return text, true, false, false
		}
		input.Key = []byte{'\r'}
	}
	// While a run is active the editor stays usable, like Pi's editor during
	// streaming. Only the cancel keys are intercepted; every other key follows
	// the normal editing path, and Enter returns the text so the application
	// can queue it as a steering or follow-up message.
	if busy && len(input.Key) == 1 && (input.Key[0] == 3 || input.Key[0] == 27) {
		return "", false, false, true
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
	if len(c.completionItems) > 0 && (s == "\x1b[A" || s == "\x1bOA" || s == "\x1b[B" || s == "\x1bOB") {
		delta := -1
		if s == "\x1b[B" || s == "\x1bOB" {
			delta = 1
		}
		c.moveCompletion(delta)
		c.Draw()
		return "", false, false, false
	}
	// Up/Down with real width: visual rows first, history at outer rows (3.4).
	w, _ := c.size()
	if s == "\x1b[A" || s == "\x1bOA" {
		if c.Editor.MoveUp(w) {
			c.drawIfChanged(false)
		} else {
			c.Editor.PrevHistory()
			c.recallLimit()
			c.drawIfChanged(false)
		}
		return "", false, false, false
	} else if s == "\x1b[B" || s == "\x1bOB" {
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
	visible := c.presentation.Display.normalized().AutocompleteMaxVisible
	c.completionRows = c.completionRows[:0]
	start := max(0, min(c.completionSelected-visible/2, len(c.completionItems)-visible))
	end := min(len(c.completionItems), start+visible)
	for i := start; i < end; i++ {
		selected := i == c.completionSelected
		marker, token := "  ", TokenMuted
		if selected {
			// Pi's completion list is a SelectList, so the selected row uses
			// the same pointer as the model picker.
			marker, token = "→ ", TokenAccent
		}
		item := c.completionItems[i]
		spans := []StyledSpan{{Text: marker, Token: token}, {Text: item, Token: token}}
		if description := c.CompletionDescriptions[item]; description != "" {
			spans = append(spans, StyledSpan{Text: "  " + description, Token: TokenMuted})
		}
		row := StyledRow{Spans: spans}
		if selected {
			row.Surface = TokenSelectedBg
		}
		c.completionRows = append(c.completionRows, FormatStyledRow(row, c.currentWidth(), c.presentation.NoColor, c.presentation.Theme))
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

func (c *Console) RequestFullRedraw() {
	c.fullDocumentRedraw = true
	c.dirty = true
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

// SetSteeringRows shows the queued steering messages above the editor, like
// Pi's pendingMessages container: one leading blank row, then one dim row per
// queued message. An empty slice clears the block.
func (c *Console) SetSteeringRows(texts []string) {
	c.steeringTexts = append(c.steeringTexts[:0], texts...)
	c.Draw()
}

// SetFollowUpRows shows queued messages that wait for the current run to end.
func (c *Console) SetFollowUpRows(texts []string) {
	c.followUpTexts = append(c.followUpTexts[:0], texts...)
	c.Draw()
}

func (c *Console) renderSteeringRows(width int) {
	if len(c.steeringTexts)+len(c.followUpTexts) == 0 {
		c.steeringRows = nil
		return
	}
	width = max(1, width-1)
	rows := make([]string, 0, len(c.steeringTexts)+len(c.followUpTexts)+1)
	rows = append(rows, "")
	appendRow := func(label, text string) {
		row := StyledRow{Spans: []StyledSpan{{Text: label, Token: TokenMuted}, {Text: TruncateToWidth(Sanitize(text), max(1, width-CellWidth(label))), Token: TokenMuted}}}
		rows = append(rows, FormatStyledRow(row, width, c.presentation.NoColor, c.presentation.Theme))
	}
	for _, text := range c.steeringTexts {
		appendRow("Steering: ", text)
	}
	for _, text := range c.followUpTexts {
		appendRow("Follow-up: ", text)
	}
	c.steeringRows = rows
}

// SetLoopStatus shows the active loop state above the editor.
func (c *Console) SetLoopStatus(text string) {
	c.loopText = text
	c.Draw()
}

func (c *Console) renderLoopRows(width int) {
	if strings.TrimSpace(c.loopText) == "" {
		c.loopRows = nil
		return
	}
	width = max(1, width-1)
	row := StyledRow{Spans: []StyledSpan{{Text: TruncateToWidth(Sanitize(c.loopText), width), Token: TokenAccent}}}
	c.loopRows = []string{FormatStyledRow(row, width, c.presentation.NoColor, c.presentation.Theme)}
}

// Status is the activity shown in Pi's editor top border. It remains owned by
// the composed tail until the application clears or replaces it.
// Status(text string) sets the transient activity shown in the editor's top
// border.
func (c *Console) Status(text string) {
	c.activity = SanitizeStatusRow(text)
	c.Draw()
}

// SetThinking updates the faint activity row for the next composed frame.
func (c *Console) SetThinking(active bool) {
	if c.thinking != active {
		c.thinking = active
		c.dirty = true
	}
}

// SetToolRow installs the active tool row above the editor, like Pi's
// tool-execution component. The row stays in the owned tail until the call
// settles. It keeps the shared message margin and stays on one physical line
// at the emission width.
func (c *Console) SetToolRow(text string) {
	c.SetToolRowNoDraw(text)
	c.Draw()
}

// SetToolRowNoDraw updates the owned row for a frame assembled by the caller.
func (c *Console) SetToolRowNoDraw(text string) {
	row := SanitizeStatusRow(text)
	if !strings.HasPrefix(row, MessageIndent) {
		row = MessageIndent + row
	}
	c.toolRow = TruncateToWidth(row, max(1, c.currentWidth()-1))
}

// ClearToolRow removes the active tool row. The next frame drops it, so the
// settled line and the removal share one frame and no duplicate running row
// stays visible.
func (c *Console) ClearToolRow() {
	c.toolRow = ""
}

func (c *Console) Draw() {
	c.dirty = false
	_ = c.emitComposed("")
}

// MarkdownWrite streams assistant source through the bounded shared Markdown
// renderer. Stable rows append to native scrollback; only its active block is
// retained in the composed tail.
func (c *Console) MarkdownWrite(p []byte) (int, error) {
	// One live stream at a time: a text run ends the reasoning run.
	if err := c.CloseThinking(); err != nil {
		return 0, err
	}
	if c.markdownStream == nil {
		c.markdownWidth = c.currentWidth()
		c.markdownStream = NewMarkdownStream(c.markdownWidth, c.presentation.NoColor)
		c.markdownStream.SetTheme(c.presentation.Theme)
		c.markdownStream.SetDisplay(c.presentation.Display)
	}
	appendTextPrefix := ""
	if !c.streamDocumentLive {
		streamPrefix := c.recordDocumentBlock(DocumentBlock{Kind: DocumentAssistant, Streaming: true})
		c.streamDocumentIndex = len(c.documentBlocks) - 1
		c.streamDocumentLive = true
		if streamPrefix != "" {
			appendTextPrefix = streamPrefix
		}
	}
	stable, _ := c.markdownStream.Feed(string(p))
	c.documentBlocks[c.streamDocumentIndex].Text = c.markdownStream.CommittedSource()
	c.streamRows = c.markdownStream.DisplayTail()
	appendText := appendTextPrefix + markdownRowsAppend(stable)
	if err := c.emitComposed(appendText); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ThinkingWrite streams reasoning source into the transcript. The source is
// always retained in the document ledger so the visibility toggle can reveal
// it later; hidden runs show only Pi's collapsed label.
func (c *Console) ThinkingWrite(p []byte) (int, error) {
	if len(p) == 0 {
		return len(p), nil
	}
	// One live stream at a time: a reasoning run ends the text run.
	if err := c.CloseMarkdown(); err != nil {
		return 0, err
	}
	if c.thinkingStream == nil {
		c.thinkingWidth = c.currentWidth()
		c.thinkingStream = NewMarkdownStream(c.thinkingWidth, c.presentation.NoColor)
		c.thinkingStream.SetTheme(c.presentation.Theme)
		c.thinkingStream.SetDisplay(c.presentation.Display)
		c.thinkingStream.SetStyle(mdStyle{token: TokenThinkingText, italic: true})
	}
	// The stream's sanitizer retains incomplete UTF-8 and control sequences.
	stable, _ := c.thinkingStream.Feed(string(p))
	appendText := ""
	if !c.thinkingLive {
		// Whitespace-only reasoning never becomes a visible block, but it stays
		// in the stream so the words around it keep their spacing.
		if strings.TrimSpace(c.thinkingStream.Source()) == "" {
			return len(p), nil
		}
		streamPrefix := c.recordDocumentBlock(DocumentBlock{Kind: DocumentThinking, Streaming: true})
		c.thinkingBlockIndex = len(c.documentBlocks) - 1
		c.thinkingLive = true
		appendText = streamPrefix
	}
	c.documentBlocks[c.thinkingBlockIndex].Text = c.thinkingStream.Source()
	if c.presentation.Display.HideThinking {
		c.thinkingRows = nil
		if !c.thinkingLabelShown {
			c.thinkingLabelShown = true
			appendText += rowsAppend(FormatThinkingLabel(c.currentWidth(), c.presentation.NoColor, c.presentation.Display, c.presentation.Theme))
		}
	} else {
		committed, tail, redraw := c.thinkingStream.SplitRows(liveThinkingTailRows)
		if redraw {
			c.fullDocumentRedraw = true
		}
		c.thinkingRows = tail
		appendText += markdownRowsAppend(stable)
		appendText += markdownRowsAppend(committed)
	}
	if err := c.emitComposed(appendText); err != nil {
		return 0, err
	}
	return len(p), nil
}

// CloseThinking settles the live reasoning run and keeps its source in the
// ledger.
func (c *Console) CloseThinking() error {
	if c.thinkingStream == nil {
		return c.err
	}
	if !c.presentation.Display.HideThinking && c.thinkingStream.emittedRows > 0 && c.thinkingStream.fenceByte != 0 {
		// Finishing a fence can replace its displayed preview or add an empty line.
		c.fullDocumentRedraw = true
	}
	rows := c.thinkingStream.Finish()
	hadOutput := c.thinkingStream.HasOutput()
	if c.thinkingLive {
		block := &c.documentBlocks[c.thinkingBlockIndex]
		block.Text = c.thinkingStream.TakeFinishedSource()
		block.Streaming = false
		if !hadOutput {
			c.documentBlocks = append(c.documentBlocks[:c.thinkingBlockIndex], c.documentBlocks[c.thinkingBlockIndex+1:]...)
		}
	}
	c.thinkingStream = nil
	c.thinkingRows = nil
	c.thinkingLive = false
	c.thinkingLabelShown = false
	for _, row := range rows {
		hadOutput = hadOutput || strings.TrimSpace(Sanitize(row)) != ""
	}
	if !hadOutput || c.presentation.Display.HideThinking {
		return c.emitComposed("")
	}
	return c.emitComposed(markdownRowsAppend(rows) + "\r\n")
}

// CloseMarkdown settles the current assistant message at a tool or run boundary.
func (c *Console) CloseMarkdown() error {
	if c.markdownStream == nil {
		return c.err
	}
	rows := c.markdownStream.Finish()
	hadOutput := c.markdownStream.HasOutput()
	if c.streamDocumentLive {
		block := &c.documentBlocks[c.streamDocumentIndex]
		block.Text = c.markdownStream.TakeFinishedSource()
		block.Streaming = false
		if !hadOutput {
			c.documentBlocks = append(c.documentBlocks[:c.streamDocumentIndex], c.documentBlocks[c.streamDocumentIndex+1:]...)
		}
	}
	clear(c.streamRows)
	c.streamRows = nil
	c.streamDocumentLive = false
	for _, row := range rows {
		hadOutput = hadOutput || strings.TrimSpace(Sanitize(row)) != ""
	}
	if !hadOutput {
		return c.emitComposed("")
	}
	return c.emitComposed(markdownRowsAppend(rows) + "\r\n")
}

// markThinkingDisplayed records the rows already rendered by a full document
// redraw. The next live frame then appends only rows that are not on screen.
func (c *Console) markThinkingDisplayed() {
	if c.thinkingStream == nil || !c.thinkingLive || c.presentation.Display.HideThinking {
		return
	}
	c.thinkingStream.MarkDisplayed()
	c.thinkingRows = c.thinkingStream.DisplayTail()
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
	width, height := c.size()
	if c.lastW > 0 && (c.lastW != width || c.lastH != height) {
		c.fullDocumentRedraw = true
	}
	if c.deferDraw {
		if !c.fullDocumentRedraw && appendText == "" {
			return c.err
		}
		c.lastDraw = time.Now()
		if c.fullDocumentRedraw {
			err := c.emit(Frame{FullRedraw: true, DocumentRows: FormatDocument(c.documentBlocks, width, c.presentation),
				Width: width, Height: height})
			if err == nil {
				c.markThinkingDisplayed()
				c.fullDocumentRedraw = false
				c.lastW, c.lastH = width, height
			}
			return err
		}
		return c.emit(Frame{Append: appendText})
	}
	if c.frameDepth > 0 {
		if appendText != "" {
			c.pendingAppend.WriteString(appendText)
		}
		c.dirty = true
		return c.err
	}
	c.lastDraw = time.Now()
	c.renderSteeringRows(width)
	c.renderLoopRows(width)
	if c.lastW != width && len(c.completionItems) > 0 {
		c.renderCompletionRows()
	}
	if c.markdownStream != nil && c.markdownWidth != width {
		c.markdownWidth = width
		c.markdownStream.SetWidth(width)
		c.streamRows = c.markdownStream.DisplayTail()
	}
	if c.thinkingStream != nil && c.thinkingWidth != width {
		c.thinkingWidth = width
		c.thinkingStream.SetWidth(width)
		if !c.presentation.Display.HideThinking {
			c.thinkingRows = c.thinkingStream.DisplayTail()
		}
	}
	var fragmentRows []string
	hasStream := false
	if c.markdownStream != nil && len(c.streamRows) > 0 {
		fragmentRows = c.streamRows
		hasStream = true
	}
	if len(c.loopRows) > 0 {
		fragmentRows = append(append([]string(nil), c.loopRows...), fragmentRows...)
	}
	if len(c.steeringRows) > 0 {
		fragmentRows = append(append([]string(nil), c.steeringRows...), fragmentRows...)
	}
	if len(c.thinkingRows) > 0 && !c.fullDocumentRedraw {
		fragmentRows = append(append([]string(nil), c.thinkingRows...), fragmentRows...)
	}
	if c.thinking {
		fragmentRows = append(append([]string(nil), fragmentRows...), FormatThinkingRow(width, c.presentation.NoColor, c.presentation.Display, c.presentation.Theme))
	}
	if c.toolRow != "" {
		fragmentRows = append(append([]string(nil), fragmentRows...), c.toolRow)
	}
	var view EditorView
	switch {
	case c.picker != nil:
		view = ComposeConsoleViewPicker(width, height, c.Editor.buf, c.Editor.pos, c.presentation, c.activity, fragmentRows, c.picker.rows, c.picker.mode)
	default:
		if !hasStream && len(c.streamFragment) > 0 && width > 3 {
			fragmentRows = wrapAssistantFragment(string(c.streamFragment), width)
		}
		view = ComposeConsoleViewComponents(width, height, c.Editor.buf, c.Editor.pos, c.presentation, c.activity, fragmentRows, c.completionRows)
	}
	if len(c.thinkingRows) > 0 && !c.fullDocumentRedraw && c.thinkingStream.canCommitRows() {
		// The layout can have less space than the usual four-row tail. Commit
		// its clipped prefix rather than silently folding those reasoning rows.
		clipped := min(len(c.thinkingRows), max(0, len(fragmentRows)-view.fragmentRows))
		c.thinkingStream.appendEmitted(c.thinkingRows[:clipped])
		appendText += markdownRowsAppend(c.thinkingRows[:clipped])
		c.thinkingRows = c.thinkingRows[clipped:]
	}
	hideCursor := c.activity != "" || (c.picker != nil && c.picker.mode == PickerChoice)
	fullRedraw := c.fullDocumentRedraw
	if fullRedraw {
		appendText = ""
	}
	var documentRows []string
	if fullRedraw {
		documentRows = FormatDocument(c.documentBlocks, width, c.presentation)
	}
	f := Frame{Append: appendText, FullRedraw: fullRedraw,
		DocumentRows: documentRows,
		Tail:         view.Rows, TailCellWidth: view.CellWidth,
		CursorRow: view.CursorRow, CursorCol: view.CursorCol,
		ShowCursor: c.hidden && !hideCursor, HideCursor: hideCursor,
		Height: height, Width: width}
	err := c.emit(f)
	if err == nil {
		if fullRedraw {
			c.markThinkingDisplayed()
		}
		c.fullDocumentRedraw = false
		c.lastW, c.lastH = width, height
	}
	c.hidden = hideCursor
	c.visibleTail = view.Rows
	c.lastDraft, c.lastCursor = c.Editor.Text(), c.Editor.Pos()
	return err
}

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
