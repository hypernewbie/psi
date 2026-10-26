package session

import "io"

// limitReader caps total discovery reads (bounded memory for session lists).
func limitReader(r io.Reader, n int64) io.Reader {
	return io.LimitReader(r, n)
}
