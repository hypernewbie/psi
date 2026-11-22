package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/hypernewbie/psi/internal/secretmem"
)

// readSecretHandle takes ownership of file. On success it transfers the buffer
// to the caller. If the read times out, the reader owns the buffer until it
// returns and wipes it; a stalled filesystem can delay that cleanup.
func readSecretHandle(file *os.File, maxBytes int64, timeout time.Duration, validFinal func() bool) ([]byte, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	return readSecretHandleUntil(file, maxBytes, timer.C, validFinal)
}

func readSecretHandleUntil(file io.ReadCloser, maxBytes int64, timedOut <-chan time.Time, validFinal func() bool) ([]byte, error) {
	if maxBytes < 1 || maxBytes > maxSecretFileBytes {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	type result struct {
		data []byte
		err  error
	}
	results := make(chan result)
	abandoned := make(chan struct{})
	go func() {
		// One bounded allocation avoids leaving retired growth buffers with
		// credential fragments when the reader expands its storage.
		data := make([]byte, int(maxBytes)+1)
		n, err := io.ReadFull(file, data)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			err = nil
		}
		data = data[:n]
		select {
		case results <- result{data: data, err: err}:
		case <-abandoned:
			secretmem.Wipe(data[:cap(data)])
		}
	}()
	select {
	case result := <-results:
		close(abandoned)
		defer file.Close()
		if result.err != nil || int64(len(result.data)) > maxBytes || !validFinal() {
			secretmem.Wipe(result.data[:cap(result.data)])
			return nil, fmt.Errorf("secret file is unavailable")
		}
		return result.data, nil
	case <-timedOut:
		close(abandoned)
		// A stalled filesystem read may delay Close; do not delay the caller.
		go file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
}
