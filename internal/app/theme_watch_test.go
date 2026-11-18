package app

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestThemeWatchDebouncesAtomicUserThemeSaveAndCloses(t *testing.T) {
	agentDir := t.TempDir()
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.Mkdir(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	watch, err := newThemeWatch(agentDir, filepath.Join(themeDir, "selected.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	temporary := filepath.Join(themeDir, ".selected.json.tmp")
	final := filepath.Join(themeDir, "selected.json")
	if err := os.WriteFile(temporary, []byte(`{"name":"selected"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, final); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watch.changes:
	case <-time.After(2 * time.Second):
		t.Fatal("atomic theme save was not reported")
	}
	watch.Close()
}

func TestThemeWatchNoticesThemesDirectoryCreatedAfterStartup(t *testing.T) {
	agentDir := t.TempDir()
	themeDir := filepath.Join(agentDir, "themes")
	watch, err := newThemeWatch(agentDir, filepath.Join(themeDir, "new.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if err := os.Mkdir(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "new.json"), []byte(`{"name":"new"}`), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watch.changes:
	case <-time.After(2 * time.Second):
		t.Fatal("created theme directory was not noticed")
	}
}

func TestThemeWatchIgnoresUnselectedThemeFiles(t *testing.T) {
	agentDir := t.TempDir()
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.Mkdir(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	watch, err := newThemeWatch(agentDir, filepath.Join(themeDir, "selected.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if err := os.WriteFile(filepath.Join(themeDir, "other.json"), []byte(`{"name":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watch.changes:
		t.Fatal("unselected theme file triggered an automatic reload")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestThemeWatchDebouncesBurstToSingleChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agentDir := filepath.Join("synthetic", "agent")
		themeDir := filepath.Join(agentDir, "themes")
		selected := filepath.Join(themeDir, "selected.json")
		events := make(chan fsnotify.Event, 8)
		for i := 0; i < 8; i++ {
			events <- fsnotify.Event{Name: selected, Op: fsnotify.Create}
		}
		w := &themeWatch{
			changes: make(chan struct{}, 10),
			stop:    make(chan struct{}),
			done:    make(chan struct{}),
		}
		w.SetSelected(selected)
		watchErrors := make(chan error)
		go w.runEvents(agentDir, themeDir, events, watchErrors)
		defer func() {
			close(w.stop)
			<-w.done
		}()

		synctest.Wait()
		if got := len(w.changes); got != 0 {
			t.Fatalf("after queued burst: got %d notifications, want 0", got)
		}

		// These sleeps advance the bubble's fake clock, not the host clock.
		time.Sleep(50 * time.Millisecond)
		events <- fsnotify.Event{Name: selected, Op: fsnotify.Create}
		synctest.Wait()

		time.Sleep(99 * time.Millisecond)
		synctest.Wait()
		if got := len(w.changes); got != 0 {
			t.Fatalf("before reset deadline: got %d notifications, want 0", got)
		}

		time.Sleep(2 * time.Millisecond)
		synctest.Wait()
		if got := len(w.changes); got != 1 {
			t.Fatalf("at reset deadline: got %d notifications, want 1", got)
		}
		<-w.changes

		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if got := len(w.changes); got != 0 {
			t.Fatalf("after quiet window: got %d notifications, want 0", got)
		}
	})
}

func TestThemeWatchRepeatedLifecycleDoesNotLeakGoroutines(t *testing.T) {
	agentDir := t.TempDir()
	before := goruntime.NumGoroutine()
	fdCount := func() int {
		if goruntime.GOOS != "linux" {
			return -1
		}
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatalf("read process descriptors: %v", err)
		}
		return len(entries)
	}
	fdsBefore := fdCount()
	for i := 0; i < 100; i++ {
		watch, err := newThemeWatch(agentDir, "")
		if err != nil {
			t.Fatal(err)
		}
		watch.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for goruntime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		goruntime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if got := goruntime.NumGoroutine(); got > before+2 {
		t.Fatalf("theme watcher goroutines grew from %d to %d", before, got)
	}
	if got := fdCount(); fdsBefore >= 0 && got > fdsBefore {
		t.Fatalf("theme watcher descriptors grew from %d to %d", fdsBefore, got)
	}
}
