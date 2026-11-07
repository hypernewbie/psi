package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// contentGuardLimit bounds the session files a byte-level guard tracks. A
// rewrite invisible to the stat triple (size, identity, mtime) must complete
// between the store's last write and the next append inside one
// inode-timestamp tick; a file this large cannot be read and rewritten that
// fast, so the stat triple is not blind for bigger files.
const contentGuardLimit = 16 << 20

// probeClockGranularity reports whether this filesystem's inode timestamps
// cannot distinguish two consecutive writes. On such systems a same-size
// external rewrite can keep the file's recorded mtime, blinding the stat
// triple; the byte-level guard closes that window. The probe writes a
// scratch file a few times and compares consecutive mtimes: any pair that
// did not advance shares one tick. Every probe failure fails safe (coarse).
func probeClockGranularity(dir string) bool {
	path := filepath.Join(dir, "psi-clock-probe")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// A probe left behind by an interrupted run: reuse and retire it.
			f, err = os.OpenFile(path, os.O_WRONLY, 0)
		}
		if err != nil {
			return true
		}
	}
	defer os.Remove(path)
	previous := time.Time{}
	for i := 0; i < 3; i++ {
		if _, err := f.Write([]byte{'x'}); err != nil {
			return true
		}
		st, err := f.Stat()
		if err != nil {
			return true
		}
		if !previous.IsZero() && !st.ModTime().After(previous) {
			return true
		}
		previous = st.ModTime()
	}
	return false
}

// guardBegin tracks the exact session-file bytes for new sessions once the
// first flush writes them, when the filesystem clock is coarse enough to
// hide a same-size rewrite.
func (s *Store) guardBegin(fileBytes []byte) {
	if !s.clockCoarse || len(fileBytes) == 0 || int64(len(fileBytes)) > s.contentGuardLimit {
		return
	}
	s.contentGuard = true
	s.contentBaseline = fileBytes
}

// guardExtend extends the tracked bytes with a record the store just wrote
// and stops tracking once the file outgrows the size a within-tick rewrite
// can span.
func (s *Store) guardExtend(record []byte) {
	if !s.contentGuard || s.contentBaseline == nil {
		return
	}
	s.contentBaseline = append(s.contentBaseline, record...)
	if int64(len(s.contentBaseline)) > s.contentGuardLimit {
		s.contentBaseline = nil
		s.contentGuard = false
	}
}

// guardVerify confirms that the file behind the open handle still equals the
// bytes this store last wrote. It reads in bounded windows and stops at the
// first difference, so an intact file costs reads only.
func (s *Store) guardVerify(f durableFile) error {
	size := int64(len(s.contentBaseline))
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("cannot inspect active session: %w", err)
	}
	if st.Size() != size {
		return fmt.Errorf("session file changed externally, refusing to append: %s", s.file)
	}
	buf := make([]byte, 64*1024)
	for off := int64(0); off < size; off += int64(len(buf)) {
		n := int64(len(buf))
		if size-off < n {
			n = size - off
		}
		got, err := f.ReadAt(buf[:n], off)
		if err != nil || int64(got) != n {
			return fmt.Errorf("session file changed externally, refusing to append: %s", s.file)
		}
		if !bytes.Equal(buf[:n], s.contentBaseline[off:off+n]) {
			return fmt.Errorf("session file changed externally, refusing to append: %s", s.file)
		}
	}
	return nil
}
