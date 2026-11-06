//go:build !windows

package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"psi/internal/agent"
	pibridgepb "psi/internal/pibridge/pb"
	"psi/internal/session"
)

var attachmentPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 1, 2, 3}

func writeAttachmentPNG(t *testing.T, dir string) []byte {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "a.png"), attachmentPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	return attachmentPNG
}

func attachmentImageRef(name string, data []byte) *pibridgepb.AttachmentRef {
	sum := sha256.Sum256(data)
	return &pibridgepb.AttachmentRef{
		Basename: name, SizeBytes: uint64(len(data)), MimeType: "image/png",
		Kind: "image", Sha256: hex.EncodeToString(sum[:]),
	}
}

func attachmentFileRef(name string, data []byte) *pibridgepb.AttachmentRef {
	sum := sha256.Sum256(data)
	return &pibridgepb.AttachmentRef{
		Basename: name, SizeBytes: uint64(len(data)), MimeType: "application/octet-stream",
		Kind: "file", Sha256: hex.EncodeToString(sum[:]),
	}
}

func attachmentPrompt(r *runtime, id, text string, refs ...*pibridgepb.AttachmentRef) *pibridgepb.PhiToPi {
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_AttachmentPromptRequest{
		AttachmentPromptRequest: &pibridgepb.AttachmentPromptRequest{
			RequestId:         id,
			ExpectedSessionId: r.store.Header().ID,
			ExpectedLeafId:    r.store.LeafID(),
			ExpectedModel:     &pibridgepb.ModelSnapshot{Provider: r.ov.Provider, Model: r.ov.Model},
			Text:              text,
			Attachments:       refs,
		},
	}}
}

func attachmentAck(t *testing.T, f *bridgePhi, id string) *pibridgepb.AttachmentPromptAck {
	t.Helper()
	frame := f.waitFrame(t, "attachment ack", func(frame *pibridgepb.PiToPhi) bool {
		ack := frame.GetAttachmentPromptAck()
		return ack != nil && ack.GetRequestId() == id
	})
	return frame.GetAttachmentPromptAck()
}

// countAttachmentAcks counts every ack for one id, so a second answer cannot
// hide behind the first.
func countAttachmentAcks(f *bridgePhi, id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, frame := range f.frames {
		if ack := frame.GetAttachmentPromptAck(); ack != nil && ack.GetRequestId() == id {
			count++
		}
	}
	return count
}

