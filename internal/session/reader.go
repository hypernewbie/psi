package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type ParseError struct {
	Path string
	Line int
	Err  error
}

func (e *ParseError) Error() string { return fmt.Sprintf("%s:%d: %v", e.Path, e.Line, e.Err) }
func (e *ParseError) Unwrap() error { return e.Err }

func ReadFileEntries(path string) (*SessionHeader, []*Entry, [][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 256*1024*1024 {
		return nil, nil, nil, fmt.Errorf("session must be a regular file no larger than 256 MiB: %s", path)
	}
	return DecodeEntries(f, path)
}

// readRecord enforces the bound while reading, not after an unbounded allocation.
func readRecord(br *bufio.Reader) ([]byte, error) {
	var record []byte
	for {
		part, err := br.ReadSlice('\n')
		if len(record)+len(part) > MaxRecordBytes+1 {
			return nil, fmt.Errorf("record exceeds %d bytes", MaxRecordBytes)
		}
		record = append(record, part...)
		if err != bufio.ErrBufferFull {
			return record, err
		}
	}
}

func DecodeEntries(r io.Reader, path string) (*SessionHeader, []*Entry, [][]byte, error) {
	br := bufio.NewReader(r)
	var header *SessionHeader
	var entries []*Entry
	var raws [][]byte
	for lineNo := 1; ; lineNo++ {
		line, err := readRecord(br)
		if err != nil && err != io.EOF {
			return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: err}
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) > MaxRecordBytes {
			return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("record exceeds %d bytes", MaxRecordBytes)}
		}
		if len(bytes.TrimSpace(line)) > 0 {
			fe, parseErr := UnmarshalFileEntry(line)
			if parseErr != nil {
				return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: parseErr}
			}
			if fe.IsHeader {
				if header != nil {
					return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("duplicate session header")}
				}
				header = fe.Header
			} else {
				if header == nil {
					return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("first record is not a session header")}
				}
				entries = append(entries, fe.Entry)
			}
			raws = append(raws, append([]byte(nil), line...))
		}
		if err == io.EOF {
			return header, entries, raws, nil
		}
	}
}

// HasIncompleteTail accepts a complete final JSON record without a newline,
// including records larger than 64 KiB. It never accepts trailing JSON garbage.
func HasIncompleteTail(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return false, err
	}
	size := st.Size()
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return false, err
	}
	if last[0] == '\n' {
		return false, nil
	}
	off := max(int64(0), size-int64(MaxRecordBytes)-1)
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return false, err
	}
	if buf[len(buf)-1] == '\n' {
		return false, nil
	}
	lastNL := bytes.LastIndexByte(buf, '\n')
	if lastNL >= 0 {
		buf = buf[lastNL+1:]
	} else if off > 0 || len(buf) > MaxRecordBytes {
		return false, fmt.Errorf("final record exceeds %d bytes", MaxRecordBytes)
	}
	buf = bytes.TrimSpace(buf)
	if len(buf) == 0 {
		return false, nil
	}
	return !json.Valid(buf), nil
}
