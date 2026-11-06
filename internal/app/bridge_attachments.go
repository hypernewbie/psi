package app

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"psi/internal/agent"
	"psi/internal/attachments"
	"psi/internal/pibridge"
)

// attachPrepareBudget bounds one preparation. Phi waits 20 s for the ack, so
// the host must answer before that; a timeout refuses once and keeps the slot
// until the worker returns.
const attachPrepareBudget = 10 * time.Second

// bridgeAttachPrep is the attachment state of the one preparation slot. The
// worker owns prepared/err; the app loop owns timedOut.
type bridgeAttach struct {
	expect   pibridge.AttachmentExpect
	root     *attachments.Root
	cancel   context.CancelFunc
	timeout  <-chan struct{}
	timedOut bool
	bytes    uint64
	prepared attachments.Prepared
	err      error
}

// startAttachment admits one attachment prompt. It refuses before touching any
// file when identity, readiness, bounds, or the model make dispatch impossible.
func (h *bridgeHandler) startAttachment(req pibridge.Request, runBusy bool) {
	id, ok := canonicalDecimal(req.RequestID())
	if !ok || id <= h.attachHigh {
		// A malformed or replayed id never dispatches. Phi's reason set has no
		// "duplicate", so an old id is reported as unavailable.
		req.ReplyAttachment(pibridge.AttachmentRefused, "unavailable")
		return
	}
	// Consume the high-water mark before any admission decision so a later
	// refusal cannot be replayed under the same id.
	h.attachHigh = id

	expect := req.AttachmentExpectation()
	hasImage, imageBytes, validateErr := attachments.Validate(expect.Text, expect.Refs)
	root := req.AttachmentRoot()
	busyReason := h.attachBusyReason(runBusy)
	switch {
	case validateErr != nil:
		req.ReplyAttachment(pibridge.AttachmentRefused, attachFailure(validateErr))
		return
	case req.Expired():
		req.ReplyAttachment(pibridge.AttachmentRefused, "unavailable")
		return
	case root == nil:
		req.ReplyAttachment(pibridge.AttachmentRefused, "unavailable")
		return
	case h.r.store == nil, h.r.selector != nil:
		req.ReplyAttachment(pibridge.AttachmentRefused, "unavailable")
		return
	case h.r.state == StateStorageBlocked || h.r.store.WriteError() != nil:
		req.ReplyAttachment(pibridge.AttachmentRefused, "storage")
		return
	case h.prep != nil:
		req.ReplyAttachment(pibridge.AttachmentRefused, "pending")
		return
	case expect.Session != h.r.store.Header().ID, expect.Leaf != h.r.store.LeafID():
		req.ReplyAttachment(pibridge.AttachmentRefused, "stale")
		return
	case req.Generation() != uint64(h.r.generation):
		req.ReplyAttachment(pibridge.AttachmentRefused, "stale")
		return
	case !h.attachModelMatches(expect):
		req.ReplyAttachment(pibridge.AttachmentRefused, "stale")
		return
	case hasImage && !h.r.cfg.Model.InputImage:
		req.ReplyAttachment(pibridge.AttachmentRefused, "unsupported-model")
		return
	case attachmentCommand(expect.Text):
		req.ReplyAttachment(pibridge.AttachmentRefused, "command")
		return
	case busyReason != "":
		req.ReplyAttachment(pibridge.AttachmentRefused, busyReason)
		return
	case h.r.compactActive:
		req.ReplyAttachment(pibridge.AttachmentRefused, "compaction-active")
		return
	case h.attachmentBytes()+imageBytes > attachments.MaxTotalBytes:
		// Refuse before the first payload read.
		req.ReplyAttachment(pibridge.AttachmentRefused, "limit")
		return
	case h.compact != nil, h.reload != nil, len(h.r.recovery) > 0:
		req.ReplyAttachment(pibridge.AttachmentRefused, "queued")
		return
	case !runBusy && h.queuedInputs() > 0:
		// A busy turn accepts more steering: FIFO order already keeps earlier
		// input ahead. An idle turn with stranded input is recovering it.
		req.ReplyAttachment(pibridge.AttachmentRefused, "queued")
		return
	case h.startInput == nil:
		req.ReplyAttachment(pibridge.AttachmentRefused, "unavailable")
		return
	}

	// The preparation follows the request's stream: a lost stream stops
	// the file reads immediately instead of burning the whole budget.
	ctx, cancel := context.WithTimeout(req.Context(), attachPrepareBudget)
	prep := &bridgePrep{
		request: req,
		kind:    pibridge.RequestAttachment,
		base:    h.r.ov,
		session: h.r.store.Header().ID,
		gen:     h.r.generation,
		done:    make(chan struct{}),
		attach: &bridgeAttach{
			expect:  expect,
			root:    root,
			cancel:  cancel,
			timeout: ctx.Done(),
			bytes:   imageBytes,
		},
	}
	h.prep = prep
	h.r.syncBridge()
	work := prep.attach
	go func() {
		defer close(prep.done)
		work.prepared, work.err = work.root.Prepare(ctx, expect.Text, expect.Refs)
	}()
}

// attachModelMatches enforces exact model agreement. An absent expectation
// matches only an absent live model.
func (h *bridgeHandler) attachModelMatches(expect pibridge.AttachmentExpect) bool {
	if expect.HasModel {
		return expect.Model.Provider == h.r.ov.Provider && expect.Model.Model == h.r.ov.Model
	}
	return h.r.ov.Provider == "" && h.r.ov.Model == ""
}

