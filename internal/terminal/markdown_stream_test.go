package terminal

import (
	"fmt"
	"strings"
	"testing"

	"psi/internal/testutil"
)

func renderMarkdownChunks(chunks []string, width int) ([]string, int, *MarkdownStream) {
	stream := NewMarkdownStream(width, true)
	var rows []string
	maxBuffered := 0
	for _, chunk := range chunks {
		stable, _ := stream.Feed(chunk)
		rows = append(rows, stable...)
		if size := stream.BufferedBytes(); size > maxBuffered {
			maxBuffered = size
		}
	}
	rows = append(rows, stream.Finish()...)
	return rows, maxBuffered, stream
}

func TestMarkdownStreamChunkIndependentAcrossMarkdownBoundaries(t *testing.T) {
	source := "# Heading\n\nA **bold** phrase with `inline code` and [a link](https://example.test).\n\n- one\n- two\n\n> quote\n> continuation\n\n```go\nfmt.Println(\"ok\")\n```\n\nAfter the fence."
	want := RenderMarkdown(source, 42, true)
	chunks := make([]string, 0, len(source))
	for i := range source {
		chunks = append(chunks, source[i:i+1])
	}
	got, maxBuffered, _ := renderMarkdownChunks(chunks, 42)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("byte chunking changed Markdown output\nwant:\n%s\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
	if maxBuffered > maxMarkdownActiveSource {
		t.Fatalf("active Markdown source retained %d bytes", maxBuffered)
	}
}

func TestMarkdownStreamMatchesWholeMessageRendererBelowFallback(t *testing.T) {
	cases := []string{
		"A paragraph with **emphasis** and a link [docs](https://example.test).\n\nSecond paragraph.",
		"## Heading\nText without a blank separator.\n\n- one\n- two\n\n  nested continuation\n\nOutside list.",
		"> first paragraph\n>\n> second paragraph\n\nOutside quote.",
		"```go\nfmt.Println(1)\n```\n\nAfter code.",
		"A [guide] reference.\n\n[guide]: https://example.test/docs\n\nNext paragraph.",
		"<div>raw html</div>\n\nNext paragraph.",
	}
	for _, source := range cases {
		got := RenderMarkdown(source, 48, true)
		want := renderMarkdownCore(source, 48, true)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("block streaming changed bounded message output for %q\nwhole:\n%s\nstreamed:\n%s", source, strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
	}
}

func TestMarkdownStreamReferenceDefinitionsRemainMessageScoped(t *testing.T) {
	source := "A [guide] reference.\n\n[guide]: https://example.test/docs\n\nNext paragraph."
	want := RenderMarkdown(source, 60, true)
	got, maxBuffered, _ := renderMarkdownChunks([]string{"A [guide] reference.\n\n[guide]: https://example.test/docs\n\nNext paragraph."}, 60)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reference definition lost across a block boundary\nwant:\n%s\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
	if maxBuffered > maxMarkdownActiveSource {
		t.Fatalf("active Markdown source retained %d bytes", maxBuffered)
	}
}

func TestMarkdownStreamLongFenceFallsBackAndStaysBounded(t *testing.T) {
	code := strings.Repeat("line of code\n", maxMarkdownActiveSource/8)
	source := "```go\n" + code + "```\n\nAfter the oversized fence."
	stream := NewMarkdownStream(50, true)
	var rows []string
	maxBuffered := 0
	fallbackAt := -1
	for offset := 0; offset < len(source); {
		end := min(len(source), offset+1024)
		stable, _ := stream.Feed(source[offset:end])
		if stream.fallback && fallbackAt < 0 {
			fallbackAt = offset
		}
		rows = append(rows, stable...)
		maxBuffered = max(maxBuffered, stream.BufferedBytes())
		offset = end
	}
	rows = append(rows, stream.Finish()...)
	joined := strings.Join(rows, "\n")
	if maxBuffered > maxMarkdownActiveSource {
		t.Fatalf("oversized fence retained %d bytes", maxBuffered)
	}
	if !strings.Contains(joined, "  ```go") || !strings.Contains(joined, "  line of code") || !strings.Contains(joined, "After the oversized fence.") {
		t.Fatalf("oversized fence fallback lost visible source or did not resume Markdown: prefix=%q suffix=%q", joined[:min(100, len(joined))], joined[max(0, len(joined)-200):])
	}
	if strings.Contains(joined, "\x1b[") {
		t.Fatalf("NO_COLOR fallback emitted SGR: %q", joined)
	}
	if got := strings.Count(joined, "line of code"); got != maxMarkdownActiveSource/8 {
		t.Fatalf("fallback lost or duplicated code lines: got=%d want=%d fallbackAt=%d rows=%d", got, maxMarkdownActiveSource/8, fallbackAt, len(rows))
	}
	if replay := strings.Join(RenderMarkdown(source, 50, true), "\n"); replay != joined {
		t.Fatal("stream and replay fallback differ")
	}
}

func TestMarkdownStreamMalformedAndSplitEscapeInputIsSafe(t *testing.T) {
	stream := NewMarkdownStream(40, true)
	var rows []string
	for _, chunk := range []string{"Visible ", "\x1b]8;;https://secret", ".test Hidden", "\a text\n\nNext"} {
		stable, _ := stream.Feed(chunk)
		rows = append(rows, stable...)
	}
	rows = append(rows, stream.Finish()...)
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "Hidden") || strings.Contains(joined, "\x1b") {
		t.Fatalf("terminal control payload escaped filtering: %q", joined)
	}
	if !strings.Contains(joined, "Visible") || !strings.Contains(joined, "text") || !strings.Contains(joined, "Next") {
		t.Fatalf("sanitizer discarded visible text: %q", joined)
	}
}

func TestMarkdownStreamPartialFenceEndsAtMessageBoundary(t *testing.T) {
	stream := NewMarkdownStream(60, true)
	stable, tail := stream.Feed("```go\nfmt.Println(\"partial\")\n``")
	if len(stable) != 0 {
		t.Fatalf("unfinished fence committed early: %q", stable)
	}
	if strings.Contains(strings.Join(tail, "\n"), "partial") == false {
		t.Fatalf("active code tail lost partial text: %q", tail)
	}
	first := append(stable, stream.Finish()...)
	if !strings.Contains(strings.Join(first, "\n"), "partial") {
		t.Fatalf("message finish lost partial code: %q", first)
	}
	stable, tail = stream.Feed("new message")
	if len(stable) != 0 || !strings.Contains(strings.Join(tail, "\n"), "new message") {
		t.Fatalf("partial fence swallowed next message: stable=%q tail=%q", stable, tail)
	}
	if got := strings.Join(stream.Finish(), "\n"); !strings.Contains(got, "new message") {
		t.Fatalf("next message not finalized: %q", got)
	}
}

func BenchmarkMarkdownStream1MiB(b *testing.B) {
	paragraph := strings.Repeat("A paragraph with **bold** text, [a link](https://example.test), and Unicode 世界. ", 42)
	source := strings.Repeat(paragraph+"\n\n", 300)
	b.SetBytes(int64(len(source)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		stream := NewMarkdownStream(80, true)
		for offset := 0; offset < len(source); offset += 8 * 1024 {
			end := min(len(source), offset+8*1024)
			stream.Feed(source[offset:end])
		}
		stream.Finish()
	}
}

func TestStreamedMarkdownMatchesSharedMessageFormatting(t *testing.T) {
	source := "## Assistant\n\nA **bold** answer with a [link](https://example.test).\n\n```go\nfmt.Println(\"done\")\n```"
	vt := testutil.NewVTModel(80, 24)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 80, 24 }}
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	if n, err := c.MarkdownWrite([]byte(source)); err != nil || n != len(source) {
		t.Fatalf("markdown write n=%d err=%v", n, err)
	}
	if err := c.CloseMarkdown(); err != nil {
		t.Fatal(err)
	}
	visible := strings.Join(vt.Scrollback, "\n")
	if len(vt.Scrollback) > 0 {
		visible += "\n"
	}
	visible += vt.Text()
	if border := strings.Index(visible, "\n────"); border >= 0 {
		visible = visible[:border]
	}
	got := strings.TrimRight(visible, "\n")
	want := strings.TrimRight(strings.Join(FormatMessage(source, 80, true), "\n"), "\n")
	if got != want {
		t.Fatalf("live Markdown differs from replay formatting\nwant:\n%s\ngot:\n%s\nscroll=%q screen=%q", want, got, vt.Scrollback, vt.Text())
	}
	if !strings.Contains(vt.Text(), "model") {
		t.Fatalf("footer disappeared after Markdown settlement: %s", vt.Text())
	}
}

