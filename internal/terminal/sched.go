package terminal

import (
	"io"
	"unicode/utf8"
)

const (
	maxFPS       = 30
	maxWriteUnit = 64 * 1024
)

// WriteBounded handles short writes without losing or duplicating their suffix.
func WriteBounded(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n := min(len(b), maxWriteUnit)
		m, err := w.Write(b[:n])
		if m < 0 || m > n {
			return io.ErrShortWrite
		}
		if err != nil {
			return err
		}
		if m == 0 {
			return io.ErrShortWrite
		}
		b = b[m:]
	}
	return nil
}

// Batches splits text at UTF-8 boundaries for bounded transcript output.
func Batches(s string, limit int) []string {
	if limit <= 0 {
		limit = maxWriteUnit
	}
	limit = max(limit, utf8.UTFMax)
	var out []string
	for len(s) > limit {
		i := limit
		for i > 0 && !runeStart(s[i]) {
			i--
		}
		if i == 0 {
			i = limit
		}
		out = append(out, s[:i])
		s = s[i:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

func runeStart(b byte) bool { return b&0xC0 != 0x80 }
