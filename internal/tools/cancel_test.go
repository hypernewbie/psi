package tools

// Phase D regressions for PLAN3 F06 (cancellation reaches write/edit steps)
// and F13 (Node-default creation modes).
//
// F06: DispatchCtx checks once, then context-free Execute ran to completion.
// Now write/edit are context-aware: checks before mkdir/read, after mkdir/
// read, around the mutation, and before success reporting. Cancellation can
// never roll a write back; results stay truthful about file changes.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestF06WriteCancelBeforeDispatch(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := reg.DispatchCtx(ctx, Invocation{ID: "w1", Name: "write",
		Arguments: map[string]any{"path": "a.txt", "content": "x"}})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "cancelled") {
		t.Fatalf("expected cancelled result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("file created despite pre-dispatch cancellation")
	}
}

func TestF06WriteCancelAfterMkdir(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	release := make(chan struct{})
	entered := make(chan struct{})
	reg.Write.AfterMkdir = func() { close(entered); <-release }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		done <- reg.DispatchCtx(ctx, Invocation{ID: "w1", Name: "write",
			Arguments: map[string]any{"path": "sub/a.txt", "content": "x"}})
	}()
	// Wait for the actual checkpoint, not a filesystem poll or busy loop.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("write did not reach the mkdir checkpoint")
	}
	cancel()
	close(release)
	res := <-done
	if !res.IsError || !strings.Contains(res.Content[0].Text, "cancelled") {
		t.Fatalf("expected cancelled result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "a.txt")); !os.IsNotExist(err) {
		t.Fatal("write proceeded after cancellation checkpoint")
	}
}

func TestF06WriteCancelDuringInjectedWrite(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	started := make(chan struct{})
	release := make(chan struct{})
	reg.Write.WriteFile = func(path string, data []byte, perm os.FileMode) error {
		close(started)
		<-release
		return os.WriteFile(path, data, perm)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		done <- reg.DispatchCtx(ctx, Invocation{ID: "w1", Name: "write",
			Arguments: map[string]any{"path": "a.txt", "content": "settled"}})
	}()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("cancelled call returned before the in-flight write settled")
	default:
	}
	close(release)
	res := <-done
	// The injected write settled synchronously; the post-write checkpoint
	// converts success into a truthful cancelled result.
	if !res.IsError || !strings.Contains(res.Content[0].Text, "cancelled after write") {
		t.Fatalf("expected truthful post-write cancellation: %+v", res)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(data) != "settled" {
		t.Fatalf("settled write lost: %q %v", data, err)
	}
}

func TestF06EditQueueWaitThenCancel(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	reg.Edit.AfterRead = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	firstDone := make(chan Result, 1)
	go func() {
		firstDone <- reg.DispatchCtx(context.Background(), Invocation{ID: "e1", Name: "edit",
			Arguments: map[string]any{"path": "f.txt", "edits": []any{map[string]any{"oldText": "base", "newText": "first"}}}})
	}()
	// Queue the second edit behind the first mutation, then cancel it.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first edit never reached the mutation hook")
	}
	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan Result, 1)
	go func() {
		secondDone <- reg.DispatchCtx(ctx, Invocation{ID: "e2", Name: "edit",
			Arguments: map[string]any{"path": "f.txt", "edits": []any{map[string]any{"oldText": "first", "newText": "second"}}}})
	}()
	cancel()
	close(release)
	r1 := <-firstDone
	if r1.IsError {
		t.Fatalf("first edit failed: %+v", r1)
	}
	r2 := <-secondDone
	if !r2.IsError || !strings.Contains(r2.Content[0].Text, "cancelled") {
		t.Fatalf("queued edit must not mutate after cancel: %+v", r2)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "first" {
		t.Fatalf("unexpected file content: %q", data)
	}
}

func TestF06EditCancelAfterRead(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	target := filepath.Join(dir, "g.txt")
	if err := os.WriteFile(target, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reg.Edit.AfterRead = func() { cancel() }
	res := reg.DispatchCtx(ctx, Invocation{ID: "e1", Name: "edit",
		Arguments: map[string]any{"path": "g.txt", "edits": []any{map[string]any{"oldText": "orig", "newText": "changed"}}}})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "cancelled before write") {
		t.Fatalf("expected no-mutation cancellation: %+v", res)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "orig" {
		t.Fatalf("file changed despite pre-write cancel: %q", data)
	}
}
