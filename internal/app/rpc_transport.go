package app

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// rpcFrameTimeout bounds one stdout frame on an interruptible stream. It never
// measures time since the last progress byte and never covers JSON marshal time.
const rpcFrameTimeout = 10 * time.Second

// rpcTransport owns the RPC stdio wrappers. The original os.Stdin/os.Stdout
// objects stay reachable but are not used for protocol I/O. Close restores
// platform flags and releases only the wrappers.
type rpcTransport struct {
	in           io.Reader
	out          io.Writer
	interruptIn  func()
	interruptOut func()
	close        func() error
}

func (t *rpcTransport) InterruptInput() {
	if t != nil && t.interruptIn != nil {
		t.interruptIn()
	}
}

func (t *rpcTransport) InterruptOutput() {
	if t != nil && t.interruptOut != nil {
		t.interruptOut()
	}
}

func (t *rpcTransport) Close() error {
	if t == nil || t.close == nil {
		return nil
	}
	return t.close()
}

type rpcDeadlineWriter interface {
	SetWriteDeadline(time.Time) error
}

// rpcEncoder serializes frames with its own mutex. It never holds rpcRunner.mu
// during JSON encoding or a blocked write.
type rpcEncoder struct {
	mu      sync.Mutex
	out     io.Writer
	err     error
	fail    func(error)
	timeout time.Duration
}

func newRPCEncoder(out io.Writer, fail func(error)) *rpcEncoder {
	return &rpcEncoder{out: out, fail: fail, timeout: rpcFrameTimeout}
}

func (e *rpcEncoder) Encode(value any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	var frame bytes.Buffer
	enc := json.NewEncoder(&frame)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return err
	}
	payload := frame.Bytes()
	var err error
	if setter, ok := e.out.(rpcDeadlineWriter); ok {
		if err := setter.SetWriteDeadline(time.Now().Add(e.timeout)); err != nil {
			e.failWrite(err)
			return err
		}
		_, err = e.out.Write(payload)
		_ = setter.SetWriteDeadline(time.Time{})
	} else {
		_, err = e.out.Write(payload)
	}
	if err != nil {
		e.failWrite(err)
		return err
	}
	return nil
}

func (e *rpcEncoder) failWrite(err error) {
	if err == nil {
		return
	}
	e.err = err
	if e.fail != nil {
		e.fail(err)
	}
}
