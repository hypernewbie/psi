//go:build unix

package app

import (
	"io"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// unixRPCReader makes inherited pipes, sockets, and terminals pollable without
// changing the original descriptor's close ownership.
type unixRPCReader struct {
	fd        int
	original  int
	flags     int
	regular   bool
	file      *os.File
	interrupt atomic.Bool
}

func newUnixRPCReader(file *os.File) (*unixRPCReader, error) {
	fd := int(file.Fd())
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFREG {
		return &unixRPCReader{regular: true, file: file}, nil
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, err
	}
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		_ = unix.Close(dup)
		return nil, err
	}
	return &unixRPCReader{fd: dup, original: fd, flags: flags}, nil
}

func (r *unixRPCReader) Read(p []byte) (int, error) {
	if r.regular {
		return r.file.Read(p)
	}
	for {
		if r.interrupt.Load() {
			return 0, io.EOF
		}
		n, err := unix.Read(r.fd, p)
		if n > 0 {
			return n, nil
		}
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			_, pollErr := unix.Poll([]unix.PollFd{{Fd: int32(r.fd), Events: unix.POLLIN}}, 100)
			if pollErr == unix.EINTR {
				continue
			}
			if pollErr != nil {
				return 0, pollErr
			}
			continue
		}
		if err == nil {
			return 0, io.EOF
		}
		return 0, err
	}
}

func (r *unixRPCReader) Interrupt() {
	r.interrupt.Store(true)
}

func (r *unixRPCReader) Close() error {
	if r.regular {
		return nil
	}
	var firstErr error
	if r.original >= 0 {
		if _, err := unix.FcntlInt(uintptr(r.original), unix.F_SETFL, r.flags); err != nil {
			firstErr = err
		}
		if r.flags&unix.O_NONBLOCK == 0 {
			if err := unix.SetNonblock(r.original, false); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := unix.Close(r.fd); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// unixRPCWriter uses nonblocking writes and a per-frame deadline so a stalled
// reader cannot pin the RPC server inside one stdout write.
type unixRPCWriter struct {
	fd        int
	original  int
	flags     int
	regular   bool
	file      *os.File
	deadline  atomic.Int64
	interrupt atomic.Bool
}

func newUnixRPCWriter(file *os.File) (*unixRPCWriter, error) {
	fd := int(file.Fd())
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFREG {
		return &unixRPCWriter{regular: true, file: file}, nil
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, err
	}
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		_ = unix.Close(dup)
		return nil, err
	}
	return &unixRPCWriter{fd: dup, original: fd, flags: flags}, nil
}

func (w *unixRPCWriter) Write(p []byte) (int, error) {
	if w.regular {
		return w.file.Write(p)
	}
	total := 0
	for len(p) > 0 {
		if w.interrupt.Load() {
			return total, os.ErrClosed
		}
		n, err := unix.Write(w.fd, p)
		if n > 0 {
			total += n
			p = p[n:]
			continue
		}
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			timeout := 100 * time.Millisecond
			if deadline := w.writeDeadline(); !deadline.IsZero() {
				remaining := time.Until(deadline)
				if remaining <= 0 {
					return total, os.ErrDeadlineExceeded
				}
				if remaining < timeout {
					timeout = remaining
				}
			}
			_, pollErr := unix.Poll([]unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLOUT}}, int(timeout/time.Millisecond)+1)
			if pollErr == unix.EINTR {
				continue
			}
			if pollErr != nil {
				return total, pollErr
			}
			continue
		}
		if err == nil {
			return total, io.ErrShortWrite
		}
		return total, err
	}
	return total, nil
}

func (w *unixRPCWriter) SetWriteDeadline(deadline time.Time) error {
	if w.regular {
		// Regular files keep synchronous I/O and have no watchdog guarantee.
		return nil
	}
	if deadline.IsZero() {
		w.deadline.Store(0)
	} else {
		w.deadline.Store(deadline.UnixNano())
	}
	return nil
}

func (w *unixRPCWriter) writeDeadline() time.Time {
	nanos := w.deadline.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

func (w *unixRPCWriter) Interrupt() {
	w.interrupt.Store(true)
}

func (w *unixRPCWriter) Close() error {
	if w.regular {
		return nil
	}
	var firstErr error
	if w.original >= 0 {
		if _, err := unix.FcntlInt(uintptr(w.original), unix.F_SETFL, w.flags); err != nil {
			firstErr = err
		}
		if w.flags&unix.O_NONBLOCK == 0 {
			if err := unix.SetNonblock(w.original, false); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := unix.Close(w.fd); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func openRPCTransport() (*rpcTransport, error) {
	in, err := newUnixRPCReader(os.Stdin)
	if err != nil {
		return nil, err
	}
	out, err := newUnixRPCWriter(os.Stdout)
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	return &rpcTransport{
		in: in, out: out,
		interruptIn: in.Interrupt, interruptOut: out.Interrupt,
		close: func() error {
			inErr := in.Close()
			outErr := out.Close()
			if inErr != nil {
				return inErr
			}
			return outErr
		},
	}, nil
}