// nthAttachmentAck waits for the n-th ack of one id. Every accepted request
// produces exactly one answer, so an extra ack is a contract failure.
func nthAttachmentAck(t *testing.T, f *bridgePhi, id string, n int) *pibridgepb.AttachmentPromptAck {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countAttachmentAcks(f, id) >= n {
			f.mu.Lock()
			seen := 0
			for _, frame := range f.frames {
				ack := frame.GetAttachmentPromptAck()
				if ack == nil || ack.GetRequestId() != id {
					continue
				}
				seen++
				if seen == n {
					f.mu.Unlock()
					return ack
				}
			}
			f.mu.Unlock()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for attachment ack %d of %q", n, id)
	return nil
}

// waitPreparation blocks until the single preparation worker returns.
func waitPreparation(t *testing.T, h *bridgeHandler) {
	t.Helper()
	select {
	case <-h.completed():
	case <-time.After(10 * time.Second):
		t.Fatal("attachment preparation did not finish")
	}
}

// One idle prompt is verified, handed to the shared start path as exactly one
// image block, and acknowledged DISPATCHED only after that launch.
func TestAttachmentIdleDispatchLaunchesOneImage(t *testing.T) {
	r := testRuntime(t)
	r.state = StateIdle
	r.cfg.Model.InputImage = true
	root := t.TempDir()
	data := writeAttachmentPNG(t, root)
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	f, h := startBridgeRuntime(t, r)
	if !r.bridge.AttachmentsAvailable() {
		t.Fatal("root was not offered to the bridge")
	}
	var launched agent.UserInput
	launches := 0
	h.startInput = func(input agent.UserInput, focus string, compact bool) (bool, string) {
		launched, launches = input, launches+1
		return true, ""
	}

	f.inject <- attachmentPrompt(r, "7", "look", attachmentImageRef("a.png", data))
	h.handle(nextRequest(t, r.bridge), false)
	if h.prep == nil {
		t.Fatal("admitted attachment did not occupy the preparation slot")
	}
	waitPreparation(t, h)
	h.finishPreparation(false)

	ack := attachmentAck(t, f, "7")
	if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_DISPATCHED || ack.GetReason() != "" {
		t.Fatalf("ack = %v %q, want DISPATCHED with no reason", ack.GetOutcome(), ack.GetReason())
	}
	if launches != 1 {
		t.Fatalf("launches = %d, want 1", launches)
	}
	blocks, ok := launched.Content.([]session.UserContentBlock)
	if !ok || len(blocks) != 2 || blocks[1].Image == nil {
		t.Fatalf("launched content = %#v, want prompt text and one image block", launched.Content)
	}
	decoded, err := base64.StdEncoding.DecodeString(blocks[1].Image.Data)
	if err != nil || string(decoded) != string(data) {
		t.Fatalf("launched bytes = %q, %v; want the verified file bytes", decoded, err)
	}
	if launched.Display == "" || launched.ImageBytes != uint64(len(data)) {
		t.Fatalf("display = %q, bytes = %d", launched.Display, launched.ImageBytes)
	}
}

// Every refusal class answers before a launch and before a payload read.
func TestAttachmentRefusalsPrecedeReadingAndLaunch(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*bridgeHandler, *runtime, *pibridgepb.PhiToPi)
		busy   bool
		reason string
	}{
		{name: "stale session", mutate: func(_ *bridgeHandler, _ *runtime, msg *pibridgepb.PhiToPi) {
			msg.GetAttachmentPromptRequest().ExpectedSessionId = "other"
		}, reason: "stale"},
		// The ref names no file, so a "stale" answer proves nothing was read.
		{name: "stale before read", mutate: func(_ *bridgeHandler, _ *runtime, msg *pibridgepb.PhiToPi) {
			msg.GetAttachmentPromptRequest().ExpectedSessionId = "other"
		}, reason: "stale"},
		{name: "stale generation", mutate: func(_ *bridgeHandler, r *runtime, _ *pibridgepb.PhiToPi) {
			r.generation = 2
		}, reason: "stale"},
		{name: "stale model", mutate: func(_ *bridgeHandler, _ *runtime, msg *pibridgepb.PhiToPi) {
			msg.GetAttachmentPromptRequest().ExpectedModel = &pibridgepb.ModelSnapshot{Provider: "other", Model: "m"}
		}, reason: "stale"},
		{name: "absent model", mutate: func(_ *bridgeHandler, _ *runtime, msg *pibridgepb.PhiToPi) {
			msg.GetAttachmentPromptRequest().ExpectedModel = nil
		}, reason: "stale"},
		{name: "preparation pending", mutate: func(h *bridgeHandler, _ *runtime, _ *pibridgepb.PhiToPi) {
			h.prep = &bridgePrep{}
		}, reason: "pending"},
		{name: "command text", mutate: func(_ *bridgeHandler, _ *runtime, msg *pibridgepb.PhiToPi) {
			msg.GetAttachmentPromptRequest().Text = " \t/compact"
		}, reason: "command"},
		{name: "storage blocked", mutate: func(_ *bridgeHandler, r *runtime, _ *pibridgepb.PhiToPi) {
			r.state = StateStorageBlocked
		}, reason: "storage"},
		{name: "compaction active", mutate: func(_ *bridgeHandler, r *runtime, _ *pibridgepb.PhiToPi) {
			r.compactActive = true
		}, reason: "compaction-active"},
		{name: "run busy", mutate: nil, busy: true, reason: "busy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRuntime(t)
			r.state = StateIdle
			r.cfg.Model.InputImage = true
			root := t.TempDir()
			data := writeAttachmentPNG(t, root)
			t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
			f, h := startBridgeRuntime(t, r)
			launched := false
			h.startInput = func(agent.UserInput, string, bool) (bool, string) {
				launched = true
				return true, ""
			}
			ref := attachmentImageRef("a.png", data)
			if tc.name == "stale before read" {
				ref = attachmentImageRef("absent.png", data)
			}
			msg := attachmentPrompt(r, "5", "look", ref)
			if tc.mutate != nil {
				tc.mutate(h, r, msg)
			}
			// The pending case seeds the slot itself; every other refusal
			// must leave the slot free.
			seeded := h.prep != nil
			f.inject <- msg
			h.handle(nextRequest(t, r.bridge), tc.busy)

			ack := attachmentAck(t, f, "5")
			if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != tc.reason {
				t.Fatalf("ack = %v %q, want REFUSED %q", ack.GetOutcome(), ack.GetReason(), tc.reason)
			}
			if launched {
				t.Fatal("refused attachment still launched a run")
			}
			if h.prep != nil && !seeded {
				t.Fatal("refused attachment occupied the preparation slot")
			}
		})
	}
}

