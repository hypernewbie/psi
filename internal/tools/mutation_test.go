package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Parallel-plan tool tests: the shared mutation queue orders same-file
// mutations in registration order, lets distinct files overlap, keys missing
// paths like Pi, and never releases a successor before settle.

// TestMutationQueueOrdersSameFileRegistrations: two edits to one file run in
// registration order without a lost update. The second edit must not read
// while the first is between read and write.
func TestMutationQueueOrdersSameFileRegistrations(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "same.txt")
	if err := os.WriteFile(target, []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two dispatches share one queue, with a per-call hook each, matching the
	// registry wiring where write and edit share the same queue.
	queue := NewMutationQueue()
	firstHook := make(chan struct{})
	releaseHook := make(chan struct{})
	secondReads := make(chan struct{}, 1)
	firstTool := &EditTool{Cwd: dir, Mutations: queue, AfterRead: func() { close(firstHook); <-releaseHook }}
	secondTool := &EditTool{Cwd: dir, Mutations: queue, AfterRead: func() { secondReads <- struct{}{} }}
	firstDone := make(chan Result, 1)
	secondDone := make(chan Result, 1)
	go func() {
		firstDone <- firstTool.ExecuteCtx(context.Background(), Invocation{ID: "e1", Name: Edit,
			Arguments: map[string]any{"path": "same.txt", "edits": []any{map[string]any{"oldText": "base", "newText": "first"}}}})
	}()
	<-firstHook
	// e1 holds the slot; e2 launches only now, so its registration strictly
	// follows e1's. e2 must wait for e1 to settle before it reads.
	go func() {
		secondDone <- secondTool.ExecuteCtx(context.Background(), Invocation{ID: "e2", Name: Edit,
			Arguments: map[string]any{"path": "same.txt", "edits": []any{map[string]any{"oldText": "first", "newText": "second"}}}})
	}()
	select {
	case <-secondReads:
		t.Fatal("second edit read while the first held the mutation slot")
	default:
	}
	close(releaseHook)
	for i := 0; i < 2; i++ {
		var res Result
		select {
		case res = <-firstDone:
		case res = <-secondDone:
		}
		if res.IsError {
			t.Fatalf("edit failed: %+v", res)
		}
	}
	data, _ := os.ReadFile(target)
	if string(data) != "second" {
		t.Fatalf("lost update: %q", data)
	}
}

// TestMutationQueueSharedAcrossWriteAndEdit: write and edit share one queue
// per file, including through a symlink alias.
func TestMutationQueueSharedAcrossWriteAndEdit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	alias := filepath.Join(dir, "alias.txt")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink creation failed: %v", err)
	}
	queue := NewMutationQueue()
	writeStarted := make(chan struct{})
	writeRelease := make(chan struct{})
	gateWrite := func() {
		select {
		case <-writeStarted:
		default:
			close(writeStarted)
		}
		<-writeRelease
	}
	writeTool := &WriteTool{Cwd: dir, Mutations: queue, AfterMkdir: gateWrite}
	editStarted := make(chan struct{})
	editTool := &EditTool{Cwd: dir, Mutations: queue, AfterRead: func() { close(editStarted) }}
	done := make(chan Result, 2)
	go func() {
		done <- writeTool.ExecuteCtx(context.Background(), Invocation{ID: "w1", Name: Write,
			Arguments: map[string]any{"path": "alias.txt", "content": "v2"}})
	}()
	<-writeStarted
	go func() {
		res := editTool.ExecuteCtx(context.Background(), Invocation{ID: "e1", Name: Edit,
			Arguments: map[string]any{"path": "real.txt", "edits": []any{map[string]any{"oldText": "v2", "newText": "v3"}}}})
		done <- res
	}()
	// The edit targets the same real file through a different name; it must
	// wait for the queued write to settle before reading.
	select {
	case <-editStarted:
		t.Fatal("edit read through a different name while the write held the slot")
	default:
	}
	close(writeRelease)
	for i := 0; i < 2; i++ {
		if res := <-done; res.IsError {
			t.Fatalf("mutation failed: %+v", res)
		}
	}
	data, _ := os.ReadFile(target)
	if string(data) != "v3" {
		t.Fatalf("expected v3 after ordered write then edit, got %q", data)
	}
}

