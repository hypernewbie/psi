package session

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"time"
)

func inspectSessionMetadata(path string) (*SessionInfo, error) {
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
		return nil, fmt.Errorf("session must be a regular file")
	}
	info := &SessionInfo{Path: path, Modified: st.ModTime(), FirstMessage: "(no messages)"}
	unavailable := func(err error) (*SessionInfo, error) {
		if info.ID == "" {
			return nil, err
		}
		info.Unavailable = err.Error()
		return info, nil
	}
	br := bufio.NewReaderSize(f, 16*1024)
	for {
		line, readErr := readRecord(br)
		if readErr != nil && readErr != io.EOF {
			return unavailable(readErr)
		}
		if len(bytes.TrimSpace(line)) > 0 {
			entry, header, err := metadataRecord(trimRecord(line))
			if err != nil {
				return unavailable(err)
			}
			if info.ID == "" {
				if header == nil {
					return nil, fmt.Errorf("missing session header")
				}
				info.ID, info.Cwd = header.ID, header.Cwd
				info.Created, _ = time.Parse(time.RFC3339Nano, header.Timestamp)
				if err := checkVersion(header, path); err != nil {
					return unavailable(err)
				}
			} else if header != nil {
				return unavailable(fmt.Errorf("duplicate session header"))
			} else if entry.SessionInfo != nil {
				info.Name = ""
				if entry.SessionInfo.Name != nil {
					info.Name = *entry.SessionInfo.Name
				}
			} else if entry.Message != nil {
				info.MessageCount++
				if info.FirstMessage == "(no messages)" && entry.Message.Message.User != nil {
					full, err := UnmarshalEntry(trimRecord(line))
					if err != nil {
						return unavailable(err)
					}
					info.FirstMessage = previewText(userText(full.Message.Message.User), 200)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if info.ID == "" {
		return nil, fmt.Errorf("missing session header")
	}
	// Inspect an existing advisory lock without creating files or changing it.
	if held, err := probeSharedLock(path); err == nil {
		if held {
			info.Unavailable = "locked by another psi process"
		}
	} else if !os.IsNotExist(err) {
		info.Unavailable = "cannot inspect ownership lock"
	}
	return info, nil
}
func previewText(text string, limit int) string {
	count := 0
	for i := range text {
		if count == limit {
			return text[:i]
		}
		count++
	}
	return text
}
