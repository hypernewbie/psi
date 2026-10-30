package terminal

import (
	"reflect"
	"strings"
	"testing"

	"psi/internal/testutil"
)

func TestTranscriptGapUsesCommittedRowsAndPendingFrames(t *testing.T) {
	for _, batched := range []bool{false, true} {
		for _, prefix := range []string{"", "notice\r\n", "notice\r\n\r\n", "notice\r\n\x1b[2m   \x1b[0m\r\n"} {
			vt := testutil.NewVTModel(50, 20)
			c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 50, 20 }}
			c.SetPresentation(PresentationSnapshot{NoColor: true})
			c.Draw()
			if batched {
				c.BeginFrame()
			}
			c.Write([]byte(prefix))
			if err := c.EnsureTranscriptGap(); err != nil {
				t.Fatal(err)
			}
			if err := c.EnsureTranscriptGap(); err != nil {
				t.Fatal(err)
			}
			c.Write([]byte("NEXT_BLOCK\r\n"))
			if batched {
				if err := c.EndFrame(); err != nil {
					t.Fatal(err)
				}
			}
			rows := append(append([]string{}, vt.Scrollback...), strings.Split(vt.Text(), "\n")...)
			next := -1
			for i, row := range rows {
				if row == "NEXT_BLOCK" {
					next = i
				}
			}
			want := 2
			if prefix == "" {
				want = 0
			}
			if next != want {
				t.Fatalf("gap count changed for %q batch=%v: %q", prefix, batched, rows)
			}
		}
	}
}

func TestWhitespaceOnlyMarkdownDoesNotAddTranscriptGaps(t *testing.T) {
	for _, text := range []string{"", " ", "\n\n", " \r\n\t "} {
		t.Run(strings.ReplaceAll(text, "\n", "NL"), func(t *testing.T) {
			vt := testutil.NewVTModel(60, 20)
			c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 60, 20 }}
			c.SetPresentation(PresentationSnapshot{NoColor: true})
			c.Draw()
			c.Write([]byte("tool one\r\n"))
			stream := NewStreamer(StreamOutput{Console: c}, true)
			stream.Delta(text)
			stream.Flush()
			stream.Close()
			stream.Close()
			c.Write([]byte("tool two\r\n"))
			rows := append(append([]string{}, vt.Scrollback...), strings.Split(vt.Text(), "\n")...)
			one, two := -1, -1
			for i, row := range rows {
				if strings.Contains(row, "tool one") {
					one = i
				}
				if strings.Contains(row, "tool two") {
					two = i
				}
			}
			if one < 0 || two != one+1 {
				t.Fatalf("empty assistant inserted a gap: %q", rows)
			}
		})
	}
}

func TestLiveMarkdownFirstFrameUsesReplayDisplay(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		for _, source := range []string{"answer", "```go\ncode\n```", "## Header\n\nfirst\n\nsecond\n"} {
			vt := testutil.NewVTModel(50, 20)
			c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 50, 20 }}
			display := DisplaySettings{OutputPad: 0, CodeBlockIndent: "> ", AutocompleteMaxVisible: 3}
			c.SetPresentation(PresentationSnapshot{NoColor: noColor, Display: display})
			c.Draw()
			stream := NewStreamer(StreamOutput{Console: c}, noColor)
			stream.Delta(source)
			stream.Close()
			want := FormatAssistantMessageWithDisplay(source, 50, noColor, display)
			rows := append(append([]string{}, vt.Scrollback...), strings.Split(vt.Text(), "\n")...)
			for i := range want {
				want[i] = strings.TrimRight(Sanitize(want[i]), " ")
			}
			if len(rows) < len(want) || !reflect.DeepEqual(rows[:len(want)], want) {
				t.Fatalf("live/replay display differs for %q color=%v: got %q want %q", source, !noColor, rows, want)
			}
		}
	}
}