// A replayed id never dispatches, even after the first one was refused.
func TestAttachmentReplayedIDNeverDispatches(t *testing.T) {
	r := testRuntime(t)
	r.state = StateIdle
	r.cfg.Model.InputImage = true
	root := t.TempDir()
	data := writeAttachmentPNG(t, root)
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	f, h := startBridgeRuntime(t, r)
	launched := false
	h.startInput = func(agent.UserInput, string, bool) (bool, string) {
		launched = true
		return true, ""
	}
	ref := attachmentImageRef("a.png", data)
	f.inject <- attachmentPrompt(r, "9", "/nope", ref)
	h.handle(nextRequest(t, r.bridge), false)
	if ack := attachmentAck(t, f, "9"); ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED {
		t.Fatalf("first ack = %v", ack.GetOutcome())
	}
	f.inject <- attachmentPrompt(r, "9", "look", ref)
	h.handle(nextRequest(t, r.bridge), false)
	if ack := nthAttachmentAck(t, f, "9", 2); ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != "unavailable" {
		t.Fatalf("replayed ack = %v %q, want REFUSED unavailable", ack.GetOutcome(), ack.GetReason())
	}
	if launched {
		t.Fatal("replayed id dispatched")
	}
	if countAttachmentAcks(f, "9") != 2 {
		t.Fatalf("acks = %d, want two refusals and no dispatch", countAttachmentAcks(f, "9"))
	}
}

// A preparation that misses its budget refuses once, keeps the slot until the
// worker returns, and never starts the late result. Lifecycle traffic still
// runs while the slot is held.
func TestAttachmentExpiryRefusesOnceAndDiscardsLateResult(t *testing.T) {
	r := testRuntime(t)
	r.state = StateIdle
	r.cfg.Model.InputImage = true
	root := t.TempDir()
	data := writeAttachmentPNG(t, root)
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	f, h := startBridgeRuntime(t, r)
	launched := false
	h.startInput = func(agent.UserInput, string, bool) (bool, string) {
		launched = true
		return true, ""
	}
	f.inject <- attachmentPrompt(r, "11", "look", attachmentImageRef("a.png", data))
	h.handle(nextRequest(t, r.bridge), false)
	if h.prep == nil {
		t.Fatal("admitted attachment did not occupy the preparation slot")
	}

	h.expireAttachment()
	if h.prep == nil {
		t.Fatal("timeout released the slot before the worker returned")
	}
	ack := attachmentAck(t, f, "11")
	if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != "unavailable" {
		t.Fatalf("timeout ack = %v %q, want REFUSED unavailable", ack.GetOutcome(), ack.GetReason())
	}

	// State reads do not wait for the blocked preparation.
	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_StateRequest{StateRequest: &pibridgepb.StateRequest{RequestId: "state-1"}}}
	h.handle(nextRequest(t, r.bridge), false)
	f.waitFrame(t, "state response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetStateSnapshot() != nil
	})

	waitPreparation(t, h)
	h.finishPreparation(false)
	if h.prep != nil {
		t.Fatal("completed preparation did not release the slot")
	}
	if launched {
		t.Fatal("late preparation result started a run")
	}
	time.Sleep(50 * time.Millisecond)
	if got := countAttachmentAcks(f, "11"); got != 1 {
		t.Fatalf("acks = %d, want exactly the timeout refusal", got)
	}
}

// A running turn accepts an image as a steer message: no second run starts,
// the image stays structured, and the context revision moves once.
func TestAttachmentSteersIntoTheRunningTurn(t *testing.T) {
	r := testRuntime(t)
	r.cfg.Model.InputImage = true
	root := t.TempDir()
	data := writeAttachmentPNG(t, root)
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	f, h := startBridgeRuntime(t, r)
	r.state = StateStreaming
	loop := agent.New(r.store, r.reg, r.prov, nil)
	h.activeLoop = loop
	launched := false
	h.startInput = func(agent.UserInput, string, bool) (bool, string) {
		launched = true
		return true, ""
	}
	before := r.contextRev
	// Earlier steering must stay ahead of the image in FIFO order.
	loop.Steer("earlier text")

	f.inject <- attachmentPrompt(r, "13", "look", attachmentImageRef("a.png", data))
	h.handle(nextRequest(t, r.bridge), true)
	if h.prep == nil {
		t.Fatal("steering attachment did not occupy the preparation slot")
	}
	waitPreparation(t, h)
	h.finishPreparation(true)

	ack := attachmentAck(t, f, "13")
	if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_DISPATCHED || ack.GetReason() != "" {
		t.Fatalf("ack = %v %q, want DISPATCHED", ack.GetOutcome(), ack.GetReason())
	}
	if launched {
		t.Fatal("steering attachment started a second run")
	}
	if got := loop.PendingContentBytes(); got != uint64(len(data)) {
		t.Fatalf("pending bytes = %d, want %d", got, len(data))
	}
	if got := r.contextRev; got != before+1 {
		t.Fatalf("context revision = %d, want %d", got, before+1)
	}
	taken := loop.TakeSteering()
	if len(taken) != 2 || taken[0].Display != "earlier text" || !taken[1].Structured() {
		t.Fatalf("steering = %+v, want earlier text then one structured input", taken)
	}
	blocks, ok := taken[1].Content.([]session.UserContentBlock)
	if !ok || len(blocks) != 2 || blocks[1].Image == nil || blocks[1].Image.MimeType != "image/png" {
		t.Fatalf("steering content = %#v, want prompt text and one image", taken[1].Content)
	}
}

