package webaccess

import (
	"bytes"
	"io"
	"testing"
)

type countingReader struct {
	reader io.Reader
	read   int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

func TestReadSSEEnvelopeBoundsUnterminatedLine(t *testing.T) {
	source := &countingReader{reader: bytes.NewReader(bytes.Repeat([]byte("x"), 1<<20))}
	if _, err := readSSEEnvelope(source, "id", 1024); err == nil {
		t.Fatal("unterminated over-limit stream accepted")
	}
	if source.read > 1025 {
		t.Fatalf("read %d bytes past the response limit", source.read)
	}
}
