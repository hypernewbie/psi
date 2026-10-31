package session

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
)

const (
	headerFastBufSize = 4096
	maxHeaderFastScan = 1024 * 1024
)

func readHeaderFast(path string) (*SessionHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, headerFastBufSize)
	var scanned int64
	var pending []byte
	for scanned < maxHeaderFastScan {
		rem := int(maxHeaderFastScan - scanned)
		chunk := make([]byte, headerFastBufSize)
		if rem < len(chunk) {
			chunk = chunk[:rem]
		}
		n, err := br.Read(chunk)
		if n > 0 {
			scanned += int64(n)
			pending = append(pending, chunk[:n]...)
			for {
				idx := bytes.IndexByte(pending, '\n')
				if idx == -1 {
					break
				}
				line := pending[:idx]
				pending = pending[idx+1:]
				line = bytes.TrimSuffix(line, []byte("\r"))
				trim := bytes.TrimSpace(line)
				if len(trim) == 0 {
					continue
				}
				_, header, perr := metadataRecord(trim)
				if perr != nil {
					continue
				}
				if header != nil {
					if header.ID == "" {
						return nil, fmt.Errorf("session header missing id")
					}
					if verr := checkVersion(header, path); verr != nil {
						return nil, verr
					}
					return header, nil
				}
				return nil, fmt.Errorf("missing session header")
			}
		}
		if err == io.EOF {
			trim := bytes.TrimSpace(pending)
			if len(trim) == 0 {
				return nil, fmt.Errorf("missing session header")
			}
			line := bytes.TrimSuffix(pending, []byte("\r"))
			trimLine := bytes.TrimSpace(line)
			_, header, perr := metadataRecord(trimLine)
			if perr != nil {
				return nil, perr
			}
			if header != nil {
				if header.ID == "" {
					return nil, fmt.Errorf("session header missing id")
				}
				if verr := checkVersion(header, path); verr != nil {
					return nil, verr
				}
				return header, nil
			}
			return nil, fmt.Errorf("missing session header")
		}
		if err != nil {
			return nil, err
		}
	}
	// probe for extra byte
	one := make([]byte, 1)
	if _, err := br.Read(one); err == nil {
		return nil, fmt.Errorf("session header exceeds %d-byte scan limit: %s", maxHeaderFastScan, path)
	}
	if len(bytes.TrimSpace(pending)) > 0 {
		line := bytes.TrimSuffix(pending, []byte("\r"))
		trimLine := bytes.TrimSpace(line)
		_, header, perr := metadataRecord(trimLine)
		if perr != nil {
			return nil, perr
		}
		if header != nil {
			if header.ID == "" {
				return nil, fmt.Errorf("session header missing id")
			}
			if verr := checkVersion(header, path); verr != nil {
				return nil, verr
			}
			return header, nil
		}
	}
	return nil, fmt.Errorf("missing session header")
}
