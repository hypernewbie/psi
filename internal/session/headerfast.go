package session

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
)

const (
	headerFastBufSize = 1024
	maxHeaderFastScan = 1024 * 1024
)

func readHeaderFast(path string) (*SessionHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readHeaderPrefix(f, path)
}

// Start with 1KB, extending only for a long header or leading blank lines.
// Keep unsupported headers with their errors so ID lookup does not treat an
// existing legacy session as absent and create a second one under the same ID.
func readHeaderPrefix(r io.Reader, path string) (*SessionHeader, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, maxHeaderFastScan+1), headerFastBufSize)
	var scanned int
	for {
		line, err := br.ReadBytes('\n')
		scanned += len(line)
		if scanned > maxHeaderFastScan {
			return nil, fmt.Errorf("session header exceeds %d-byte scan limit: %s", maxHeaderFastScan, path)
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if data := bytes.TrimSpace(line); len(data) > 0 {
			_, header, parseErr := metadataRecord(data)
			if parseErr != nil {
				return nil, parseErr
			}
			if header == nil {
				return nil, fmt.Errorf("missing session header")
			}
			return header, checkVersion(header, path)
		}
		if err == io.EOF {
			return nil, fmt.Errorf("missing session header")
		}
	}
}
