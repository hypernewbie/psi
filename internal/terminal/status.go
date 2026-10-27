package terminal

import (
	"fmt"
	"io"
	"strings"
)

// Status renders compact tool status (Step 83): name + state only.
// No arguments, output bodies, diffs, expand-details, or result widgets.

type ToolState string

const (
	ToolRunning   ToolState = "running"
	ToolDone      ToolState = "done"
	ToolFailed    ToolState = "failed"
	ToolCancelled ToolState = "cancelled"
)

// FormatStatus is "read: done" style. Repeated updates are throttled by the
// caller (one line per transition, not per byte).
func FormatStatus(name string, st ToolState) string {
	return fmt.Sprintf("%s: %s", name, string(st))
}

// StatusWriter emits compact status lines; full results stay in the session.
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
		return // throttle repeated identical updates
	}
	if len(s.last) >= 1024 {
		clear(s.last)
	}
	s.last[key] = st
	fmt.Fprintln(s.w, Sanitize(FormatStatus(name, st)))
}

// StreamText prints assistant deltas in order without full redraw (Step 82).
// Old transcript lines remain scrollback; only the active prompt repositions.
type Streamer struct {
	w       io.Writer
	needsNL bool
	filter  Sanitizer
}

func NewStreamer(w io.Writer, noColor bool) *Streamer {
	return &Streamer{w: w} // the inline interface emits no color
}

func (s *Streamer) PrintUser(text string) {
	fmt.Fprintln(s.w, Sanitize(text))
	s.needsNL = false
}

func (s *Streamer) Delta(t string) {
	text := s.filter.Feed(t)
	fmt.Fprint(s.w, text)
	if text == "" {
		return
	}
	if !strings.HasSuffix(text, "\n") {
		s.needsNL = true
	} else {
		s.needsNL = false
	}
}

func (s *Streamer) Close() {
	s.filter = Sanitizer{}
	if s.needsNL {
		fmt.Fprintln(s.w)
		s.needsNL = false
	}
}
