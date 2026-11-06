//go:build !windows

package app

import (
	"strings"
	"testing"

	"psi/internal/agent"
	pibridgepb "psi/internal/pibridge/pb"
	"psi/internal/session"
)

func recoveryInput(bytes uint64) agent.UserInput {
	return agent.UserInput{
		Content: []session.UserContentBlock{{Type: "image", Image: &session.ImageContent{
			Type: "image", MimeType: "image/png", Data: "aGVsbG8=",
		}}},
		Display:    "Attached: recovered.png",
		ImageBytes: bytes,
	}
}

// A huge declared ref needs no payload: admission reads only metadata.
func declaredImageRef(name string, size uint64) *pibridgepb.AttachmentRef {
	return &pibridgepb.AttachmentRef{
		Basename: name, SizeBytes: size, MimeType: "image/png",
		Kind: "image", Sha256: strings.Repeat("a", 64),
	}
}

// A stopped run keeps structured steering as structured recovery, while plain
// text still takes the editor path.
func TestTakeSteeringSplitsStructuredRecoveryFromText(t *testing.T) {
	r := testRuntime(t)
	loop := agent.New(r.store, r.reg, r.prov, nil)
	loop.Steer("plain text")
	loop.SteerContent(recoveryInput(1024))

	texts := r.takeSteering(loop)
	if len(texts) != 1 || texts[0] != "plain text" {
		t.Fatalf("editor texts = %q, want only the plain message", texts)
	}
	if len(r.recovery) != 1 || !r.recovery[0].Structured() {
		t.Fatalf("recovery = %+v, want one structured input", r.recovery)
	}
	if got := loop.PendingContentBytes(); got != 0 {
		t.Fatalf("queued bytes = %d, want the loop to release recovery's bytes", got)
	}
	if got := r.recoveryBytes(); got != 1024 {
		t.Fatalf("recovery bytes = %d, want 1024", got)
	}
}

// Session replacement drops undelivered prompts and says so.
func TestClearRecoveryNotifiesAndEmptiesTheBatch(t *testing.T) {
	r := testRuntime(t)
	r.recovery = []agent.UserInput{recoveryInput(7), recoveryInput(9)}
	var notice string
	r.recoveryNotify = func(text string) { notice = text }
	r.clearRecovery()
	if len(r.recovery) != 0 {
		t.Fatalf("recovery = %+v, want it cleared", r.recovery)
	}
	if !strings.Contains(notice, "2 undelivered attachment prompt(s)") {
		t.Fatalf("notice = %q, want the discard count", notice)
	}
	if got := r.recoveryBytes(); got != 0 {
		t.Fatalf("recovery bytes = %d, want 0", got)
	}
}

// Recovery counts toward the 50 MiB budget: an exact fit is not a size
// refusal, one byte more is. Both answers come from metadata alone.
func TestAttachmentBudgetSpansPreparationAndRecovery(t *testing.T) {
	r := testRuntime(t)
	r.cfg.Model.InputImage = true
	root := t.TempDir()
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	f, h := startBridgeRuntime(t, r)
	h.startInput = func(agent.UserInput, string, bool) (bool, string) {
		t.Fatal("budget test launched a run")
		return false, ""
	}
	r.recovery = []agent.UserInput{recoveryInput(20 << 20)}

	// Twenty recovered mebibytes plus thirty declared ones is exactly the
	// budget, so the refusal is the recovery gate, not the size limit.
	f.inject <- attachmentPrompt(r, "21", "", declaredImageRef("a.png", 25<<20), declaredImageRef("b.png", 5<<20))
	h.handle(nextRequest(t, r.bridge), false)
	ack := attachmentAck(t, f, "21")
	if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != "queued" {
		t.Fatalf("exact budget ack = %v %q, want REFUSED queued", ack.GetOutcome(), ack.GetReason())
	}

	// One byte more refuses before any payload read.
	f.inject <- attachmentPrompt(r, "22", "", declaredImageRef("a.png", 25<<20), declaredImageRef("b.png", 5<<20+1))
	h.handle(nextRequest(t, r.bridge), false)
	ack = attachmentAck(t, f, "22")
	if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != "limit" {
		t.Fatalf("over-budget ack = %v %q, want REFUSED limit", ack.GetOutcome(), ack.GetReason())
	}
	if h.prep != nil {
		t.Fatal("over-budget prompt started preparation")
	}
	if got := h.attachmentBytes(); got != 20<<20 {
		t.Fatalf("bytes after refusals = %d, want only recovery's 20 MiB", got)
	}

	// The initial prompt waiting for RunStart is another owner of the same
	// budget, so it must be summed, not counted twice or dropped.
	r.inFlight = &agent.UserInput{Display: "in flight", ImageBytes: 5 << 20}
	if got := h.attachmentBytes(); got != 25<<20 {
		t.Fatalf("bytes with an in-flight prompt = %d, want 25 MiB", got)
	}
}

// An occupied recovery batch refuses new attachments with queued.
func TestAttachmentRefusesWhileRecoveryIsOccupied(t *testing.T) {
	r := testRuntime(t)
	r.cfg.Model.InputImage = true
	root := t.TempDir()
	data := writeAttachmentPNG(t, root)
	t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)
	f, h := startBridgeRuntime(t, r)
	h.startInput = func(agent.UserInput, string, bool) (bool, string) {
		t.Fatal("recovery-occupied refusal launched a run")
		return false, ""
	}
	r.recovery = []agent.UserInput{recoveryInput(8)}

	f.inject <- attachmentPrompt(r, "23", "look", attachmentImageRef("a.png", data))
	h.handle(nextRequest(t, r.bridge), false)
	ack := attachmentAck(t, f, "23")
	if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != "queued" {
		t.Fatalf("ack = %v %q, want REFUSED queued", ack.GetOutcome(), ack.GetReason())
	}
}

// A cancelled run's steered image is recovered, not flattened or replayed.
func TestCancelledSteeringBecomesRecoveryWithoutReplay(t *testing.T) {
	r := testRuntime(t)
	loop := agent.New(r.store, r.reg, r.prov, nil)
	loop.SteerContent(recoveryInput(512))
	texts := r.takeSteering(loop)
	if len(texts) != 0 {
		t.Fatalf("editor texts = %q, want the image kept out of the editor", texts)
	}
	if len(r.recovery) != 1 {
		t.Fatalf("recovery = %+v, want the steered image", r.recovery)
	}
	if _, err := r.store.ActivePath(); err != nil {
		t.Fatal(err)
	}
	path, err := r.store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range path {
		if entry.Kind == "message" && entry.Message.Message.User != nil {
			if _, ok := entry.Message.Message.User.Content.([]session.UserContentBlock); ok {
				t.Fatal("cancelled steering was persisted")
			}
		}
	}
	if got := loop.PendingSteering(); len(got) != 0 {
		t.Fatalf("steering = %q, want it drained into recovery", got)
	}
}
