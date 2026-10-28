package terminal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConsoleResizeWatcherOnlyPostsValidChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan [2]int)
	observed := make(chan struct{}, 32)
	inputs := make(chan Input, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		width, height := 80, 24
		watchConsoleResize(ctx, func() (int, int, error) {
			select {
			case size := <-updates:
				width, height = size[0], size[1]
			default:
			}
			select {
			case observed <- struct{}{}:
			default:
			}
			return width, height, nil
		}, inputs, time.Millisecond)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-observed:
		case <-time.After(time.Second):
			t.Fatal("size watcher did not sample the console")
		}
	}
	if len(inputs) != 0 {
		t.Fatal("unchanged idle console posted redraw events")
	}
	for _, size := range [][2]int{{0, 0}, {60, 16}, {20, 6}, {80, 24}} {
		select {
		case updates <- size:
		case <-time.After(time.Second):
			t.Fatal("size watcher stopped polling")
		}
		if size[0] == 0 {
			continue
		}
		select {
		case event := <-inputs:
			if !event.Resize || event.Err != nil || len(event.Key) != 0 || event.Paste {
				t.Fatalf("resize was interpreted as editor input: %+v", event)
			}
		case <-time.After(time.Second):
			t.Fatalf("valid size change %v did not post a resize", size)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("resize watcher survived console cancellation")
	}
}

func TestConsoleResizeWatcherStopsWithoutConsoleOrReceiver(t *testing.T) {
	watchConsoleResize(context.Background(), func() (int, int, error) {
		return 0, 0, errors.New("not a console")
	}, make(chan Input), time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	changed := make(chan struct{})
	go func() {
		defer close(done)
		first := true
		watchConsoleResize(ctx, func() (int, int, error) {
			if first {
				first = false
				return 80, 24, nil
			}
			close(changed)
			return 20, 6, nil
		}, make(chan Input), time.Millisecond)
	}()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("resize watcher did not reach blocked send")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked resize send prevented console cleanup")
	}
}
