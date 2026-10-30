package tools

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/testutil/leakcheck"
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

// TestMutationQueue200GoroutinesSameFileEdits (T-TL-01): 200 goroutines doing
// read-modify-write via EditTool on distinct lines of the same file concurrently.
// All edits must succeed without lost updates, and execution must be serial.
func TestMutationQueue200GoroutinesSameFileEdits(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	filePath := filepath.Join(dir, "shared.txt")

	const n = 200
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(fmt.Sprintf("line %03d: 0\n", i))
	}
	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	queue := NewMutationQueue()
	var active atomic.Int32
	var maxActive atomic.Int32

	results := make([]Result, n)
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			tool := &EditTool{
				Cwd:       dir,
				Mutations: queue,
				AfterRead: func() {
					cur := active.Add(1)
					for {
						m := maxActive.Load()
						if cur <= m || maxActive.CompareAndSwap(m, cur) {
							break
						}
					}
					time.Sleep(100 * time.Microsecond)
					active.Add(-1)
				},
			}

			oldLine := fmt.Sprintf("line %03d: 0", i)
			newLine := fmt.Sprintf("line %03d: 1", i)
			res := tool.ExecuteCtx(context.Background(), Invocation{
				ID:   fmt.Sprintf("edit-%03d", i),
				Name: Edit,
				Arguments: map[string]any{
					"path": "shared.txt",
					"edits": []any{
						map[string]any{"oldText": oldLine, "newText": newLine},
					},
				},
			})
			results[i] = res
		}()
	}

	wg.Wait()

	for i, res := range results {
		if res.IsError {
			t.Fatalf("edit %d failed: %+v", i, res)
		}
	}

	if m := maxActive.Load(); m > 1 {
		t.Fatalf("concurrency violation: maxActive was %d (expected 1)", m)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	for i := 0; i < n; i++ {
		expected := fmt.Sprintf("line %03d: 1\n", i)
		if !strings.Contains(content, expected) {
			t.Fatalf("lost update: line %03d not updated to 1 in:\n%s", i, content)
		}
	}
}

// TestMutationQueue200GoroutinesExactRegistrationOrder (T-TL-03): verifies
// that mutations registered under the queue lock execute in exact registration order.
func TestMutationQueue200GoroutinesExactRegistrationOrder(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	queue := NewMutationQueue()

	const n = 200
	type item struct {
		slot *mutationSlot
		seq  int
	}

	items := make([]item, n)
	var regMu sync.Mutex
	var regWg sync.WaitGroup
	var nextRegSeq int
	regWg.Add(n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer regWg.Done()
			regMu.Lock()
			seq := nextRegSeq
			nextRegSeq++
			slot, err := queue.register(dir, "order.txt")
			if err != nil {
				regMu.Unlock()
				t.Errorf("register failed: %v", err)
				return
			}
			items[i] = item{slot: slot, seq: seq}
			regMu.Unlock()
		}()
	}
	regWg.Wait()

	// Reorder slots by registered sequence so we launch them concurrently.
	var execMu sync.Mutex
	execOrder := make([]int, 0, n)
	var active atomic.Int32
	var maxActive atomic.Int32

	var runWg sync.WaitGroup
	runWg.Add(n)
	for i := 0; i < n; i++ {
		it := items[i]
		go func() {
			defer runWg.Done()
			it.slot.run(func() Result {
				cur := active.Add(1)
				for {
					m := maxActive.Load()
					if cur <= m || maxActive.CompareAndSwap(m, cur) {
						break
					}
				}
				execMu.Lock()
				execOrder = append(execOrder, it.seq)
				execMu.Unlock()
				active.Add(-1)
				return textResult("ok")
			})
		}()
	}
	runWg.Wait()

	if m := maxActive.Load(); m > 1 {
		t.Fatalf("concurrency violation: maxActive was %d", m)
	}

	if len(execOrder) != n {
		t.Fatalf("expected %d executions, got %d", n, len(execOrder))
	}
	for i, seq := range execOrder {
		if seq != i {
			t.Fatalf("order mismatch at index %d: got seq %d", i, seq)
		}
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.tails) != 0 {
		t.Fatalf("tails not cleaned up: %v", queue.tails)
	}
}

