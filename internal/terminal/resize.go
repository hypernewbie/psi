package terminal

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

func ParseCursorPositionReport(data []byte) (row, col int, ok bool) {
	text := string(data)
	if !strings.HasPrefix(text, "\x1b[") || !strings.HasSuffix(text, "R") {
		return 0, 0, false
	}
	parts := strings.Split(text[2:len(text)-1], ";")
	if len(parts) != 2 {
		return 0, 0, false
	}
	row, errRow := strconv.Atoi(parts[0])
	col, errCol := strconv.Atoi(parts[1])
	if errRow != nil || errCol != nil || row < 1 || col < 1 {
		return 0, 0, false
	}
	return row, col, true
}

// resizeState records the viewport size the app has drawn or already queued a
// redraw for. A Windows size poll can miss a size that appears and disappears
// between ticks. If another frame draws at that transient size, comparing only
// against the previous sample would treat the older sampled size as already
// handled and never redraw the final size.
type resizeState struct {
	mu            sync.Mutex
	width, height int
}

func (s *resizeState) setApplied(width, height int) {
	s.mu.Lock()
	s.width, s.height = width, height
	s.mu.Unlock()
}

func (s *resizeState) needsRedraw(width, height int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return width != s.width || height != s.height
}

// watchConsoleResize supplies the resize notifications that Pi receives from
// stdout's resize event. Windows has no SIGWINCH, and a pending console ReadFile
// can block across a resize, so it cannot share the key-reader's polling loop.
// Only a size that differs from the drawn or queued size posts an event; output
// stays on the app event loop.
func watchConsoleResize(ctx context.Context, size func() (int, int, error), applied *resizeState, inputs chan<- Input, interval time.Duration) {
	width, height, err := size()
	if err != nil || width <= 0 || height <= 0 {
		return // Not a console (for example, an isolated pipe-based test).
	}
	applied.setApplied(width, height)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			nextWidth, nextHeight, err := size()
			if err != nil || nextWidth <= 0 || nextHeight <= 0 || !applied.needsRedraw(nextWidth, nextHeight) {
				continue
			}
			select {
			case inputs <- Input{Resize: true}:
				applied.setApplied(nextWidth, nextHeight)
			case <-ctx.Done():
				return
			}
		}
	}
}
