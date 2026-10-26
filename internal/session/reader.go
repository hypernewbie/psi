package session

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
)

// ParseError reports a JSONL parse failure with file and line context.
type ParseError struct {
	Path string
	Line int
	Err  error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s:%d: %v", e.Path, e.Line, e.Err)
}
func (e *ParseError) Unwrap() error { return e.Err }

// ReadFileEntries reads one logical JSON record per line.
// Blank lines are skipped (not errors). Records larger than MaxRecordBytes
// are refused. Malformed non-blank lines return *ParseError with file+line.
// A final line without trailing newline is accepted if complete JSON.
func ReadFileEntries(path string) (header *SessionHeader, entries []*Entry, rawLines [][]byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	defer f.Close()
	return DecodeEntries(f, path)
}

// DecodeEntries streams JSONL from r.
func DecodeEntries(r io.Reader, path string) (*SessionHeader, []*Entry, [][]byte, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	var raws [][]byte
	var header *SessionHeader
	var entries []*Entry
	lineNo := 0
	for {
		chunk, err := br.ReadBytes('\n')
		hasNL := len(chunk) > 0 && chunk[len(chunk)-1] == '\n'
		if len(chunk) == 0 {
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, nil, nil, err
			}
			continue
		}
		line := chunk
		if hasNL {
			line = chunk[:len(chunk)-1]
			line = bytes.TrimSuffix(line, []byte("\r"))
		}
		if err == io.EOF {
			if len(bytes.TrimSpace(line)) == 0 {
				break
			}
			lineNo++
			if len(line) > MaxRecordBytes {
				return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("record exceeds %d bytes", MaxRecordBytes)}
			}
			fe, perr := UnmarshalFileEntry(line)
			if perr != nil {
				return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: perr}
			}
			cp := append([]byte(nil), line...)
			raws = append(raws, cp)
			if fe.IsHeader {
				if header != nil {
					return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("duplicate session header")}
				}
				header = fe.Header
				break
			}
			if header == nil {
				return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("first record is not a session header")}
			}
			entries = append(entries, fe.Entry)
			break
		}
		if err != nil {
			return nil, nil, nil, err
		}
		lineNo++
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(line) > MaxRecordBytes {
			return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("record exceeds %d bytes", MaxRecordBytes)}
		}
		fe, perr := UnmarshalFileEntry(line)
		if perr != nil {
			return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: perr}
		}
		cp := append([]byte(nil), line...)
		raws = append(raws, cp)
		if fe.IsHeader {
			if header != nil {
				return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("duplicate session header")}
			}
			header = fe.Header
			continue
		}
		if header == nil {
			return nil, nil, nil, &ParseError{Path: path, Line: lineNo, Err: fmt.Errorf("first record is not a session header")}
		}
		entries = append(entries, fe.Entry)
	}
	return header, entries, raws, nil
}

// HasIncompleteTail reports whether the file ends with a non-empty fragment
// that is not complete JSON (truncated write). Reads only the tail.
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
	const probe = 65536
	off := st.Size() - probe
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return false, err
	}
	if buf[len(buf)-1] == '\n' {
		return false, nil
	}
	lastNL := bytes.LastIndexByte(buf, '\n')
	fragment := buf
	if lastNL >= 0 {
		fragment = buf[lastNL+1:]
	}
	if len(bytes.TrimSpace(fragment)) == 0 {
		return false, nil
	}
	var v any
	dec := jsonDecoder(fragment)
	if err := dec.Decode(&v); err != nil {
		return true, nil
	}
	return false, nil
}