// TestMutationQueueDistinctFilesOverlap: mutations to distinct files overlap.
func TestMutationQueueDistinctFilesOverlap(t *testing.T) {
	dir := t.TempDir()
	queue := NewMutationQueue()
	release := make(chan struct{})
	toolFor := func(file string, entered chan struct{}) *EditTool {
		target := filepath.Join(dir, file)
		if err := os.WriteFile(target, []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		return &EditTool{Cwd: dir, Mutations: queue, AfterRead: func() {
			select {
			case <-entered:
			default:
				close(entered)
			}
			<-release
		}}
	}
	enteredA := make(chan struct{})
	enteredB := make(chan struct{})
	toolA := toolFor("a.txt", enteredA)
	toolB := toolFor("b.txt", enteredB)
	done := make(chan Result, 2)
	go func() {
		done <- toolA.ExecuteCtx(context.Background(), Invocation{ID: "ea", Name: Edit,
			Arguments: map[string]any{"path": "a.txt", "edits": []any{map[string]any{"oldText": "a.txt", "newText": "a2"}}}})
	}()
	go func() {
		done <- toolB.ExecuteCtx(context.Background(), Invocation{ID: "eb", Name: Edit,
			Arguments: map[string]any{"path": "b.txt", "edits": []any{map[string]any{"oldText": "b.txt", "newText": "b2"}}}})
	}()
	// Both hooks must be live at once: distinct keys overlap.
	<-enteredA
	<-enteredB
	close(release)
	for i := 0; i < 2; i++ {
		if res := <-done; res.IsError {
			t.Fatalf("edit failed: %+v", res)
		}
	}
}

// TestMutationQueueMissingPathKey: two writes to one missing path serialize
// on the resolved absolute key, so the second write observes the first.
func TestMutationQueueMissingPathKey(t *testing.T) {
	dir := t.TempDir()
	queue := NewMutationQueue()
	first, err := queue.register(dir, "missing/x.txt")
	if err != nil {
		t.Fatal(err)
	}
	// A differently spelled path to the same missing file registers behind
	// the first slot on the same resolved key.
	second, err := queue.register(dir, filepath.Join(dir, "missing", "..", "missing", "x.txt"))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	firstDone := make(chan Result, 1)
	go func() { firstDone <- first.run(func() Result { <-release; return textResult("first") }) }()
	secondStarted := make(chan struct{}, 1)
	secondDone := make(chan Result, 1)
	go func() {
		secondDone <- second.run(func() Result { secondStarted <- struct{}{}; return textResult("second") })
	}()
	select {
	case <-secondStarted:
		t.Fatal("second mutation entered before the first settled")
	case <-firstDone:
		t.Fatal("first mutation settled while its body still blocked")
	default:
	}
	close(release)
	<-firstDone
	<-secondDone
}

// TestMutationQueueSlotSurvivesPanic: a panicking body still releases the
// successor slot.
func TestMutationQueueSlotSurvivesPanic(t *testing.T) {
	dir := t.TempDir()
	queue := NewMutationQueue()
	first, err := queue.register(dir, "p.txt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := queue.register(dir, "p.txt")
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		first.run(func() Result { panic("mutation body panicked") })
	}()
	done := make(chan Result, 1)
	go func() { done <- second.run(func() Result { return textResult("second") }) }()
	if res := <-done; res.IsError {
		t.Fatalf("successor failed after a panicking predecessor: %+v", res)
	}
}

// TestMutationQueueTailCleanup: settled keys leave no queue state behind.
func TestMutationQueueTailCleanup(t *testing.T) {
	dir := t.TempDir()
	queue := NewMutationQueue()
	if res := queue.withSlot(dir, "gone.txt", func() Result { return textResult("ok") }); res.IsError {
		t.Fatalf("mutation failed: %+v", res)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.tails) != 0 {
		t.Fatalf("settled key left queue state: %v", queue.tails)
	}
}
