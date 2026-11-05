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
	if !st.Mode().IsRegular() {
		return nil, nil, nil, fmt.Errorf("session must be a regular file: %s", path)
	}
	return DecodeEntries(f, path)
}

// readRecord reads one JSONL record without imposing an application-level
// record-size limit. The caller already owns the file and the record must be
// materialized for JSON decoding, so its size is naturally bounded by the
// available address space.
func readRecord(br *bufio.Reader) ([]byte, error) {
	return br.ReadBytes('\n')
}

// loadFileEntries loads the whole session file into memory, the way Pi's
// loadEntriesFromFile does: one pass over the file, every record parsed once,
// and the parsed entries kept for the rest of the session's life.
func loadFileEntries(path string) (*SessionHeader, []*Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("session must be a regular file: %s", path)
	}
	return loadFileEntriesFrom(f, path)
}

// loadFileEntriesFrom parses every record of a session file. A torn record is
// an invisible gap: the surrounding session stays readable and the writer
// stays disabled, matching Pi's skip of malformed lines.
func loadFileEntriesFrom(r io.Reader, path string) (*SessionHeader, []*Entry, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var header *SessionHeader
	var entries []*Entry
	for lineNo := 1; ; lineNo++ {
		line, err := readRecord(br)
		if err != nil && err != io.EOF {
			return nil, nil, &ParseError{Path: path, Line: lineNo, Err: err}
		}
		data := trimRecord(line)
		if len(bytes.TrimSpace(data)) > 0 {
			fe, parseErr := UnmarshalFileEntry(data)
			if parseErr != nil {
				if !json.Valid(data) {
					// A damaged physical record is an invisible gap.
					if header == nil {
						return nil, nil, &ParseError{Path: path, Line: lineNo, Err: parseErr}
					}
					if err == io.EOF {
						break
					}
					continue
				}
				return nil, nil, &ParseError{Path: path, Line: lineNo, Err: parseErr}
			}
			if fe.IsHeader {
				if header != nil {
					return nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("duplicate session header")}
				}
				header = fe.Header
			} else {
				if header == nil {
					return nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("first record is not a session header")}
				}
				fillEntryMetadata(fe.Entry)
				entries = append(entries, fe.Entry)
			}
		}
		if err == io.EOF {
			break
		}
	}
	return header, entries, nil
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

// HasIncompleteTail accepts a complete final JSON record without a newline.
// It locates the final physical record from the end of the file instead of
// using a fixed-size tail window.
func HasIncompleteTail(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	if st.Size() == 0 {
		return false, nil
	}
	size := st.Size()
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return false, err
	}
	if last[0] == '\n' {
		return false, nil
	}

	start, err := lastRecordOffset(f, size)
	if err != nil {
		return false, err
	}
	length := size - start
	if length < 0 || uint64(length) > uint64(maxInt()) {
		return false, fmt.Errorf("final record is too large for this platform")
	}
	buf := make([]byte, int(length))
	if _, err := io.ReadFull(io.NewSectionReader(f, start, length), buf); err != nil {
		return false, err
	}
	buf = bytes.TrimSpace(buf)
	if len(buf) == 0 {
		return false, nil
	}
	return !json.Valid(buf), nil
}

func lastRecordOffset(f *os.File, size int64) (int64, error) {
	// This is an I/O buffer size, not a record limit. The scan continues until
	// it finds the previous newline, regardless of record size.
	const scanBufferSize = 64 * 1024
	buf := make([]byte, scanBufferSize)
	for end := size; end > 0; {
		start := end - int64(len(buf))
		if start < 0 {
			start = 0
		}
		n := int(end - start)
		read, err := f.ReadAt(buf[:n], start)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if read > 0 {
			if i := bytes.LastIndexByte(buf[:read], '\n'); i >= 0 {
				return start + int64(i+1), nil
			}
		}
		end = start
	}
	return 0, nil
}

func maxInt() int { return int(^uint(0) >> 1) }
