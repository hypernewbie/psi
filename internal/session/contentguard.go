package session

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
)

// guardVerify compares the open file to the bytes loaded and subsequently
// appended by this store. Metadata alone cannot detect an in-place rewrite
// with restored or coarse-grained timestamps.
func (s *Store) guardVerify(f durableFile) error {
	if s.contentHash == nil {
		return fmt.Errorf("session content baseline unavailable, refusing to append: %s", s.file)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, s.knownSize)); err != nil {
		return fmt.Errorf("cannot verify session file %s: %w", s.file, err)
	}
	if !bytes.Equal(h.Sum(nil), s.contentHash.Sum(nil)) {
		return fmt.Errorf("session file changed externally, refusing to append: %s", s.file)
	}
	return nil
}
