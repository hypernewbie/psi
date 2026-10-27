package session

import (
	"bytes"
	"fmt"
)

// Metadata contains no text, arguments, image data, signatures, or tool output.
// The JSON decoder validates and skips those fields without retaining them.
func metadataEntry(data []byte) (*Entry, error) {
	e, header, err := metadataRecord(data)
	if err == nil && header != nil {
		return nil, fmt.Errorf("unexpected session header")
	}
	return e, err
}

func metadataRecord(data []byte) (*Entry, *SessionHeader, error) {
	// Single-pass hand scan (F05/P3-018/S12): byte-exact keys, strict
	// leaves, no payload retention. Agreement with the eager reader is
	// guarded by the differential fuzzer.
	return handScanRecord(data)
}

func trimRecord(data []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSuffix(data, []byte("\n")), []byte("\r"))
}
