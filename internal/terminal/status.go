package terminal

import (
	"fmt"
	"io"
	"strings"
)

type ToolState string

const (
	ToolRunning   ToolState = "running"
	ToolDone      ToolState = "done"
	ToolFailed    ToolState = "failed"
	ToolCancelled ToolState = "cancelled"
)

func FormatStatus(name string, st ToolState) string {
	return fmt.Sprintf("%s: %s", name, string(st))
}

// StatusWriter commits one settled line. A running state belongs to the owned
// live tail, not to immutable scrollback.
type StatusWriter struct {
	w    io.Writer
	last map[string]ToolState
}

func NewStatusWriter(w io.Writer) *StatusWriter {
	return &StatusWriter{w: w, last: map[string]ToolState{}}
}

func (s *StatusWriter) Update(name, callID string, st ToolState) {
	key := name + "\x00" + callID
	if s.last[key] == st {
		return
	}
	if len(s.last) >= 1024 {
		clear(s.last)
	}
	s.last[key] = st
	if st != ToolRunning {
		fmt.Fprintln(s.w, Sanitize(FormatStatus(name, st)))
	}
}

// Streamer buffers deltas until the event loop's frame timer or an operation
// boundary. It shares the console writer and bounds pending output by bytes.
type Streamer struct {
	w       io.Writer
	needsNL bool
	filter  Sanitizer
	pending strings.Builder
	err     error
}

func NewStreamer(w io.Writer, noColor bool) *Streamer { return &Streamer{w: w} }

func (s *Streamer) PrintUser(text string) {
	s.Close()
	fmt.Fprintln(s.w, Sanitize(text))
}

func (s *Streamer) Delta(t string) {
	text := s.filter.Feed(t)
	for len(text) > 0 {
		n := min(len(text), maxWriteUnit-s.pending.Len())
		s.pending.WriteString(text[:n])
		text = text[n:]
		if s.pending.Len() == maxWriteUnit {
			s.Flush()
		}
	}
}

func (s *Streamer) Pending() bool { return s.pending.Len() > 0 }

func (s *Streamer) Flush() {
	if s.pending.Len() == 0 {
		return
	}
	text := s.pending.String()
	if s.err == nil {
		s.err = WriteBounded(s.w, []byte(text))
	}
	s.needsNL = !strings.HasSuffix(text, "\n")
	s.pending.Reset()
}

func (s *Streamer) Close() {
	s.Flush()
	s.filter = Sanitizer{}
	if s.needsNL {
		if s.err == nil {
			s.err = WriteBounded(s.w, []byte("\n"))
		}
		s.needsNL = false
	}
}
