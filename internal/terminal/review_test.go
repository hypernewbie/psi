package terminal

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"psi/internal/testutil"
)

func reviewConsole() (*Console, *testutil.VTModel, *bytes.Buffer) {
	vt, raw := testutil.NewVTModel(80, 24), &bytes.Buffer{}
	out := io.MultiWriter(vtFeed{vt}, raw)
	return &Console{Editor: NewEditor(), Out: out, writer: NewWriter(out), decoder: NewDecoder()}, vt, raw
}

func TestEditorKeyToFrameLatencyP95(t *testing.T) {
	c, _, _ := reviewConsole()
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/synthetic/work", Model: "offline/model", NoColor: true})
	c.Draw()
	samples := make([]time.Duration, 100)
	for i := range samples {
		start := time.Now()
		c.Handle(Input{Key: []byte{'x'}}, false)
		c.Draw()
		samples[i] = time.Since(start)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("editor key-to-frame: samples=%d p50=%s p95=%s max=%s", len(samples), samples[49], samples[94], samples[99])
	if samples[94] > 50*time.Millisecond {
		t.Fatalf("key-to-frame p95 %s exceeds 50ms", samples[94])
	}
}

func TestCancellationDispatchP95(t *testing.T) {
	c, _, _ := reviewConsole()
	samples := make([]time.Duration, 100)
	for i := range samples {
		start := time.Now()
		_, _, _, cancel := c.Handle(Input{Key: []byte{3}}, true)
		samples[i] = time.Since(start)
		if !cancel {
			t.Fatal("Ctrl+C did not dispatch cancellation")
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("cancellation input dispatch: samples=%d p50=%s p95=%s max=%s", len(samples), samples[49], samples[94], samples[99])
	if samples[94] > 100*time.Millisecond {
		t.Fatalf("cancellation dispatch p95 %s exceeds 100ms", samples[94])
	}
}

func Test256KiBPasteUpdatesWithinBudget(t *testing.T) {
	c, _, _ := reviewConsole()
	payload := bytes.Repeat([]byte{'x'}, pasteBudget)
	c.Handle(Input{Paste: true, PasteStart: true}, false)
	c.Handle(Input{Paste: true, Key: payload}, false)
	start := time.Now()
	c.Handle(Input{Paste: true, PasteEnd: true}, false)
	elapsed := time.Since(start)
	budget := 200 * time.Millisecond
	if raceInstrumentation {
		budget = 500 * time.Millisecond
	}
	t.Logf("256KiB paste end-to-frame: %s (budget %s)", elapsed, budget)
	if elapsed > budget {
		t.Fatalf("paste update took %s, exceeds %s", elapsed, budget)
	}
	if len(c.Editor.Text()) != pasteBudget {
		t.Fatalf("paste admitted %d bytes, want %d", len(c.Editor.Text()), pasteBudget)
	}
}

func TestScheduledFrameBatchCombinesAppendAndStatus(t *testing.T) {
	c, vt, _ := reviewConsole()
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	_, before, _, _ := c.writer.Stats()
	c.BeginFrame()
	if _, err := c.MarkdownWrite([]byte("First stable paragraph.\n\nActive tail")); err != nil {
		t.Fatal(err)
	}
	c.Status("Working…")
	_, during, _, _ := c.writer.Stats()
	if during != before {
		t.Fatalf("batched updates emitted early: before=%d during=%d", before, during)
	}
	if err := c.EndFrame(); err != nil {
		t.Fatal(err)
	}
	_, after, _, _ := c.writer.Stats()
	if after-before != 1 {
		t.Fatalf("append, active tail, and status used %d frames, want one", after-before)
	}
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	for _, text := range []string{"First stable paragraph.", "Active tail", "Working…", "model"} {
		if !strings.Contains(visible, text) {
			t.Fatalf("combined frame lost %q: %q", text, visible)
		}
	}
}

func TestModelActuallyOverwritesAndErases(t *testing.T) {
	vt := testutil.NewVTModel(12, 3)
	vt.Feed([]byte("abcde\rXY\x1b[K"))
	if got := strings.Split(vt.Text(), "\n")[0]; got != "XY" {
		t.Fatalf("stale cells: %q", got)
	}
	vt.Feed([]byte("\x1b[2J"))
	if strings.TrimSpace(vt.Text()) != "" {
		t.Fatal("ED must erase cells, not only record an operation")
	}
	vt.Feed([]byte("\x1b"))
	vt.Feed([]byte("[Hfresh"))
	if !strings.HasPrefix(vt.Text(), "fresh\n") {
		t.Fatal(vt.Text())
	}
}

func TestSubmitDefersMessageOutputUntilApplicationClassification(t *testing.T) {
	c, vt, raw := reviewConsole()
	fmt.Fprintln(RawOutput{W: c}, "committed sentinel")
	c.Editor.Insert("first\nsecond\nthird")
	c.Draw()
	c.Editor.DocStart() // cursor is not on the last owned row
	c.Draw()
	text, submitted, _, _ := c.Handle(Input{Key: []byte{'\r'}}, false)
	if !submitted || text != "first\nsecond\nthird" {
		t.Fatalf("submission: %q %v", text, submitted)
	}
	c.Draw()
	fmt.Fprintln(RawOutput{W: c}, "a local notice")
	c.Draw()
	want := "committed sentinel\na local notice\n\n─"
	if !strings.HasPrefix(vt.Text(), want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", vt.Text(), want)
	}
	if strings.Contains(vt.Text(), "first") || strings.Contains(vt.Text(), "second") {
		t.Fatal("Console committed a message before the application classified it")
	}
	for _, forbidden := range []string{"\x1b[J", "\x1b[0J", "\x1b[2J", "\x1b[3J"} {
		if bytes.Contains(raw.Bytes(), []byte(forbidden)) {
			t.Fatalf("unbounded erase %q", forbidden)
		}
	}
}

func TestChangedRowsAndTargetCursorColumn(t *testing.T) {
	c, vt, raw := reviewConsole()
	w := c.writer
	w.Emit(Frame{Tail: []string{"> first", "  middle", "  last"}, CursorRow: 0, CursorCol: 5})
	raw.Reset()
	w.Emit(Frame{Tail: []string{"> first", "  NEW", "  last"}, CursorRow: 0, CursorCol: 3})
	if vt.CursorCol != 3 || vt.CursorRow != 0 {
		t.Fatalf("cursor %d,%d", vt.CursorRow, vt.CursorCol)
	}
	if strings.Contains(raw.String(), "first") || strings.Contains(raw.String(), "last") {
		t.Fatal("unchanged rows rewritten")
	}
	if !strings.HasPrefix(vt.Text(), "> first\n  NEW\n  last\n") {
		t.Fatal(vt.Text())
	}
	w.Emit(Frame{Tail: []string{"> first", "  NEW", "  last"}, CursorRow: 1, CursorCol: 4})
	if vt.CursorCol != 4 || vt.CursorRow != 1 {
		t.Fatalf("cursor %d,%d", vt.CursorRow, vt.CursorCol)
	}
	w.Emit(Frame{Tail: []string{"> first"}, CursorCol: 2})
	if !strings.HasPrefix(vt.Text(), "> first\n\n\n") {
		t.Fatal("stale rows: " + vt.Text())
	}
}

func TestBackspaceDeletesOneGraphemeThroughConsole(t *testing.T) {
	c, _, _ := reviewConsole()
	c.Editor.Insert("ae\u0301")
	c.Handle(Input{Key: []byte{127}}, false)
	if c.Editor.Text() != "a" {
		t.Fatalf("backspace ignored or split a cluster: %q", c.Editor.Text())
	}
}

func TestPasteRejectsWholeEditAgainstExistingDraft(t *testing.T) {
	c, _, _ := reviewConsole()
	c.Editor.Insert("original")
	c.Handle(Input{PasteStart: true}, false)
	c.Handle(Input{Paste: true, Key: bytes.Repeat([]byte{'x'}, editorBudget)}, false)
	c.Handle(Input{PasteEnd: true, Paste: true}, false)
	if c.Editor.Text() != "original" {
		t.Fatalf("oversized paste partially replaced draft: %d bytes", len(c.Editor.Text()))
	}
}

func TestDecoderKeepsSlowUTF8AndUsesEscapeIdleTime(t *testing.T) {
	d := NewDecoder()
	now := time.Now()
	d.SetClock(func() time.Time { return now })
	if got := d.Feed([]byte{0xe7}); len(got) != 0 {
		t.Fatal(got)
	}
	now = now.Add(time.Second)
	if got := d.Feed(nil); len(got) != 0 {
		t.Fatal("UTF-8 was timed out as Escape")
	}
	got := d.Feed([]byte{0x95, 0x8c})
	if len(got) != 1 || string(got[0].Key) != "界" {
		t.Fatalf("lost UTF-8: %+v", got)
	}
	for _, part := range []string{"\x1b", "[", "1", "~"} {
		now = now.Add(40 * time.Millisecond)
		got = d.Feed([]byte(part))
	}
	if len(got) != 1 || string(got[0].Key) != "\x1b[1~" {
		t.Fatalf("not an idle timeout: %+v", got)
	}
	d.Feed([]byte("\x1b[" + strings.Repeat("1", 2000)))
	if len(d.pending) > maxPendingEscape {
		t.Fatal("escape buffer unbounded")
	}
	got = d.Feed([]byte("~x"))
	if len(got) != 1 || string(got[0].Key) != "x" {
		t.Fatalf("overlong sequence not drained: %+v", got)
	}
}

func TestLayoutNewlineAndSoftWrapSourceBoundaries(t *testing.T) {
	buf := []rune("abc\ndef")
	l := LayoutDraft(buf, 4, 80)
	if l.CursorRow != 1 || l.CursorCol != 0 || l.VisualRunePos(buf, 1, 3) != 7 {
		t.Fatalf("newline source ranges: %+v", l)
	}
	l = LayoutDraft([]rune("abcd"), 3, 6)
	if l.CursorRow != 0 || l.CursorCol != 3 {
		t.Fatalf("cursor before wrapped cluster: %+v", l)
	}
	for _, text := range []string{"1️⃣", "👨‍👩‍👧‍👦", "🇺🇸"} {
		if CellWidth(text) != 2 {
			t.Fatalf("width %q = %d", text, CellWidth(text))
		}
	}
}

type shortWriter struct {
	data  bytes.Buffer
	limit int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	n := min(w.limit, len(p))
	return w.data.Write(p[:n])
}

func TestWriteBoundedPreservesEveryShortWriteByte(t *testing.T) {
	for _, size := range []int{17, 2*maxWriteUnit + 19} {
		data := bytes.Repeat([]byte("0123456789"), size/10+1)[:size]
		w := &shortWriter{limit: 3}
		if err := WriteBounded(w, data); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, w.data.Bytes()) {
			t.Fatalf("short write corrupted %d-byte frame", size)
		}
	}
	w := &shortWriter{limit: 0}
	if err := WriteBounded(w, []byte("x")); err != io.ErrShortWrite {
		t.Fatal(err)
	}
}

func TestRuntimeStreamerCoalescesActualWrites(t *testing.T) {
	w := &countWriter{}
	s := NewStreamer(w, true)
	for i := 0; i < 10000; i++ {
		s.Delta("a")
	}
	if w.writes != 0 || !s.Pending() {
		t.Fatal("deltas bypassed the frame timer")
	}
	s.Flush()
	if w.writes != 1 || w.data.String() != strings.Repeat("a", 10000) {
		t.Fatal("coalesced text lost or duplicated")
	}
	s.Close()
	if w.data.String() != strings.Repeat("a", 10000)+"\n" {
		t.Fatal("bad stream boundary")
	}
}

type countWriter struct {
	data   bytes.Buffer
	writes int
}

func (w *countWriter) Write(p []byte) (int, error) { w.writes++; return w.data.Write(p) }

func TestSanitizerKeepsUTF8AcrossWriteBoundaries(t *testing.T) {
	text := "界🌍e\u0301"
	var filter Sanitizer
	var out strings.Builder
	for _, b := range []byte(text) {
		out.WriteString(filter.Feed(string([]byte{b})))
	}
	if out.String() != text {
		t.Fatalf("split UTF-8 lost: %q", out.String())
	}
	for _, batch := range Batches(text, 1) {
		if !utf8.ValidString(batch) {
			t.Fatal("batch split a rune")
		}
	}
}

type recoverableWriter struct {
	bytes.Buffer
	fail bool
}

func (w *recoverableWriter) Write(p []byte) (int, error) {
	if w.fail {
		w.fail = false
		return 0, io.ErrClosedPipe
	}
	return w.Buffer.Write(p)
}

func TestCleanupRestoresModesAfterFrameOutputFailure(t *testing.T) {
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out := &recoverableWriter{}
	c := NewConsole(in, out)
	c.Status("working")
	out.Reset()
	out.fail = true
	if _, err := c.Write([]byte("response")); err == nil {
		t.Fatal("frame output failure lost")
	}
	c.Close()
	if c.Err() == nil {
		t.Fatal("output failure not sticky")
	}
	if !strings.Contains(out.String(), "\x1b[?25h\x1b[?2004l") {
		t.Fatal("sticky error prevented mode cleanup")
	}
}

func TestToolStatusesCommitOneOutcomePerCall(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	s := NewStatusWriter(&out)
	for i := 0; i < 100; i++ {
		id := fmt.Sprint(i)
		s.Update("read", id, ToolRunning)
		s.Update("read", id, ToolDone)
		s.Update("read", id, ToolDone)
	}
	if out.String() != strings.Repeat(MessageIndent+"read: done\n", 100) {
		t.Fatal("transient or duplicate statuses committed")
	}
}
