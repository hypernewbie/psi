package terminal

import (
	"context"
	"strconv"
	"strings"
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

// watchConsoleResize supplies the resize notifications that Pi receives from
// stdout's resize event. Windows has no SIGWINCH, and a pending console ReadFile
// can block across a resize, so it cannot share the key-reader's polling loop.
// Only a changed valid size posts an event; output stays on the app event loop.
func watchConsoleResize(ctx context.Context, size func() (int, int, error), inputs chan<- Input, interval time.Duration) {
	width, height, err := size()
	if err != nil || width <= 0 || height <= 0 {
		return // Not a console (for example, an isolated pipe-based test).
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			nextWidth, nextHeight, err := size()
			if err != nil || nextWidth <= 0 || nextHeight <= 0 || (nextWidth == width && nextHeight == height) {
				continue
			}
			select {
			case inputs <- Input{Resize: true}:
				width, height = nextWidth, nextHeight
			case <-ctx.Done():
				return
			}
		}
	}
}
