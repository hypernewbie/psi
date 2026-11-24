//go:build windows

package app

import (
	"errors"
	"io"
	"os"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

var (
	procCancelSynchronousIo = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")
	procCancelIoEx          = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelIoEx")
)

// cancelBlockedIO interrupts an inherited pipe or console operation. The
// synchronous-thread cancellation covers WriteFile/ReadFile on inherited
// handles; CancelIoEx covers handles the runtime put on the poller.
func cancelBlockedIO(file *os.File, thread windows.Handle) {
	if file != nil {
		_, _, _ = procCancelIoEx.Call(file.Fd(), 0)
	}
	if thread != 0 {
		_, _, _ = procCancelSynchronousIo.Call(uintptr(thread))
	}
}

type windowsRPCReadRequest struct {
	p    []byte
	n    int
	err  error
	done chan struct{}
}

type windowsRPCWriterRequest struct {
	p        []byte
	n        int
	err      error
	deadline time.Time
	done     chan struct{}
}

type windowsRPCReader struct {
	file      *os.File
	regular   bool
	thread    atomic.Uintptr
	requests  chan *windowsRPCReadRequest
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
}

func newWindowsRPCReader(file *os.File) (*windowsRPCReader, error) {
	regular, err := windowsFileIsRegular(file)
	if err != nil {
		return nil, err
	}
	if regular {
		return &windowsRPCReader{file: file, regular: true}, nil
	}
	dup, err := duplicateWindowsHandle(file)
	if err != nil {
		return nil, err
	}
	r := &windowsRPCReader{
		file:     os.NewFile(uintptr(dup), "psi-rpc-stdin"),
		requests: make(chan *windowsRPCReadRequest), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go r.loop()
	return r, nil
}

func (r *windowsRPCReader) loop() {
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	thread, err := windows.OpenThread(windows.THREAD_TERMINATE, false, windows.GetCurrentThreadId())
	if err == nil {
		r.thread.Store(uintptr(thread))
		defer windows.CloseHandle(thread)
	}
	defer close(r.done)
	for {
		select {
		case <-r.stop:
			return
		case request := <-r.requests:
			n, err := r.file.Read(request.p)
			if err != nil && r.closed.Load() {
				err = io.EOF
			}
			request.n, request.err = n, err
			close(request.done)
		}
	}
}

func (r *windowsRPCReader) Read(p []byte) (int, error) {
	if r.regular {
		return r.file.Read(p)
	}
	if r.closed.Load() {
		return 0, io.EOF
	}
	request := &windowsRPCReadRequest{p: p, done: make(chan struct{})}
	select {
	case r.requests <- request:
	case <-r.stop:
		return 0, io.EOF
	}
	<-request.done
	return request.n, request.err
}

func (r *windowsRPCReader) Interrupt() {
	if r.regular {
		return
	}
	cancelBlockedIO(r.file, windows.Handle(r.thread.Load()))
}

func (r *windowsRPCReader) Close() error {
	if r.regular {
		return nil
	}
	var err error
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		close(r.stop)
		r.Interrupt()
		err = r.file.Close()
		<-r.done
	})
	return err
}

type windowsRPCWriter struct {
	file        *os.File
	regular     bool
	thread      atomic.Uintptr
	requests    chan *windowsRPCWriterRequest
	stop        chan struct{}
	done        chan struct{}
	deadline    atomic.Int64
	closeOnce   sync.Once
	closed      atomic.Bool
	interrupted atomic.Bool
	deadlineHit atomic.Bool
}

func newWindowsRPCWriter(file *os.File) (*windowsRPCWriter, error) {
	regular, err := windowsFileIsRegular(file)
	if err != nil {
		return nil, err
	}
	if regular {
		return &windowsRPCWriter{file: file, regular: true}, nil
	}
	dup, err := duplicateWindowsHandle(file)
	if err != nil {
		return nil, err
	}
	w := &windowsRPCWriter{
		file:     os.NewFile(uintptr(dup), "psi-rpc-stdout"),
		requests: make(chan *windowsRPCWriterRequest), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go w.loop()
	return w, nil
}

func (w *windowsRPCWriter) loop() {
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	thread, err := windows.OpenThread(windows.THREAD_TERMINATE, false, windows.GetCurrentThreadId())
	if err == nil {
		w.thread.Store(uintptr(thread))
		defer windows.CloseHandle(thread)
	}
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			return
		case request := <-w.requests:
			if w.interrupted.Load() {
				request.err = os.ErrClosed
				close(request.done)
				continue
			}
			var timer *time.Timer
			if wait := time.Until(request.deadline); !request.deadline.IsZero() && wait <= 0 {
				request.err = os.ErrDeadlineExceeded
				close(request.done)
				continue
			} else if !request.deadline.IsZero() {
				timer = time.AfterFunc(wait, func() {
					w.deadlineHit.Store(true)
					w.Interrupt()
				})
			}
			n, err := w.file.Write(request.p)
			if timer != nil {
				timer.Stop()
			}
			if err != nil {
				switch {
				case w.deadlineHit.Load():
					err = os.ErrDeadlineExceeded
				case w.closed.Load() || w.interrupted.Load():
					err = os.ErrClosed
				}
			}
			request.n, request.err = n, err
			close(request.done)
		}
	}
}

func (w *windowsRPCWriter) Write(p []byte) (int, error) {
	if w.regular {
		return w.file.Write(p)
	}
	if w.closed.Load() {
		return 0, os.ErrClosed
	}
	request := &windowsRPCWriterRequest{p: p, deadline: w.writeDeadline(), done: make(chan struct{})}
	select {
	case w.requests <- request:
	case <-w.stop:
		return 0, os.ErrClosed
	}
	<-request.done
	return request.n, request.err
}

func (w *windowsRPCWriter) SetWriteDeadline(deadline time.Time) error {
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

func (w *windowsRPCWriter) writeDeadline() time.Time {
	nanos := w.deadline.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

func (w *windowsRPCWriter) Interrupt() {
	if w.regular {
		return
	}
	w.interrupted.Store(true)
	cancelBlockedIO(w.file, windows.Handle(w.thread.Load()))
}

func (w *windowsRPCWriter) Close() error {
	if w.regular {
		return nil
	}
	var err error
	w.closeOnce.Do(func() {
		w.closed.Store(true)
		close(w.stop)
		w.Interrupt()
		err = w.file.Close()
		<-w.done
	})
	return err
}

func windowsFileIsRegular(file *os.File) (bool, error) {
	fileType, err := windows.GetFileType(windows.Handle(file.Fd()))
	if err != nil {
		return false, err
	}
	return fileType == windows.FILE_TYPE_DISK, nil
}

func duplicateWindowsHandle(file *os.File) (windows.Handle, error) {
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(process, windows.Handle(file.Fd()), process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return 0, err
	}
	if duplicate == 0 {
		return 0, errors.New("duplicate handle returned zero")
	}
	return duplicate, nil
}

func openRPCTransport() (*rpcTransport, error) {
	in, err := newWindowsRPCReader(os.Stdin)
	if err != nil {
		return nil, err
	}
	out, err := newWindowsRPCWriter(os.Stdout)
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
