package app

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func feedCollector(collector *shellCollector, chunks ...string) {
	for _, chunk := range chunks {
		collector.Feed([]byte(chunk))
	}
}

func TestBangShellOutputBoundaries(t *testing.T) {
	t.Run("split utf8 osc csi and crlf", func(t *testing.T) {
		collector := newShellCollector()
		feedCollector(collector, "A", "\xf0", "\x9f", "\x8c", "\x8d") // split 🌍
		feedCollector(collector, "\x1b]0;ti", "tle\x07OK")
		feedCollector(collector, "\x1b[3", "1mRED")
		feedCollector(collector, "\r", "\nnext")
		feedCollector(collector, "\rlone")
		got := collector.Finalize()
		want := "A🌍OKRED\nnext\nlone"
		if got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})

	t.Run("trailing cr and incomplete suffixes", func(t *testing.T) {
		collector := newShellCollector()
		feedCollector(collector, "tail\r")
		if got := collector.Finalize(); got != "tail\n" {
			t.Fatalf("trailing CR = %q", got)
		}
		if got := collector.Finalize(); got != "tail\n" {
			t.Fatalf("repeated finalize = %q", got)
		}
		collector.Feed([]byte("ignored"))
		if got := collector.Text(); got != "tail\n" {
			t.Fatalf("feed after finalize = %q", got)
		}

		collector = newShellCollector()
		feedCollector(collector, "ok\x1b]0;unfinished")
		if got := collector.Finalize(); got != "ok" {
			t.Fatalf("incomplete OSC leaked: %q", got)
		}
		collector = newShellCollector()
		feedCollector(collector, "ok\x1b")
		if got := collector.Finalize(); got != "ok" {
			t.Fatalf("incomplete escape leaked: %q", got)
		}
		collector = newShellCollector()
		feedCollector(collector, "ok\xf0\x9f")
		if got := collector.Finalize(); got != "ok" {
			t.Fatalf("incomplete UTF-8 leaked: %q", got)
		}
	})

	t.Run("invalid bytes", func(t *testing.T) {
		collector := newShellCollector()
		feedCollector(collector, "a\xffb\x00c")
		if got := collector.Finalize(); got != "abc" {
			t.Fatalf("invalid bytes = %q", got)
		}
		if !utf8.ValidString(collector.Text()) {
			t.Fatal("collector published invalid UTF-8")
		}
	})

	t.Run("byte boundary", func(t *testing.T) {
		collector := newShellCollector()
		feedCollector(collector, strings.Repeat("x", shellOutputBytes))
		if collector.Finalize() == "" || collector.Truncated() {
			t.Fatalf("exact byte bound truncated=%v len=%d", collector.Truncated(), len(collector.Text()))
		}
		collector = newShellCollector()
		feedCollector(collector, strings.Repeat("x", shellOutputBytes+1))
		got := collector.Finalize()
		if !collector.Truncated() || len(got) != shellOutputBytes {
			t.Fatalf("one over: truncated=%v len=%d", collector.Truncated(), len(got))
		}
		collector = newShellCollector()
		feedCollector(collector, strings.Repeat("x", shellOutputBytes-1), "🌍")
		got = collector.Finalize()
		if len(got) > shellOutputBytes || !utf8.ValidString(got) {
			t.Fatalf("multibyte cut: len=%d valid=%v", len(got), utf8.ValidString(got))
		}
	})

	t.Run("line boundary", func(t *testing.T) {
		collector := newShellCollector()
		feedCollector(collector, strings.Repeat("\n", shellOutputLines-1))
		if collector.Finalize() != strings.Repeat("\n", shellOutputLines-1) || collector.Truncated() {
			t.Fatalf("exact line bound truncated=%v", collector.Truncated())
		}
		collector = newShellCollector()
		feedCollector(collector, strings.Repeat("\n", shellOutputLines))
		got := collector.Finalize()
		if !collector.Truncated() || strings.Count(got, "\n") != shellOutputLines-1 {
			t.Fatalf("one over line bound: truncated=%v newlines=%d", collector.Truncated(), strings.Count(got, "\n"))
		}
	})

	t.Run("diagnostic survives unfinished child escape", func(t *testing.T) {
		collector := newShellCollector()
		feedCollector(collector, "child\x1b]0;")
		collector.Finalize()
		collector.AppendDiagnostic("shell: capture failed")
		got := collector.Text()
		if !strings.Contains(got, "child") || !strings.Contains(got, "shell: capture failed") {
			t.Fatalf("diagnostic lost: %q", got)
		}
	})

	t.Run("bounded output is valid utf8", func(t *testing.T) {
		collector := newShellCollector()
		var raw bytes.Buffer
		for i := 0; i < 2000; i++ {
			raw.WriteString("line-")
			raw.WriteString(strings.Repeat("界", 40))
			raw.WriteByte('\n')
		}
		collector.Feed(raw.Bytes())
		got := collector.Finalize()
		if !utf8.ValidString(got) || len(got) > shellOutputBytes {
			t.Fatalf("bounded output len=%d valid=%v", len(got), utf8.ValidString(got))
		}
	})
}
