package session

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/fnv"
	"io"
	"os"
)

// RecordIndex is reconstructible metadata, not another history database.
type RecordIndex struct {
	Records []RecordMeta
	Path    string
	Header  *SessionHeader
}
type RecordMeta struct {
	Offset, Length     int64
	Type, ID, ParentID string
	Hash               uint64 // FNV-1a over the trimmed record bytes
	entry              *Entry
}

// recordHash identifies exact record bytes. Equal-size rewrites that keep
// offsets and lengths valid still change the hash, so a lazy read can never
// mix generations silently.
func recordHash(data []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return h.Sum64()
}

func BuildIndex(path string) (*RecordIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("session must be a regular file: %s", path)
	}
	return BuildIndexFrom(f, path)
}
func BuildIndexFrom(r io.Reader, path string) (*RecordIndex, error) {
	idx := &RecordIndex{Path: path}
	br := bufio.NewReaderSize(r, 64*1024)
	var offset int64
	for lineNo := 1; ; lineNo++ {
		line, err := readRecord(br)
		if err != nil && err != io.EOF {
			return nil, &ParseError{Path: path, Line: lineNo, Err: err}
		}
		length := int64(len(line))
		data := trimRecord(line)
		if len(data) > MaxRecordBytes {
			return nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("record exceeds %d bytes", MaxRecordBytes)}
		}
		if len(bytes.TrimSpace(data)) > 0 {
			e, header, parseErr := metadataRecord(data)
			if parseErr != nil {
				return nil, &ParseError{Path: path, Line: lineNo, Err: parseErr}
			}
			meta := RecordMeta{Offset: offset, Length: length, Hash: recordHash(data)}
			if header != nil {
				meta.Type = "session"
				if idx.Header != nil || len(idx.Records) > 0 {
					return nil, fmt.Errorf("%s:%d: misplaced or duplicate session header", path, lineNo)
				}
				idx.Header = header
				if idx.Header.ID == "" {
					return nil, fmt.Errorf("session header missing id")
				}
				if e := checkVersion(idx.Header, path); e != nil {
					return nil, e
				}
				meta.ID = idx.Header.ID
			} else {
				meta.entry, meta.ID, meta.Type = e, e.ID(), e.Kind
				if e.ParentID() != nil {
					meta.ParentID = *e.ParentID()
				}
			}
			idx.Records = append(idx.Records, meta)
		}
		offset += length
		if err == io.EOF {
			break
		}
	}
	return idx, nil
}
func (idx *RecordIndex) ActivePathIDs(leaf string) map[string]bool {
	byID := map[string]RecordMeta{}
	for _, m := range idx.Records {
		if m.Type != "session" {
			byID[m.ID] = m
		}
	}
	if leaf == "" {
		for i := len(idx.Records) - 1; i >= 0; i-- {
			if idx.Records[i].Type != "session" {
				leaf = idx.Records[i].ID
				break
			}
		}
	}
	out := map[string]bool{}
	for leaf != "" && !out[leaf] {
		m, ok := byID[leaf]
		if !ok {
			break
		}
		out[leaf] = true
		leaf = m.ParentID
	}
	return out
}
func loadRecord(f *os.File, meta RecordMeta) (*Entry, error) {
	if meta.Offset < 0 || meta.Length <= 0 || meta.Length > MaxRecordBytes+2 {
		return nil, fmt.Errorf("invalid indexed record bounds")
	}
	buf := make([]byte, meta.Length)
	if _, err := f.ReadAt(buf, meta.Offset); err != nil {
		return nil, err
	}
	data := trimRecord(buf)
	if meta.Hash != 0 && recordHash(data) != meta.Hash {
		return nil, fmt.Errorf("session file changed externally: record bytes differ from index")
	}
	loaded, err := UnmarshalEntry(data)
	if err != nil {
		return nil, err
	}
	if meta.ID != "" && loaded.ID() != meta.ID {
		return nil, fmt.Errorf("session file changed externally: record identity changed")
	}
	if meta.Type != "" && loaded.Kind != meta.Type {
		return nil, fmt.Errorf("session file changed externally: record type changed")
	}
	lp, mp := loaded.ParentID(), meta.ParentID
	if (lp == nil) != (mp == "") || (lp != nil && *lp != mp) {
		return nil, fmt.Errorf("session file changed externally: record parent changed")
	}
	return loaded, nil
}
func (idx *RecordIndex) LoadEntry(meta RecordMeta) (*Entry, error) {
	if meta.Offset < 0 || meta.Length <= 0 || meta.Length > MaxRecordBytes+2 {
		return nil, fmt.Errorf("invalid indexed record bounds")
	}
	f, err := os.Open(idx.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return loadRecord(f, meta)
}