// attachBusyReason reports whether the live state cannot take this prompt at
// all. A normal running turn accepts steering through the current loop;
// cancellation, compaction, and every idle-state mismatch still refuse.
func (h *bridgeHandler) attachBusyReason(runBusy bool) string {
	if runBusy {
		switch h.r.state {
		case StateWaiting, StateStreaming, StateRunningTool:
			if h.activeLoop == nil {
				return "unavailable"
			}
			return ""
		default:
			return "busy"
		}
	}
	if h.r.state != StateIdle {
		return "busy"
	}
	return ""
}

// attachmentBytes sums decoded image bytes owned by live preparation, the
// current loop's queued steering, the initial in-flight prompt, and recovery.
// There is no second global counter: every owner is read on the app loop.
func (h *bridgeHandler) attachmentBytes() uint64 {
	var total uint64
	if h.prep != nil && h.prep.attach != nil {
		total += h.prep.attach.bytes
	}
	if h.activeLoop != nil {
		total += h.activeLoop.PendingContentBytes()
	}
	total += h.r.recoveryBytes()
	return total
}

// timeout yields the app-loop notification for an expired preparation. It is
// nil, and therefore inert, whenever no attachment preparation is live.
func (h *bridgeHandler) timeout() <-chan struct{} {
	if h == nil || h.prep == nil || h.prep.attach == nil {
		return nil
	}
	return h.prep.attach.timeout
}

// expireAttachment refuses a preparation that will not finish in budget. The
// slot stays installed: the worker owns it until it returns, and its late
// result is discarded without a second ack.
func (h *bridgeHandler) expireAttachment() {
	prep := h.prep
	if prep == nil || prep.attach == nil || prep.attach.timedOut {
		return
	}
	prep.attach.timedOut = true
	prep.attach.timeout = nil
	prep.request.ReplyAttachment(pibridge.AttachmentRefused, "unavailable")
	h.r.syncBridge()
}

// finishAttach resolves one prepared prompt on the loop, after rechecking
// every fence the worker could not observe.
func (h *bridgeHandler) finishAttach(prep *bridgePrep, runBusy bool) {
	attach := prep.attach
	if attach == nil {
		return
	}
	attach.cancel()
	attach.timeout = nil
	if attach.timedOut {
		// The refusal was already sent; never read worker fields or ack twice.
		h.r.syncBridge()
		return
	}
	if attach.err != nil {
		prep.request.ReplyAttachment(pibridge.AttachmentRefused, attachFailure(attach.err))
		h.r.syncBridge()
		return
	}
	expect := attach.expect
	busyReason := h.attachBusyReason(runBusy)
	reason := ""
	switch {
	case prep.request.Expired(), h.r.generation != prep.gen, h.r.store == nil:
		reason = "stale"
	case h.r.store.Header().ID != expect.Session:
		reason = "stale"
	case h.r.store.LeafID() != expect.Leaf:
		reason = "stale"
	case !h.attachModelMatches(expect):
		reason = "stale"
	case h.r.selector != nil || h.r.state == StateStorageBlocked:
		reason = "unavailable"
	case h.r.store.WriteError() != nil:
		reason = "storage"
	case busyReason != "":
		reason = busyReason
	case h.r.compactActive:
		reason = "compaction-active"
	case h.compact != nil, h.reload != nil, len(h.r.recovery) > 0:
		reason = "queued"
	case !runBusy && h.queuedInputs() > 0:
		reason = "queued"
	case attach.root.Check() != nil:
		// A renamed or replaced root would retarget a later ordinary-file
		// path; refuse instead of dispatching unverifiable content.
		reason = "unavailable"
	case h.startInput == nil:
		reason = "unavailable"
	}
	if reason != "" {
		prep.request.ReplyAttachment(pibridge.AttachmentRefused, reason)
		h.r.syncBridge()
		return
	}
	input := agent.UserInput{
		Content:    attach.prepared.Content,
		Display:    attach.prepared.Display,
		ImageBytes: attach.prepared.ImageBytes,
	}
	if runBusy {
		// A running turn accepts the prompt as a steer message, so it reaches
		// the next model request without starting a second run.
		loop := h.activeLoop
		if loop == nil {
			prep.request.ReplyAttachment(pibridge.AttachmentRefused, "unavailable")
			h.r.syncBridge()
			return
		}
		loop.SteerContent(input)
		h.r.bumpContextRev()
		prep.request.ReplyAttachment(pibridge.AttachmentDispatched, "")
		h.r.syncBridge()
		return
	}
	admitted, safeReason := h.startInput(input, "", false)
	if !admitted {
		prep.request.ReplyAttachment(pibridge.AttachmentRefused, safeReason)
		h.r.syncBridge()
		return
	}
	// DISPATCHED reports admission, never model receipt.
	prep.request.ReplyAttachment(pibridge.AttachmentDispatched, "")
	h.r.syncBridge()
}

// attachFailure maps a typed preparation error to Phi's fixed reason set.
func attachFailure(err error) string {
	switch {
	case errors.Is(err, attachments.ErrMissing):
		return "missing-item"
	case errors.Is(err, attachments.ErrUnsupportedImage):
		return "unsupported-image"
	case errors.Is(err, attachments.ErrLimit):
		return "limit"
	case errors.Is(err, attachments.ErrInvalid):
		return "invalid-item"
	case errors.Is(err, attachments.ErrUnavailable):
		return "unavailable"
	default:
		return "error"
	}
}

// attachmentCommand reports whether the prompt would be read as a native
// command. Leading Unicode whitespace is ignored, matching command parsing.
func attachmentCommand(text string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	return strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "!")
}
