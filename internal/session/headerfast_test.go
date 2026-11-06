package session

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestHeaderPrefixReadsAtMostOneKBForShortRecords(t *testing.T) {
	const header = `{"type":"session","version":3,"id":"header-id","cwd":"/ws"}`
	for _, tc := range []struct {
		name, first string
		wantError   bool
	}{
		{"valid header", header, false},
		{"corrupt first record", "broken record", true},
		{"non-header first record", `{"type":"session_info","id":"entry"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.first + "\n" + header + "\n" + strings.Repeat("x", 8192)
			r := &io.LimitedReader{R: strings.NewReader(data), N: int64(len(data))}
			h, err := readHeaderPrefix(r, "session.jsonl")
			if (err != nil) != tc.wantError {
				t.Fatalf("header = %+v, error = %v", h, err)
			}
			if !tc.wantError && (h == nil || h.ID != "header-id") {
				t.Fatalf("header = %+v", h)
			}
			if n := int64(len(data)) - r.N; n > 1024 {
				t.Fatalf("short first record read %d bytes, want at most 1024", n)
			}
		})
	}
}

func TestHeaderPrefixLongHeadersAndBoundaries(t *testing.T) {
	const base = `{"type":"session","version":3,"id":"header-id","cwd":"/ws","extra":""}`
	for _, tc := range []struct {
		name, data string
		wantError  bool
	}{
		{"no final newline", base, false},
		{"leading blanks and CRLF", "\r\n \t\r\n" + base + "\r\n", false},
		{"long header", strings.Replace(base, `"extra":""`, `"extra":"`+strings.Repeat("x", 2048)+`"`, 1), false},
		{"exact limit", strings.Replace(base, `"extra":""`, `"extra":"`+strings.Repeat("x", maxHeaderFastScan-len(base))+`"`, 1), false},
		{"over limit", strings.Repeat(" ", maxHeaderFastScan) + base, true},
		{"empty file", "", true},
		{"truncated header", strings.TrimSuffix(base, "}"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &io.LimitedReader{R: strings.NewReader(tc.data), N: int64(len(tc.data))}
			h, err := readHeaderPrefix(r, "session.jsonl")
			if (err != nil) != tc.wantError {
				t.Fatalf("header = %+v, error = %v", h, err)
			}
			if !tc.wantError && (h == nil || h.ID != "header-id") {
				t.Fatalf("header = %+v", h)
			}
			if n := int64(len(tc.data)) - r.N; n > maxHeaderFastScan+1 {
				t.Fatalf("read %d bytes beyond the header scan limit", n)
			}
		})
	}
}

func TestHeaderPrefixPropagatesReadError(t *testing.T) {
	want := errors.New("read failure")
	if _, err := readHeaderPrefix(iotest.ErrReader(want), "session.jsonl"); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