// TestMutationQueueInFlightAbortedWriteHoldsSlot (Pi parity): an aborted write
// in flight keeps the queue locked until it settles; subsequent write proceeds cleanly.
func TestMutationQueueInFlightAbortedWriteHoldsSlot(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	filePath := filepath.Join(dir, "abort-write.txt")
	queue := NewMutationQueue()

	firstWriteStarted := make(chan struct{})
	finishFirstWrite := make(chan struct{})
	secondWriteStarted := make(chan struct{})
	var firstWriteSettled atomic.Bool

	writeTool := &WriteTool{
		Cwd:       dir,
		Mutations: queue,
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			if string(data) == "first\n" {
				close(firstWriteStarted)
				<-finishFirstWrite
				err := os.WriteFile(path, data, perm)
				firstWriteSettled.Store(true)
				return err
			}
			if string(data) == "second\n" {
				if !firstWriteSettled.Load() {
					t.Error("second write started before first write settled")
				}
				select {
				case <-secondWriteStarted:
				default:
					close(secondWriteStarted)
				}
			}
			return os.WriteFile(path, data, perm)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan Result, 1)
	go func() {
		firstDone <- writeTool.ExecuteCtx(ctx, Invocation{
			ID:   "w1",
			Name: Write,
			Arguments: map[string]any{
				"path":    "abort-write.txt",
				"content": "first\n",
			},
		})
	}()

	<-firstWriteStarted
	cancel() // abort first write while it is in WriteFile

	secondDone := make(chan Result, 1)
	go func() {
		secondDone <- writeTool.ExecuteCtx(context.Background(), Invocation{
			ID:   "w2",
			Name: Write,
			Arguments: map[string]any{
				"path":    "abort-write.txt",
				"content": "second\n",
			},
		})
	}()

	// Assert second write has not started while first write is still blocked.
	select {
	case <-secondWriteStarted:
		t.Fatal("second write started before first write finished")
	case <-time.After(20 * time.Millisecond):
	}

	close(finishFirstWrite)

	res1 := <-firstDone
	if !res1.IsError || !strings.Contains(res1.Content[0].Text, "cancelled") {
		t.Fatalf("expected first write to report cancelled: %+v", res1)
	}

	res2 := <-secondDone
	if res2.IsError {
		t.Fatalf("second write failed: %+v", res2)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second\n" {
		t.Fatalf("expected second write content, got %q", string(data))
	}
}

// TestMutationQueueInFlightAbortedEditHoldsSlot (Pi parity): an aborted edit
// in flight keeps the queue locked until it settles; subsequent edit proceeds cleanly.
func TestMutationQueueInFlightAbortedEditHoldsSlot(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	filePath := filepath.Join(dir, "abort-edit.txt")
	if err := os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	queue := NewMutationQueue()
	firstWriteStarted := make(chan struct{})
	finishFirstWrite := make(chan struct{})
	secondWriteStarted := make(chan struct{})
	var firstWriteSettled atomic.Bool

	editTool := &EditTool{
		Cwd:       dir,
		Mutations: queue,
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			if strings.Contains(string(data), "ALPHA") && !strings.Contains(string(data), "BETA") {
				close(firstWriteStarted)
				<-finishFirstWrite
				err := os.WriteFile(path, data, perm)
				firstWriteSettled.Store(true)
				return err
			}
			if strings.Contains(string(data), "BETA") {
				if !firstWriteSettled.Load() {
					t.Error("second edit started before first edit settled")
				}
				select {
				case <-secondWriteStarted:
				default:
					close(secondWriteStarted)
				}
			}
			return os.WriteFile(path, data, perm)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan Result, 1)
	go func() {
		firstDone <- editTool.ExecuteCtx(ctx, Invocation{
			ID:   "e1",
			Name: Edit,
			Arguments: map[string]any{
				"path": "abort-edit.txt",
				"edits": []any{
					map[string]any{"oldText": "alpha", "newText": "ALPHA"},
				},
			},
		})
	}()

	<-firstWriteStarted
	cancel() // abort first edit while in WriteFile

	secondDone := make(chan Result, 1)
	go func() {
		secondDone <- editTool.ExecuteCtx(context.Background(), Invocation{
			ID:   "e2",
			Name: Edit,
			Arguments: map[string]any{
				"path": "abort-edit.txt",
				"edits": []any{
					map[string]any{"oldText": "beta", "newText": "BETA"},
				},
			},
		})
	}()

	select {
	case <-secondWriteStarted:
		t.Fatal("second edit started before first edit settled")
	case <-time.After(20 * time.Millisecond):
	}

	close(finishFirstWrite)

	res1 := <-firstDone
	if !res1.IsError || !strings.Contains(res1.Content[0].Text, "cancelled") {
		t.Fatalf("expected first edit to report cancelled: %+v", res1)
	}

	res2 := <-secondDone
	if res2.IsError {
		t.Fatalf("second edit failed: %+v", res2)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ALPHA\nBETA\n" {
		t.Fatalf("expected ALPHA\\nBETA\\n, got %q", string(data))
	}
}

