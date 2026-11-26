package app

import (
	"strings"
	"testing"
)

func feedCollector(collector *shellCollector, chunks ...string) {
	for _, chunk := range chunks {
		collector.Feed([]byte(chunk))
	}
}

func TestBangShellOutputBoundaries(t *testing.T) {
	t.Run("split UTF-8, escapes and line endings", func(t *testing.T) {
		var collector shellCollector
		feedCollector(&collector, "A\xf0", "\x9f", "\x8c\x8d") // split 🌍
		feedCollector(&collector, "\x1b]0;ti", "tle\x07OK", "\x1b[3", "1mRED")
		feedCollector(&collector, "\r", "\nnext\xff", "\rlone\x00\r")
		got, truncated := collector.finish("")
		if want := "A🌍OKRED\nnext\nlone\n"; got != want || truncated {
			t.Fatalf("output = %q, truncated = %v; want %q, not truncated", got, truncated, want)
		}
	})

	t.Run("truncated UTF-8 tail does not depend on chunking", func(t *testing.T) {
		longLine := strings.Repeat("界", shellOutputBytes/len("界")+1)
		want := strings.Repeat("界", (shellOutputBytes-1)/len("界")) + "\n"
		for _, chunks := range [][]string{{longLine + "\n"}, {longLine, "\n"}} {
			var collector shellCollector
			feedCollector(&collector, chunks...)
			got, truncated := collector.finish("")
			if !truncated || got != want {
				t.Fatalf("%d chunks: output length = %d, truncated = %v; want length %d and exact UTF-8 tail", len(chunks), len(got), truncated, len(want))
			}
		}
	})

	t.Run("exact and over-limit tails", func(t *testing.T) {
		for _, tc := range []struct{ name, tail string }{
			{"bytes", strings.Repeat("x", shellOutputBytes)},
			{"lines", strings.Repeat("line\n", shellOutputLines-1) + "last"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var exact, over shellCollector
				feedCollector(&exact, tc.tail)
				feedCollector(&over, "discard\n", tc.tail)
				if got, truncated := exact.finish(""); got != tc.tail || truncated {
					t.Fatalf("exact: length = %d, truncated = %v", len(got), truncated)
				}
				if got, truncated := over.finish(""); got != tc.tail || !truncated {
					t.Fatalf("over: length = %d, truncated = %v", len(got), truncated)
				}
			})
		}
	})

	t.Run("diagnostic survives incomplete child output", func(t *testing.T) {
		for _, suffix := range []string{"\x1b]0;unfinished", "\x1b", "\xf0\x9f"} {
			var collector shellCollector
			feedCollector(&collector, "child", suffix)
			got, truncated := collector.finish("shell: capture failed")
			if want := "child\nshell: capture failed"; got != want || truncated {
				t.Fatalf("suffix %q: output = %q, truncated = %v; want %q, not truncated", suffix, got, truncated, want)
			}
		}
	})
}
