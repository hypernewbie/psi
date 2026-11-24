package terminal

import (
	"fmt"
	"strings"
	"testing"
)

func shellOutputLines(count int) string {
	var b strings.Builder
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&b, "line %02d\n", i)
	}
	return b.String()
}

func TestBangShellLiveAndReplay(t *testing.T) {
	t.Run("collapsed keeps the newest logical lines and expansion restores the tail", func(t *testing.T) {
		output := "EARLY_MARKER\n" + shellOutputLines(40) + "LATE_MARKER\n"
		collapsed := FormatShellBlock(ShellDisplay{Command: "printf output", Output: output, State: "done"}, 80, true)
		joined := strings.Join(collapsed, "\n")
		if strings.Contains(joined, "EARLY_MARKER") {
			t.Fatalf("collapsed block leaked the early marker:\n%s", joined)
		}
		if !strings.Contains(joined, "LATE_MARKER") {
			t.Fatalf("collapsed block lacks the late marker:\n%s", joined)
		}
		if !strings.Contains(joined, "earlier lines (Ctrl+O to expand)") {
			t.Fatalf("collapsed block lacks the hidden-line count:\n%s", joined)
		}
		expanded := strings.Join(FormatShellBlock(ShellDisplay{Command: "printf output", Output: output, State: "done", Expanded: true}, 80, true), "\n")
		if !strings.Contains(expanded, "EARLY_MARKER") || !strings.Contains(expanded, "LATE_MARKER") {
			t.Fatalf("expanded block lost retained lines:\n%s", expanded)
		}
	})

	t.Run("collapsed wrapping is capped at twenty rows at two widths", func(t *testing.T) {
		line := strings.Repeat("wide ", 40)
		for _, width := range []int{40, 80} {
			rows := FormatShellBlock(ShellDisplay{Command: "printf wrapped", Output: line, State: "done"}, width, true)
			body := 0
			for _, row := range rows[1:] {
				if CellWidth(row) > width-1 {
					t.Fatalf("width %d row exceeds budget: %q (%d cells)", width, row, CellWidth(row))
				}
				if strings.Contains(row, "wide") {
					body++
				}
			}
			if body > shellCollapsedWrappedRows {
				t.Fatalf("width %d retained %d wrapped rows, want at most %d", width, body, shellCollapsedWrappedRows)
			}
		}
	})

	t.Run("excluded header has a plain-text cue and dim token", func(t *testing.T) {
		rows := FormatShellBlock(ShellDisplay{Command: "echo private", Output: "private", Excluded: true, State: "done"}, 80, true)
		if !strings.HasPrefix(rows[0], "!! $ ") {
			t.Fatalf("no-color excluded header = %q", rows[0])
		}
		colored := FormatShellBlock(ShellDisplay{Command: "echo private", Output: "private", Excluded: true, State: "done"}, 80, false)
		if strings.HasPrefix(colored[0], "!! ") {
			t.Fatalf("color excluded header uses the fallback text cue: %q", colored[0])
		}
		plain := FormatShellBlock(ShellDisplay{Command: "echo public", Output: "public"}, 80, true)
		if strings.HasPrefix(plain[0], "!! ") {
			t.Fatalf("included header marked excluded: %q", plain[0])
		}
	})

	t.Run("imported control bytes and huge tails are bounded", func(t *testing.T) {
		output := "EARLY\n\x1b]0;title\x07\xff\xfe" + strings.Repeat("x", shellDisplayBytes+100)
		rows := FormatShellBlock(ShellDisplay{Command: "bad\x1b[2Jcommand", Output: output, State: "done"}, 60, true)
		joined := strings.Join(rows, "\n")
		if strings.ContainsRune(joined, '\x1b') || strings.Contains(joined, "\xff") {
			t.Fatalf("unsafe bytes reached the display: %q", joined)
		}
		for _, row := range rows {
			if CellWidth(row) > 59 {
				t.Fatalf("row exceeds width: %q (%d)", row, CellWidth(row))
			}
		}
		if !strings.Contains(joined, "[Output truncated]") {
			t.Fatalf("bounded imported output lacks a truncation cue:\n%s", joined)
		}
		if strings.Contains(joined, "[2J") {
			t.Fatalf("control sequence text reached the display: %q", joined)
		}
	})

	t.Run("outcomes and truncation stay explicit", func(t *testing.T) {
		code := 7
		failed := strings.Join(FormatShellBlock(ShellDisplay{Command: "false", State: "failed", ExitCode: &code}, 80, true), "\n")
		if !strings.Contains(failed, "(exit 7)") {
			t.Fatalf("failed block = %q", failed)
		}
		cancelled := strings.Join(FormatShellBlock(ShellDisplay{Command: "sleep 1", State: "cancelled"}, 80, true), "\n")
		if !strings.Contains(cancelled, "(cancelled)") {
			t.Fatalf("cancelled block = %q", cancelled)
		}
		running := strings.Join(FormatShellBlock(ShellDisplay{Command: "sleep 1", State: "running"}, 80, true), "\n")
		if !strings.Contains(running, "Running... (Esc to cancel)") {
			t.Fatalf("running block = %q", running)
		}
		truncated := strings.Join(FormatShellBlock(ShellDisplay{Command: "yes", Output: "tail", State: "done", Truncated: true, FullOutputPath: "/tmp/full output"}, 80, true), "\n")
		if !strings.Contains(truncated, "[Output truncated. Full output: /tmp/full output]") {
			t.Fatalf("truncated block = %q", truncated)
		}
		empty := strings.Join(FormatShellBlock(ShellDisplay{Command: "true", State: "done"}, 80, true), "\n")
		if !strings.Contains(empty, "(no output)") {
			t.Fatalf("empty block = %q", empty)
		}
	})

	t.Run("live rows survive the raw fragment fallback", func(t *testing.T) {
		console, screen, _ := newTestConsole(30, 12)
		console.streamFragment = []byte("RAW_FRAGMENT")
		console.SetShellPreview([]string{"SHELL_LIVE_ROW"})
		console.Draw()
		if !strings.Contains(screen.Text(), "SHELL_LIVE_ROW") {
			t.Fatalf("raw fallback hid the live shell row: %q", screen.Text())
		}
	})

	t.Run("small geometry keeps the shell preview bounded", func(t *testing.T) {
		console, screen, _ := newTestConsole(20, 6)
		console.SetShellPreview([]string{"second", "third", "fourth", "SHELL_SMALL_ROW"})
		console.Draw()
		if !strings.Contains(screen.Text(), "SHELL_SMALL_ROW") {
			t.Fatalf("small geometry dropped the live shell row: %q", screen.Text())
		}
	})

	t.Run("small width does not overflow the terminal", func(t *testing.T) {
		rows := FormatShellBlock(ShellDisplay{Command: strings.Repeat("command ", 20), Output: strings.Repeat("output ", 20), State: "done"}, 10, true)
		if len(rows) == 0 {
			t.Fatal("small-width block produced no rows")
		}
		for _, row := range rows {
			if CellWidth(row) > 9 {
				t.Fatalf("small-width row exceeds budget: %q", row)
			}
		}
	})
}