// A busy ordinary file steers as a path block, and a cancelling turn refuses.
func TestAttachmentBusyFileSteersAndCancellingRefuses(t *testing.T) {
	r := testRuntime(t)
	root := t.TempDir()
	content := []byte("ordinary bytes")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	f, h := startBridgeRuntime(t, r)
	r.state = StateStreaming
	loop := agent.New(r.store, r.reg, r.prov, nil)
	h.activeLoop = loop
	h.startInput = func(agent.UserInput, string, bool) (bool, string) {
		t.Fatal("busy attachment used the idle start path")
		return false, ""
	}

	f.inject <- attachmentPrompt(r, "15", "", attachmentFileRef("notes.txt", content))
	h.handle(nextRequest(t, r.bridge), true)
	waitPreparation(t, h)
	h.finishPreparation(true)
	if ack := attachmentAck(t, f, "15"); ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_DISPATCHED {
		t.Fatalf("ack = %v %q", ack.GetOutcome(), ack.GetReason())
	}
	taken := loop.TakeSteering()
	if len(taken) != 1 {
		t.Fatalf("steering = %+v", taken)
	}
	blocks, ok := taken[0].Content.([]session.UserContentBlock)
	if !ok || len(blocks) != 1 || blocks[0].Text == nil || !strings.Contains(blocks[0].Text.Text, "Attached file path (JSON-quoted): ") {
		t.Fatalf("file steering content = %#v, want one path block", taken[0].Content)
	}
	if got := loop.PendingContentBytes(); got != 0 {
		t.Fatalf("file steering counted %d image bytes", got)
	}

	r.state = StateCancelling
	f.inject <- attachmentPrompt(r, "16", "", attachmentFileRef("notes.txt", content))
	h.handle(nextRequest(t, r.bridge), true)
	if ack := attachmentAck(t, f, "16"); ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != "busy" {
		t.Fatalf("cancelling ack = %v %q, want REFUSED busy", ack.GetOutcome(), ack.GetReason())
	}
}

// A root removed after Hello refuses that prompt and leaves the rest of the
// bridge working. Psi can only learn about the loss when it checks again.
func TestAttachmentRootLossDegradesOnlyAttachments(t *testing.T) {
	r := testRuntime(t)
	r.cfg.Model.InputImage = true
	root := t.TempDir()
	data := writeAttachmentPNG(t, root)
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	f, h := startBridgeRuntime(t, r)
	h.startInput = func(agent.UserInput, string, bool) (bool, string) {
		t.Fatal("root loss launched a run")
		return false, ""
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	f.inject <- attachmentPrompt(r, "31", "look", attachmentImageRef("a.png", data))
	h.handle(nextRequest(t, r.bridge), false)
	if h.prep == nil {
		t.Fatal("root check did not run")
	}
	waitPreparation(t, h)
	h.finishPreparation(false)
	ack := attachmentAck(t, f, "31")
	if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != "unavailable" {
		t.Fatalf("ack = %v %q, want REFUSED unavailable", ack.GetOutcome(), ack.GetReason())
	}

	// The rest of the bridge is unaffected by the lost root.
	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_StateRequest{StateRequest: &pibridgepb.StateRequest{RequestId: "state-2"}}}
	h.handle(nextRequest(t, r.bridge), false)
	f.waitFrame(t, "state response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetStateSnapshot() != nil
	})
}

