package app

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

var errStartupCancelled = errors.New("session selection cancelled")

// Read only this line. A new buffered reader can steal the first editor input
// from stdin when a consent answer and prompt arrive in one terminal write.
func readStartupLine(in io.Reader) (string, error) {
	var line strings.Builder
	var one [1]byte
	for line.Len() <= 64*1024 {
		_, err := io.ReadFull(in, one[:])
		if err != nil {
			if errors.Is(err, io.EOF) && line.Len() > 0 {
				return line.String(), nil
			}
			return "", err
		}
		if one[0] == '\n' {
			return strings.TrimSuffix(line.String(), "\r"), nil
		}
		line.WriteByte(one[0])
	}
	return "", fmt.Errorf("startup input exceeds 64 KiB")
}
