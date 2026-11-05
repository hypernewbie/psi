package session

// The metadata scanner is the session's second reader: it reads the same
// records as the eager reader without keeping the payloads. These helpers
// drive it from tests.

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
)

// scanMetadataEntries scans a session file with the metadata scanner.
func scanMetadataEntries(path string) (*SessionHeader, []*Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return scanMetadataBytes(data)
}

// scanMetadataBytes scans session records with the metadata scanner.
func scanMetadataBytes(data []byte) (*SessionHeader, []*Entry, error) {
	var header *SessionHeader
	var entries []*Entry
	br := bufio.NewReader(bytes.NewReader(data))
	for lineNo := 1; ; lineNo++ {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			record := trimRecord(line)
			if len(bytes.TrimSpace(record)) > 0 {
				entry, h, scanErr := metadataRecord(record)
				if scanErr != nil {
					return nil, nil, fmt.Errorf("line %d: %w", lineNo, scanErr)
				}
				if h != nil {
					header = h
				} else if entry != nil {
					entries = append(entries, entry)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return header, entries, nil
}