// Readiness exists only for a bridge with a valid root, and image availability
// is present only when the live model is known.
func TestAttachmentReadinessFollowsRootAndModel(t *testing.T) {
	r := testRuntime(t)
	if snap := bridgeSnapshot(r); snap.HasAttachment {
		t.Fatal("readiness published without an attachment root")
	}
	root := t.TempDir()
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	startBridgeRuntime(t, r)

	snap := bridgeSnapshot(r)
	if !snap.HasAttachment {
		t.Fatal("readiness missing with a valid root")
	}
	if !snap.Attachment.HasImageAvailable || snap.Attachment.ImageAvailable {
		t.Fatalf("image availability = %v/%v, want a present false", snap.Attachment.HasImageAvailable, snap.Attachment.ImageAvailable)
	}
	if !snap.Attachment.Idle {
		t.Fatalf("idle = false in an idle runtime: %+v", snap.Attachment)
	}

	r.cfg.Model.InputImage = true
	if snap := bridgeSnapshot(r); !snap.Attachment.ImageAvailable {
		t.Fatal("image availability stayed false for an image model")
	}

	r.compactPending = true
	snap = bridgeSnapshot(r)
	if snap.Attachment.Idle || !snap.Attachment.BridgePending {
		t.Fatalf("pending compaction not reflected: %+v", snap.Attachment)
	}

	r.cfg.Model.ID = ""
	if snap := bridgeSnapshot(r); snap.Attachment.HasImageAvailable {
		t.Fatal("image availability published without a known model")
	}
}

// Dispatch rechecks every fence the worker could not observe. The worker
// already succeeded here, so each refusal proves the second check fired.
func TestAttachmentDispatchRechecksIdentityAndRoot(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*runtime, string)
		reason string
	}{
		{name: "generation moved", mutate: func(r *runtime, _ string) { r.generation++ }, reason: "stale"},
		{name: "root removed", mutate: func(_ *runtime, root string) {
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
		}, reason: "unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRuntime(t)
			r.state = StateIdle
			r.cfg.Model.InputImage = true
			root := t.TempDir()
			data := writeAttachmentPNG(t, root)
			t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
			f, h := startBridgeRuntime(t, r)
			h.startInput = func(agent.UserInput, string, bool) (bool, string) {
				t.Fatal("recheck refusal launched a run")
				return false, ""
			}

			f.inject <- attachmentPrompt(r, "41", "look", attachmentImageRef("a.png", data))
			h.handle(nextRequest(t, r.bridge), false)
			if h.prep == nil {
				t.Fatal("admitted attachment did not occupy the preparation slot")
			}
			waitPreparation(t, h)
			tc.mutate(r, root)
			h.finishPreparation(false)

			ack := attachmentAck(t, f, "41")
			if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != tc.reason {
				t.Fatalf("ack = %v %q, want REFUSED %q", ack.GetOutcome(), ack.GetReason(), tc.reason)
			}
		})
	}
}

// A run that ends while preparation is in flight turns the prompt back into
// an idle launch: verified content is neither lost nor forced into steering.
func TestAttachmentPrepOutlivingRunStartsNewRun(t *testing.T) {
	r := testRuntime(t)
	r.cfg.Model.InputImage = true
	root := t.TempDir()
	data := writeAttachmentPNG(t, root)
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	f, h := startBridgeRuntime(t, r)
	r.state = StateStreaming
	loop := agent.New(r.store, r.reg, r.prov, nil)
	h.activeLoop = loop
	var launched agent.UserInput
	launches := 0
	h.startInput = func(input agent.UserInput, _ string, _ bool) (bool, string) {
		launched, launches = input, launches+1
		return true, ""
	}

	f.inject <- attachmentPrompt(r, "43", "look", attachmentImageRef("a.png", data))
	h.handle(nextRequest(t, r.bridge), true)
	if h.prep == nil {
		t.Fatal("steering attachment did not occupy the preparation slot")
	}
	waitPreparation(t, h)
	// The run settled before dispatch: idle state, no live loop.
	r.state = StateIdle
	h.activeLoop = nil
	h.finishPreparation(false)

	ack := attachmentAck(t, f, "43")
	if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_DISPATCHED {
		t.Fatalf("ack = %v %q, want DISPATCHED", ack.GetOutcome(), ack.GetReason())
	}
	if launches != 1 {
		t.Fatalf("launches = %d, want 1", launches)
	}
	if !launched.Structured() || launched.ImageBytes != uint64(len(data)) {
		t.Fatalf("launched = %+v, want the verified structured prompt", launched)
	}
	if got := loop.PendingSteering(); len(got) != 0 {
		t.Fatalf("steering = %q, want nothing steered into the settled run", got)
	}
}
