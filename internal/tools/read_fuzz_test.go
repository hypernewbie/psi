package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const readReferenceFragmentBytes = 4 * 1024 // Default bufio.Reader buffer size used by read.

func FuzzReadSkippedPrefix(f *testing.F) {
	// Keep fuzz inputs short while still generating files that cross reader
	// buffer boundaries and the output caps.
	add := func(head, unit, tail string, repeats, offset, limit int) {
		f.Add(head, unit, tail, uint16(repeats), uint32(offset-1), uint16(limit-1))
	}
	const prefix = "first\nskip\n"
	const pairs = 600
	// Each pair contributes two skipped lines, including a blank line.
	add("first\n", "skip\x00\r\n\n", "chosen α\r\nchosen β\nlast", pairs, 2+2*pairs, 2)
	// The skipped line's CR and LF straddle the first buffer boundary.
	add(prefix, "s", "\r\nchosen\r\nunread\x00\n", readReferenceFragmentBytes-len(prefix)-1, 4, 1)
	// The NUL is after the first fragment that exceeds the byte cap.
	const rejectedFragmentEnd = (DefaultMaxBytes/readReferenceFragmentBytes + 1) * readReferenceFragmentBytes
	add(prefix, "x", "\x00\n", rejectedFragmentEnd, 3, 1)
	add(prefix, "\r\n", "unread\x00", DefaultMaxLines, 3, DefaultMaxLines+1)
	add(prefix, "x", "", readReferenceFragmentBytes, 3, 1)

	f.Fuzz(func(t *testing.T, head, unit, tail string, repeats uint16, offsetIndex uint32, limitIndex uint16) {
		const maxFileBytes = 128 * 1024
		remaining := maxFileBytes - len(head) - len(tail)
		if remaining < 0 {
			return
		}
		n := min(int(repeats), remaining/max(1, len(unit)))
		data := head + strings.Repeat(unit, n) + tail
		// Map to positive arguments. Include one offset beyond even an
		// unterminated final line, and a limit above the tool's line cap.
		offset := 1 + int(offsetIndex%uint32(strings.Count(data, "\n")+2))
		limit := 1 + int(limitIndex%(DefaultMaxLines+1))
		dir := t.TempDir()
		const name = "target.txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		tool := &ReadTool{Cwd: dir}
		got := tool.ExecuteCtx(t.Context(), Invocation{
			Name:      Read,
			Arguments: map[string]any{"path": name, "offset": offset, "limit": limit},
		})
		want := readTextReference(data, offset, limit)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%d bytes, offset %d, limit %d: got %+v, want %+v", len(data), offset, limit, got, want)
		}
	})
}

// readTextReference walks complete LF-delimited lines instead of simulating
// reader refills or batching. Only selected lines are split into fragments.
func readTextReference(data string, offset, limit int) Result {
	if data == "" && offset == 1 {
		return textResult("(empty file)")
	}
	var body strings.Builder
	number := 0
	for line := range strings.SplitAfterSeq(data, "\n") {
		if line == "" {
			break // No line after a final LF.
		}
		number++
		if number < offset {
			continue
		}
		if number-offset == min(limit, DefaultMaxLines) {
			return readReferencePage(body.String(), number)
		}
		for line != "" {
			n := min(len(line), readReferenceFragmentBytes)
			part := line[:n]
			if strings.IndexByte(part, 0) >= 0 {
				return errResult("read: file contains binary data")
			}
			if body.Len()+n > DefaultMaxBytes {
				return readReferencePage(body.String(), number)
			}
			body.WriteString(part)
			line = line[n:]
		}
	}
	if body.Len() == 0 {
		return errResult(fmt.Sprintf("read: offset %d is beyond end of file", offset))
	}
	return readReferencePage(body.String(), 0)
}

func readReferencePage(body string, next int) Result {
	if next != 0 {
		body += fmt.Sprintf("\n\n[Output truncated. Use offset=%d to continue; use bash for a line larger than 50 KiB.]", next)
	}
	return Result{Content: []Block{{Type: "text", Text: body}}, Details: map[string]any{"truncated": next != 0}}
}
