package mcp

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func FuzzStdioFraming(f *testing.F) {
	f.Add("", uint8(0), uint16(0))
	f.Add(stdioFinalFirst+"\n"+stdioFinalLast+"\n", uint8(11), uint16(0))
	// A complete JSON reply without its final newline is still incomplete.
	f.Add(stdioFinalFirst+"\n"+stdioFinalLast+"\n", uint8(0), uint16(1))
	f.Add(stdioFinalFirst+"\n"+stdioFinalLast+"\n", uint8(7), uint16(20))
	f.Add(strings.Repeat(" ", 32)+"\r\n\n"+stdioFinalFirst+"\r\n", uint8(5), uint16(0))
	f.Add("\xff\x00not-json\r\n\t", uint8(2), uint16(0))

	f.Fuzz(func(t *testing.T, wire string, chunk uint8, tailBytes uint16) {
		// The frame size limit is covered separately. Keep fuzz iterations small.
		if len(wire) > 4<<10 {
			return
		}
		drop := int(tailBytes) % (len(wire) + 1)
		wire = wire[:len(wire)-drop]
		input := strings.NewReader(wire)
		reader := stdioReaderFunc(func(p []byte) (int, error) {
			return input.Read(p[:min(len(p), int(chunk)+1)])
		})
		// Use a small buffer to exercise multi-chunk frames even with short inputs.
		connection := &stdioConnection{stdout: bufio.NewReaderSize(reader, 16)}

		// Compare against whole-stream splitting, independent of read boundaries.
		rest := wire
		for {
			line, next, complete := strings.Cut(rest, "\n")
			if !complete {
				break
			}
			rest = next
			if strings.TrimSpace(line) == "" {
				continue
			}
			want := strings.TrimSuffix(line, "\r")
			got, err := connection.readFrame()
			if err != nil || string(got) != want {
				t.Fatalf("frame = %q, %v; want %q", got, err, want)
			}
		}

		got, err := connection.readFrame()
		if len(got) != 0 {
			t.Fatalf("unexpected frame at end of stream: %q", got)
		}
		if rest == "" {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("after complete frames: %v; want EOF", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "incomplete stdio frame") {
			t.Fatalf("after unterminated tail %q: %v; want incomplete frame error", rest, err)
		}
	})
}