// TestMutationQueueAbortedWaiterInQueueDoesNotBlockSuccessors: when a queued
// mutation is cancelled while waiting, it unblocks promptly and allows the
// successor mutation to acquire the slot.
func TestMutationQueueAbortedWaiterInQueueDoesNotBlockSuccessors(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	filePath := filepath.Join(dir, "wait-abort.txt")
	queue := NewMutationQueue()

	w1Started := make(chan struct{})
	releaseW1 := make(chan struct{})

	writeTool := &WriteTool{
		Cwd:       dir,
		Mutations: queue,
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			if string(data) == "w1" {
				close(w1Started)
				<-releaseW1
			}
			return os.WriteFile(path, data, perm)
		},
	}

	// W1 holds slot
	w1Done := make(chan Result, 1)
	go func() {
		w1Done <- writeTool.ExecuteCtx(context.Background(), Invocation{
			ID:        "w1",
			Name:      Write,
			Arguments: map[string]any{"path": "wait-abort.txt", "content": "w1"},
		})
	}()
	<-w1Started

	// W2 queues behind W1, then is cancelled
	ctx2, cancel2 := context.WithCancel(context.Background())
	w2Done := make(chan Result, 1)
	go func() {
		w2Done <- writeTool.ExecuteCtx(ctx2, Invocation{
			ID:        "w2",
			Name:      Write,
			Arguments: map[string]any{"path": "wait-abort.txt", "content": "w2"},
		})
	}()
	time.Sleep(10 * time.Millisecond)
	cancel2()

	// W3 queues behind W2
	w3Done := make(chan Result, 1)
	go func() {
		w3Done <- writeTool.ExecuteCtx(context.Background(), Invocation{
			ID:        "w3",
			Name:      Write,
			Arguments: map[string]any{"path": "wait-abort.txt", "content": "w3"},
		})
	}()

	close(releaseW1)

	r1 := <-w1Done
	if r1.IsError {
		t.Fatalf("w1 failed: %+v", r1)
	}

	r2 := <-w2Done
	if !r2.IsError || !strings.Contains(r2.Content[0].Text, "cancelled") {
		t.Fatalf("w2 expected cancelled: %+v", r2)
	}

	r3 := <-w3Done
	if r3.IsError {
		t.Fatalf("w3 failed: %+v", r3)
	}

	data, _ := os.ReadFile(filePath)
	if string(data) != "w3" {
		t.Fatalf("expected w3, got %q", string(data))
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.tails) != 0 {
		t.Fatalf("tails not cleaned up: %v", queue.tails)
	}
}

// TestMutationQueueSymlinkFlippedBetweenRegisterAndRun (T-TL-06): symlink
// target flips after register; slot execution must proceed without deadlock.
func TestMutationQueueSymlinkFlippedBetweenRegisterAndRun(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	target1 := filepath.Join(dir, "target1.txt")
	target2 := filepath.Join(dir, "target2.txt")
	link := filepath.Join(dir, "link.txt")

	_ = os.WriteFile(target1, []byte("t1"), 0o644)
	_ = os.WriteFile(target2, []byte("t2"), 0o644)
	if err := os.Symlink(target1, link); err != nil {
		t.Skipf("symlink creation failed: %v", err)
	}

	queue := NewMutationQueue()
	slot, err := queue.register(dir, "link.txt")
	if err != nil {
		t.Fatal(err)
	}

	// Flip symlink to target2
	_ = os.Remove(link)
	if err := os.Symlink(target2, link); err != nil {
		t.Fatal(err)
	}

	res := slot.run(func() Result {
		return textResult("flipped-ok")
	})
	if res.IsError || res.Content[0].Text != "flipped-ok" {
		t.Fatalf("unexpected result: %+v", res)
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.tails) != 0 {
		t.Fatalf("tails map not empty: %v", queue.tails)
	}
}

