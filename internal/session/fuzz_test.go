package session

import (
	"bytes"
	"testing"
)

func FuzzDecodeEntries(f *testing.F) {
	f.Add([]byte(`{"type":"session","version":3,"id":"fuzz","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp"}` + "\n"))
	f.Add([]byte("not json\n"))
	f.Add([]byte("{\"type\":\"message\","))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024*1024 {
			return
		}
		_, entries, _, err := DecodeEntries(bytes.NewReader(data), "fuzz")
		if err != nil {
			return
		}
		path, err := BuildPath(entries, "")
		if err != nil {
			return
		}
		projection := BuildProjection(path)
		_ = CheckContinuable(projection.Flat, projection.Entries)
	})
}
