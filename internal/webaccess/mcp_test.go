package webaccess

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/iotest"
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

func TestReadSSEEnvelopeDistinguishesDisconnectFromEndOfEvent(t *testing.T) {
	const payload = `{"jsonrpc":"2.0","id":"id","result":{}}`
	const line = "data: " + payload + "\n"
	for _, tt := range []struct {
		name  string
		body  io.Reader
		valid bool
	}{
		{"disconnected before delimiter", io.MultiReader(strings.NewReader(line), iotest.ErrReader(io.ErrUnexpectedEOF)), false},
		{"complete event before disconnect", io.MultiReader(strings.NewReader(line+"\n"), iotest.ErrReader(io.ErrUnexpectedEOF)), true},
		{"clean EOF without delimiter", strings.NewReader(line), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readSSEEnvelope(tt.body, "id", 1024)
			if tt.valid {
				if err != nil || string(got) != payload {
					t.Fatalf("got %q, %v; want matching envelope", got, err)
				}
			} else if err == nil || !hasClass(err.Error(), classInvalid) || got != nil {
				t.Fatalf("got %q, %v; want invalid response with no envelope", got, err)
			}
		})
	}
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
