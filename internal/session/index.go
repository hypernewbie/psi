package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// RecordIndex is the entry index (P2-066): offsets + small envelope fields.
// It decodes large bodies on demand and never retains every payload.
// Reference: session-manager.ts buildSessionPath + reader streaming.
type RecordIndex struct {
	Records []RecordMeta
	Path    string
}

type RecordMeta struct {
	Offset   int64
	Length   int64
	Type     string
	ID       string
	ParentID string
}

// BuildIndex scans JSONL without retaining large payloads (bounded metadata).
// It enforces the 32 MiB record limit and reports record+limit on violation.
func BuildIndex(path string) (*RecordIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return BuildIndexFrom(f, path)
}

func BuildIndexFrom(r io.Reader, path string) (*RecordIndex, error) {
	idx := &RecordIndex{Path: path}
	var offset int64
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := readRecord(br)
		if err != nil && len(line) == 0 {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		length := int64(len(line))
		// Handle final record without newline.
		hasNewline := length > 0 && line[length-1] == '\n'
		trimmed := line
		if hasNewline {
			trimmed = line[:length-1]
			if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '\r' {
				trimmed = trimmed[:len(trimmed)-1]
			}
		}
		if len(bytes.TrimSpace(trimmed)) == 0 {
			offset += length
			if err == io.EOF {
				break
			}
			continue
		}
		if int64(len(trimmed)) > MaxRecordBytes {
			return nil, fmt.Errorf("record exceeds %d bytes at offset %d", MaxRecordBytes, offset)
		}
		// Decode envelope only.
		var probe struct {
			Type     string  `json:"type"`
			ID       string  `json:"id"`
			ParentID *string `json:"parentId"`
		}
		if err := json.Unmarshal(trimmed, &probe); err != nil {
			return nil, fmt.Errorf("invalid record at offset %d: %w", offset, err)
		} else {
			pid := ""
			if probe.ParentID != nil {
				pid = *probe.ParentID
			}
			idx.Records = append(idx.Records, RecordMeta{Offset: offset, Length: length, Type: probe.Type, ID: probe.ID, ParentID: pid})
		}
		offset += length
		if err == io.EOF {
			break
		}
	}
	return idx, nil
}

// ActivePathIDs returns active-path membership without loading payloads.
func (idx *RecordIndex) ActivePathIDs(leafID string) map[string]bool {
	byID := map[string]RecordMeta{}
	for _, m := range idx.Records {
		if m.ID != "" {
			byID[m.ID] = m
		}
	}
	out := map[string]bool{}
	cur := leafID
	if cur == "" {
		// Last record.
		for i := len(idx.Records) - 1; i >= 0; i-- {
			if idx.Records[i].ID != "" {
				cur = idx.Records[i].ID
				break
			}
		}
	}
	for cur != "" {
		if out[cur] {
			break // cycle guard
		}
		out[cur] = true
		m, ok := byID[cur]
		if !ok || m.ParentID == "" {
			break
		}
		cur = m.ParentID
	}
	return out
}

// LoadEntry decodes one record body on demand.
func (idx *RecordIndex) LoadEntry(meta RecordMeta) (*Entry, error) {
	if meta.Offset < 0 || meta.Length <= 0 || meta.Length > MaxRecordBytes+2 {
		return nil, fmt.Errorf("invalid indexed record bounds")
	}
	f, err := os.Open(idx.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(meta.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, meta.Length)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	// Trim trailing newline.
	if len(buf) > 0 && buf[len(buf)-1] == '\n' {
		buf = buf[:len(buf)-1]
		if len(buf) > 0 && buf[len(buf)-1] == '\r' {
			buf = buf[:len(buf)-1]
		}
	}
	fe, err := UnmarshalFileEntry(buf)
	if err != nil {
		return nil, err
	}
	if fe.IsHeader {
		return nil, fmt.Errorf("record is session header, not entry")
	}
	return fe.Entry, nil
}
