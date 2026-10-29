package session

import (
	"io"
	"os"
)

// Per-store I/O seams keep failure tests deterministic without global hooks.
// A failed write, sync, close, or post-write stat leaves the disk uncertain.
// Never truncate or repair it as part of an unsuccessful append.
type durableFile interface {
	io.Writer
	io.ReaderAt
	Sync() error
	Stat() (os.FileInfo, error)
	Close() error
}
type storageOps struct {
	open func(string, int, os.FileMode) (durableFile, error)
	stat func(string) (os.FileInfo, error)
}

func (s *Store) openDurable(path string, flags int, mode os.FileMode) (durableFile, error) {
	if s.storage != nil && s.storage.open != nil {
		return s.storage.open(path, flags, mode)
	}
	return os.OpenFile(path, flags, mode)
}
func (s *Store) statDurable() (os.FileInfo, error) {
	if s.storage != nil && s.storage.stat != nil {
		return s.storage.stat(s.file)
	}
	return os.Stat(s.file)
}
func writeDurable(f durableFile, data []byte) error {
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// FailWritesForTest forces subsequent durable opens to fail (deterministic
// storage-refusal schedules). Production code never calls it.
func (s *Store) FailWritesForTest(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storage == nil {
		s.storage = &storageOps{}
	}
	s.storage.open = func(string, int, os.FileMode) (durableFile, error) { return nil, err }
}
