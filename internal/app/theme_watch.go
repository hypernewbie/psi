package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// themeWatch only reports changes. The app loop performs the transactional
// reload and remains the sole owner of terminal output and theme state.
type themeWatch struct {
	watcher  *fsnotify.Watcher
	changes  chan struct{}
	stop     chan struct{}
	done     chan struct{}
	closed   sync.Once
	mu       sync.RWMutex
	selected string
}

func newThemeWatch(agentDir, selectedPath string) (*themeWatch, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	agentDir = filepath.Clean(agentDir)
	if err := watcher.Add(agentDir); err != nil {
		watcher.Close()
		return nil, err
	}
	watch := &themeWatch{watcher: watcher, changes: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	watch.SetSelected(selectedPath)
	themeDir := filepath.Join(agentDir, "themes")
	if info, err := os.Stat(themeDir); err == nil && info.IsDir() {
		if err := watcher.Add(themeDir); err != nil {
			watcher.Close()
			return nil, err
		}
	}
	go watch.run(agentDir, themeDir)
	return watch, nil
}

func (w *themeWatch) run(agentDir, themeDir string) {
	defer close(w.done)
	var timer *time.Timer
	var timerC <-chan time.Time
	reset := func() {
		if timer == nil {
			timer = time.NewTimer(100 * time.Millisecond)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(100 * time.Millisecond)
		}
		timerC = timer.C
	}
	drainEvents := func() bool {
		changed := false
		for {
			select {
			case event, ok := <-w.watcher.Events:
				if !ok {
					return changed
				}
				if w.handleEvent(event, agentDir, themeDir) {
					reset()
					changed = true
				}
			default:
				return changed
			}
		}
	}
	for {
		select {
		case <-w.stop:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-timerC:
			timerC = nil
			if drainEvents() {
				continue
			}
			select {
			case w.changes <- struct{}{}:
			default:
			}
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if w.handleEvent(event, agentDir, themeDir) {
				reset()
			}
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *themeWatch) handleEvent(event fsnotify.Event, agentDir, themeDir string) bool {
	name := filepath.Clean(event.Name)
	if name == themeDir {
		if event.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
			if info, err := os.Stat(themeDir); err == nil && info.IsDir() {
				_ = w.watcher.Add(themeDir)
				return w.selectedIn(themeDir)
			}
		}
		return false
	}
	if filepath.Dir(name) != themeDir || !strings.HasSuffix(strings.ToLower(name), ".json") {
		// The agent-directory watch exists only to detect creation of the
		// themes directory; settings and auth changes are not reloads.
		if filepath.Dir(name) == agentDir && filepath.Base(name) == "themes" {
			if info, err := os.Stat(themeDir); err == nil && info.IsDir() {
				_ = w.watcher.Add(themeDir)
				return w.selectedIn(themeDir)
			}
		}
		return false
	}
	return w.isSelected(name)
}

func (w *themeWatch) SetSelected(path string) {
	if path != "" {
		path = filepath.Clean(path)
	}
	w.mu.Lock()
	w.selected = path
	w.mu.Unlock()
}

func (w *themeWatch) selectedIn(dir string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.selected != "" && filepath.Dir(w.selected) == dir
}

func (w *themeWatch) isSelected(path string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.selected != "" && filepath.Clean(path) == w.selected
}

func (w *themeWatch) Close() {
	if w == nil {
		return
	}
	w.closed.Do(func() {
		close(w.stop)
		_ = w.watcher.Close()
		<-w.done
	})
}
