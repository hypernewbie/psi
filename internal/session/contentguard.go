package session

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
)

// CheckWritable verifies that the durable session file is still safe to mutate
// without appending anything. It reuses the append guards so a manual bump,
// which has no user-message append, cannot write through an externally changed
// file. A failure is sticky, like an append failure. Lazy and non-persistent
// stores perform no file I/O and never create a file.
func (s *Store) CheckWritable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return fmt.Errorf("storage blocked: %w", s.writeErr)
	}
	if !s.persist || s.file == "" || !s.flushed {
		return nil
	}
	if err := s.checkWritableLocked(); err != nil {
		s.writeErr = err
		return err
	}
	return nil
}

func (s *Store) checkWritableLocked() (err error) {
	st, err := s.statDurable()
	if err != nil {
		return fmt.Errorf("cannot inspect active session: %w", err)
	}
	if st.Size() != s.knownSize || (s.knownInfo != nil && (!os.SameFile(st, s.knownInfo) || !st.ModTime().Equal(s.knownInfo.ModTime()))) {
		return fmt.Errorf("session file changed externally, refusing to append: %s", s.file)
	}
	if incomplete, err := HasIncompleteTail(s.file); err != nil {
		return err
	} else if incomplete {
		return fmt.Errorf("session file has truncated final record, refusing to append: %s", s.file)
	}
	f, err := s.openDurable(s.file, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("cannot check active session: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if err := checkSessionLinks(s.file); err != nil {
		return err
	}
	if opened.Size() != s.knownSize || s.knownInfo != nil && (!os.SameFile(opened, s.knownInfo) || !opened.ModTime().Equal(s.knownInfo.ModTime())) {
		return fmt.Errorf("session file changed before storage check: %s", s.file)
	}
	return s.guardVerify(f)
}

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