func TestMarkdownActiveTailKeepsFooterAndReflowsOnResize(t *testing.T) {
	vt := testutil.NewVTModel(50, 12)
	width := 50
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return width, 12 }}
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	c.Status("⠋ Working…")
	if _, err := c.MarkdownWrite([]byte("## Active\n\nA partial **bold** assistant answer with enough words to wrap.")); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(c.streamRows, "\n")
	if !strings.Contains(vt.Text(), "model") || !strings.Contains(vt.Text(), "Working…") {
		t.Fatalf("active Markdown tail hid status/footer: %s", vt.Text())
	}
	width = 30
	c.Draw()
	after := strings.Join(c.streamRows, "\n")
	if before == after || !strings.Contains(after, "partial") || !strings.Contains(vt.Text(), "model") {
		t.Fatalf("resize did not reflow only the active Markdown tail: before=%q after=%q screen=%s", before, after, vt.Text())
	}
}

func TestMarkdownStreamSetWidthOnlyReflowsTail(t *testing.T) {
	stream := NewMarkdownStream(30, true)
	stable, _ := stream.Feed("A completed paragraph.\n\nAn active paragraph with enough words to wrap at the current width")
	if len(stable) == 0 {
		t.Fatal("first block was not committed")
	}
	before := fmt.Sprint(stable)
	tailBefore := strings.Join(stream.Tail(), "\n")
	tailAfter := strings.Join(stream.SetWidth(60), "\n")
	if before != fmt.Sprint(stable) || tailBefore == tailAfter {
		t.Fatalf("resize changed committed rows or failed to reflow active tail: stable=%q before=%q after=%q", stable, tailBefore, tailAfter)
	}
}