// TestMutationQueueConsecutivePanicsCleanTails: multiple panicking mutations
// in sequence release slots and clean up the tails map completely.
func TestMutationQueueConsecutivePanicsCleanTails(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	queue := NewMutationQueue()

	const panics = 5
	slots := make([]*mutationSlot, panics+1)
	for i := 0; i <= panics; i++ {
		var err error
		slots[i], err = queue.register(dir, "panic.txt")
		if err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < panics; i++ {
		i := i
		func() {
			defer func() { _ = recover() }()
			slots[i].run(func() Result {
				panic(fmt.Sprintf("panic-%d", i))
			})
		}()
	}

	// The final slot runs normally
	res := slots[panics].run(func() Result {
		return textResult("survivor")
	})
	if res.IsError || res.Content[0].Text != "survivor" {
		t.Fatalf("survivor failed: %+v", res)
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.tails) != 0 {
		t.Fatalf("settled panics left queue state: %v", queue.tails)
	}
}

// TestMutationQueue10kDistinctPathsBounded (T-TL-05): 10,000 distinct paths
// touched through withSlot leaves the tails map empty.
func TestMutationQueue10kDistinctPathsBounded(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	queue := NewMutationQueue()

	for i := 0; i < 10_000; i++ {
		path := fmt.Sprintf("path-%d.txt", i)
		res := queue.withSlot(dir, path, func() Result {
			return textResult("ok")
		})
		if res.IsError {
			t.Fatalf("withSlot failed on %s: %+v", path, res)
		}
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.tails) != 0 {
		t.Fatalf("expected 0 tails, got %d", len(queue.tails))
	}
}

// TestMutationQueueRandomizedMixModelChecked (T-TL-02): randomized mix of writes
// and edits across multiple files verified against a sequential model.
func TestMutationQueueRandomizedMixModelChecked(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	queue := NewMutationQueue()

	const numFiles = 4
	const numOps = 40

	files := make([]string, numFiles)
	for i := 0; i < numFiles; i++ {
		files[i] = fmt.Sprintf("file_%d.txt", i)
		_ = os.WriteFile(filepath.Join(dir, files[i]), []byte("init"), 0o644)
	}

	// Launch concurrent operations, capturing their execution order per file
	type fileOp struct {
		file    string
		content string
	}

	var regMu sync.Mutex
	regOrderPerFile := make(map[string][]string)

	var wg sync.WaitGroup
	wg.Add(numOps)

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < numOps; i++ {
		fileIdx := rng.Intn(numFiles)
		fileName := files[fileIdx]
		tag := fmt.Sprintf("op_%d", i)

		regMu.Lock()
		regOrderPerFile[fileName] = append(regOrderPerFile[fileName], tag)
		slot, err := queue.register(dir, fileName)
		regMu.Unlock()

		if err != nil {
			t.Fatal(err)
		}

		go func(s *mutationSlot, fn, tg string) {
			defer wg.Done()
			s.run(func() Result {
				p := filepath.Join(dir, fn)
				data, _ := os.ReadFile(p)
				_ = os.WriteFile(p, []byte(string(data)+":"+tg), 0o644)
				return textResult("ok")
			})
		}(slot, fileName, tag)
	}

	wg.Wait()

	for _, fn := range files {
		data, err := os.ReadFile(filepath.Join(dir, fn))
		if err != nil {
			t.Fatal(err)
		}
		expected := "init"
		for _, tag := range regOrderPerFile[fn] {
			expected += ":" + tag
		}
		if string(data) != expected {
			t.Fatalf("file %s mismatch:\ngot:  %s\nwant: %s", fn, string(data), expected)
		}
	}
}

// TestTailCaptureConcurrentWritesAndReads (T-TL-12): concurrent Write, lastWrite,
// and touch operations on tailCapture run race-free under -race.
func TestTailCaptureConcurrentWritesAndReads(t *testing.T) {
	leakcheck.Verify(t)
	f, err := os.CreateTemp("", "psi-capture-race-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()

	cap := &tailCapture{file: f, cancel: func() {}}
	const workers = 30
	var wg sync.WaitGroup
	wg.Add(workers * 3)

	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = cap.Write([]byte(fmt.Sprintf("worker-%d-chunk-%d\n", id, j)))
			}
		}(i)

		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				cap.touch()
			}
		}()

		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = cap.lastWrite()
			}
		}()
	}

	wg.Wait()
}
