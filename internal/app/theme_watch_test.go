package app

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"
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
	agentDir := t.TempDir()
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.Mkdir(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(themeDir, "selected.json")
	watch, err := newThemeWatch(agentDir, path)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	for i := 0; i < 8; i++ {
		temp := filepath.Join(themeDir, ".selected.tmp")
		if err := os.WriteFile(temp, []byte(`{"name":"selected"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temp, path); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-watch.changes:
	case <-time.After(2 * time.Second):
		t.Fatal("theme save burst was not reported")
	}
	select {
	case <-watch.changes:
		t.Fatal("theme save burst produced more than one debounced change")
	case <-time.After(200 * time.Millisecond):
	}
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
