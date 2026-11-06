package pibridge

import (
	"context"
	"sync"

	"google.golang.org/protobuf/proto"

	"psi/internal/attachments"
	pibridgepb "psi/internal/pibridge/pb"
)

// Optional replies are bounded independently of lifecycle traffic: edges are
// never queued here, so a full read queue can delay them but never evict one.
const (
	optionalReplySlots = 14
	optionalReplyBytes = 240 * 1024
)

// Request kinds the host may receive. Each maps to one negotiated
// capability, so an unknown or unnegotiated payload is never forwarded.
const (
	RequestState       = "state"
	RequestModels      = "models"
	RequestSetModel    = "set-model"
	RequestThinking    = "thinking-levels"
	RequestSetThinking = "set-thinking"
	RequestHistory     = "history"
	RequestSkills      = "skills"
	RequestInvokeSkill = "invoke-skill"
	RequestTranscript  = "transcript-search"
	RequestCompact     = "compact"
	RequestReload      = "reload"
	RequestAttachment  = "attachments"
)

// ModelRef is one selectable catalog entry, named the way snapshots name it.
type ModelRef struct {
	Provider string
	Model    string
}

// SkillRef is one advertised skill: metadata only, never the body.
type SkillRef struct {
	Name        string
	Description string
}

// outbox holds replies until the stream goroutine writes them. Enqueueing
// never blocks and never touches the network: the host loop must stay
// responsive even when Phi stops reading.
type outbox struct {
	mu     sync.Mutex
	ch     chan *pibridgepb.PiToPhi
	bytes  int
	closed bool
}

func newOutbox() *outbox {
	return &outbox{ch: make(chan *pibridgepb.PiToPhi, optionalReplySlots)}
}

func (o *outbox) push(msg *pibridgepb.PiToPhi) bool {
	size := proto.Size(msg)
	if size > optionalReplyBytes {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.bytes+size > optionalReplyBytes {
		return false
	}
	select {
	case o.ch <- msg:
		o.bytes += size
		return true
	default:
		return false
	}
}

func (o *outbox) sent(msg *pibridgepb.PiToPhi) {
	size := proto.Size(msg)
	o.mu.Lock()
	o.bytes -= size
	o.mu.Unlock()
}

func (o *outbox) close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
}

// Request is one capability request forwarded from the stream to the host.
// A request is only valid for the stream that carried it: its context is
// cancelled when that stream ends, and replies enqueue nothing afterwards.
type Request struct {
	client *Client
	kind   string
	ctx    context.Context
	out    *outbox

	paneID, processID string
	generation        uint64

	requestID       string
	provider, model string
	thinkingLevel   string
	name            string

	query      string
	sessionID  string
	anchorID   string
	startIndex int
	initial    bool

	compactSession string
	compactLeaf    string
	compactModel   ModelRef
	hasModel       bool

	reloadSession    string
	reloadLeaf       string
	reloadModel      ModelRef
	hasReloadModel   bool
	reloadRunID      *uint64
	reloadContextRev *uint64

	attachSession  string
	attachLeaf     string
	attachModel    ModelRef
	hasAttachModel bool
	attachText     string
	attachRefs     []attachments.Ref
}

// Kind names the request. Unrecognized kinds are never forwarded.
func (r Request) Kind() string { return r.kind }

// RequestID is the correlating id Phi echoes back.
func (r Request) RequestID() string { return r.requestID }

// Context is cancelled when the originating stream ends.
func (r Request) Context() context.Context { return r.ctx }

// Provider and Model name the requested catalog entry for a switch.
func (r Request) Provider() string { return r.provider }

// Model names the requested catalog entry for a switch.
func (r Request) Model() string { return r.model }

// Level names the requested thinking level.
func (r Request) Level() string { return r.thinkingLevel }

// Name is the requested skill name.
func (r Request) Name() string { return r.name }

// Query is the search text. It is matched literally, never as a pattern.
func (r Request) Query() string { return r.query }

// SearchAnchor is the branch the caller pinned, empty on an initial request.
func (r Request) SearchAnchor() string { return r.anchorID }

// SearchSession names the session the caller expects to search.
func (r Request) SearchSession() string { return r.sessionID }

// SearchStart resumes at the next uninspected branch entry.
func (r Request) SearchStart() int { return r.startIndex }

// SearchInitial reports whether the caller is starting a new search.
func (r Request) SearchInitial() bool { return r.initial }

// TranscriptMatch is one visible-text hit.
type TranscriptMatch struct {
	EntryID string
	Role    string
	Snippet string
}

// Expired reports whether the originating stream already ended.
func (r Request) Expired() bool { return r.ctx.Err() != nil }

// Generation is the runtime generation the originating stream bound.
func (r Request) Generation() uint64 { return r.generation }

// send builds one reply envelope and enqueues it. The payload stays a
// generated oneof wrapper: this package never invents its own wire types.
func (r Request) send(build func(*pibridgepb.PiToPhi)) bool {
	if r.client == nil || r.out == nil || r.ctx.Err() != nil {
		return false
	}
	msg := &pibridgepb.PiToPhi{
		PaneId:            r.paneID,
		ProcessId:         r.processID,
		RuntimeGeneration: r.generation,
	}
	build(msg)
	return r.out.push(msg)
}

// ReplyState answers a state refresh from the client's current snapshot.
func (r Request) ReplyState() bool {
	if r.client == nil {
		return false
	}
	r.client.mu.Lock()
	snap := r.client.snapshotLocked(r.requestID)
	r.client.mu.Unlock()
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_StateSnapshot{StateSnapshot: snap}
	})
}

// ReplyModels answers a catalog read. A list that does not fit the message
// bound must answer unavailable instead: entries are never trimmed silently.
func (r Request) ReplyModels(models []ModelRef, truncated bool) bool {
	refs := make([]*pibridgepb.ModelRef, 0, len(models))
	for _, m := range models {
		refs = append(refs, &pibridgepb.ModelRef{Provider: m.Provider, Model: m.Model})
	}
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_ModelsResponse{ModelsResponse: &pibridgepb.ModelsResponse{
			RequestId: r.requestID,
			Models:    refs,
			Truncated: truncated,
		}}
	})
}

// ReplyModelsUnavailable answers a catalog read Phi must fall back from.
func (r Request) ReplyModelsUnavailable() bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_ModelsResponse{ModelsResponse: &pibridgepb.ModelsResponse{
			RequestId:   r.requestID,
			Unavailable: true,
		}}
	})
}

// ReplySetModel answers a switch. An empty reason with ok=false means the
// attempt errored and its outcome is unknown; callers must not retry it.
func (r Request) ReplySetModel(ok bool, reason string) bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_SetModelAck{SetModelAck: &pibridgepb.SetModelAck{
			RequestId: r.requestID,
			Ok:        ok,
			Reason:    reason,
		}}
	})
}

// ReplyThinkingLevels answers a thinking read with the live model's levels.
func (r Request) ReplyThinkingLevels(levels []string, provider, model string) bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_ThinkingLevelsResponse{ThinkingLevelsResponse: &pibridgepb.ThinkingLevelsResponse{
			RequestId: r.requestID,
			Levels:    levels,
			Provider:  provider,
			Model:     model,
		}}
	})
}

// ReplyThinkingUnavailable answers a thinking read Phi must fall back from.
func (r Request) ReplyThinkingUnavailable() bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_ThinkingLevelsResponse{ThinkingLevelsResponse: &pibridgepb.ThinkingLevelsResponse{
			RequestId:   r.requestID,
			Unavailable: true,
		}}
	})
}

// ReplySetThinking answers a level change. An empty reason with ok=false
// means the outcome is unknown, exactly like a model switch.
func (r Request) ReplySetThinking(ok bool, reason string) bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_SetThinkingAck{SetThinkingAck: &pibridgepb.SetThinkingAck{
			RequestId: r.requestID,
			Ok:        ok,
			Reason:    reason,
		}}
	})
}

// ReplyHistory answers a live recall read. Entries are newest-first; an
// answer beyond the message bound is wholly unavailable, never trimmed.
func (r Request) ReplyHistory(entries []string) bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_HistoryResponse{HistoryResponse: &pibridgepb.HistoryResponse{
			RequestId: r.requestID,
			Entries:   entries,
		}}
	})
}

// ReplyHistoryUnavailable answers a recall read Phi must fall back from.
func (r Request) ReplyHistoryUnavailable() bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_HistoryResponse{HistoryResponse: &pibridgepb.HistoryResponse{
			RequestId:   r.requestID,
			Unavailable: true,
		}}
	})
}

// ReplySkills answers a skill list. Entries are metadata only.
func (r Request) ReplySkills(skills []SkillRef, truncated bool) bool {
	entries := make([]*pibridgepb.SkillRef, 0, len(skills))
	for _, s := range skills {
		entries = append(entries, &pibridgepb.SkillRef{Name: s.Name, Description: s.Description})
	}
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_SkillsResponse{SkillsResponse: &pibridgepb.SkillsResponse{
			RequestId: r.requestID,
			Skills:    entries,
			Truncated: truncated,
		}}
	})
}

// ReplySkillsUnavailable answers a skill list Phi must fall back from.
func (r Request) ReplySkillsUnavailable() bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_SkillsResponse{SkillsResponse: &pibridgepb.SkillsResponse{
			RequestId:   r.requestID,
			Unavailable: true,
		}}
	})
}

// ReplyInvokeSkill answers a dispatch. ok means the input reached the host
// queue, never that the model received or completed it.
func (r Request) ReplyInvokeSkill(ok bool, reason string) bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_InvokeSkillAck{InvokeSkillAck: &pibridgepb.InvokeSkillAck{
			RequestId: r.requestID,
			Ok:        ok,
			Reason:    reason,
		}}
	})
}

// CompactExpect is the identity a compact request was admitted against.
type CompactExpect struct {
	Session  string
	Leaf     string
	Model    ModelRef
	HasModel bool
}

// CompactExpectation returns the identity the caller pinned.
func (r Request) CompactExpectation() CompactExpect {
	return CompactExpect{Session: r.compactSession, Leaf: r.compactLeaf, Model: r.compactModel, HasModel: r.hasModel}
}

// CompactOutcome mirrors the wire enum. Zero means genuinely unknown: never
// guess success, failure, or cancellation from absent evidence.
type CompactOutcome int

const (
	CompactUnknown CompactOutcome = iota
	CompactCompleted
	CompactRefused
	CompactFailed
	CompactCancelled
)

// ReplyCompact answers one compaction with its terminal outcome.
func (r Request) ReplyCompact(outcome CompactOutcome, reason string) bool {
	wire := pibridgepb.CompactOutcome_COMPACT_OUTCOME_UNSPECIFIED
	switch outcome {
	case CompactCompleted:
		wire = pibridgepb.CompactOutcome_COMPACT_OUTCOME_COMPLETED
	case CompactRefused:
		wire = pibridgepb.CompactOutcome_COMPACT_OUTCOME_REFUSED
	case CompactFailed:
		wire = pibridgepb.CompactOutcome_COMPACT_OUTCOME_FAILED
	case CompactCancelled:
		wire = pibridgepb.CompactOutcome_COMPACT_OUTCOME_CANCELLED
	}
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_CompactResult{CompactResult: &pibridgepb.CompactResult{
			RequestId: r.requestID,
			Outcome:   wire,
			Reason:    reason,
		}}
	})
}

// ReplyTranscript answers one search page. Complete means the captured branch
// was inspected to its end; it never means nothing else matched.
func (r Request) ReplyTranscript(sessionID, anchorID string, nextIndex int, matches []TranscriptMatch, complete bool) bool {
	refs := make([]*pibridgepb.TranscriptSearchMatch, 0, len(matches))
	for _, match := range matches {
		refs = append(refs, &pibridgepb.TranscriptSearchMatch{
			EntryId: match.EntryID,
			Role:    match.Role,
			Snippet: match.Snippet,
		})
	}
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_TranscriptSearchResponse{TranscriptSearchResponse: &pibridgepb.TranscriptSearchResponse{
			RequestId:    r.requestID,
			SessionId:    sessionID,
			AnchorLeafId: anchorID,
			NextIndex:    uint32(nextIndex),
			Matches:      refs,
			Complete:     complete,
		}}
	})
}

// ReplyTranscriptUnavailable reports a page that cannot be answered safely.
// Phi keeps loaded-terminal search in that case.
func (r Request) ReplyTranscriptUnavailable() bool {
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_TranscriptSearchResponse{TranscriptSearchResponse: &pibridgepb.TranscriptSearchResponse{
			RequestId:   r.requestID,
			Unavailable: true,
		}}
	})
}

// AttachmentExpect is the identity one attachment prompt was admitted
// against. Model presence is significant: an absent expectation matches only
// an absent live model.
type AttachmentExpect struct {
	Session  string
	Leaf     string
	Model    ModelRef
	HasModel bool
	Text     string
	Refs     []attachments.Ref
}

// AttachmentExpectation returns the identity the caller pinned. The refs are
// copied so later host mutation cannot change what was validated.
func (r Request) AttachmentExpectation() AttachmentExpect {
	return AttachmentExpect{
		Session:  r.attachSession,
		Leaf:     r.attachLeaf,
		Model:    r.attachModel,
		HasModel: r.hasAttachModel,
		Text:     r.attachText,
		Refs:     append([]attachments.Ref(nil), r.attachRefs...),
	}
}

// AttachmentRoot is the checked handoff root this client retained, nil when
// the capability was not offered or the pane is shutting down.
func (r Request) AttachmentRoot() *attachments.Root {
	if r.client == nil {
		return nil
	}
	return r.client.AttachmentRoot()
}

// AttachmentOutcome mirrors the wire enum.
type AttachmentOutcome int

const (
	AttachmentRefused AttachmentOutcome = iota
	AttachmentDispatched
	AttachmentUnknown
)

// ReplyAttachment answers one attachment prompt. DISPATCHED reports admission
// only, never model receipt. Refusal reasons come from Phi's fixed vocabulary;
// anything else is coerced so Phi never discards the ack.
func (r Request) ReplyAttachment(outcome AttachmentOutcome, reason string) bool {
	wire := pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED
	switch outcome {
	case AttachmentDispatched:
		wire = pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_DISPATCHED
	case AttachmentUnknown:
		wire = pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_UNKNOWN
	}
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_AttachmentPromptAck{AttachmentPromptAck: &pibridgepb.AttachmentPromptAck{
			RequestId: r.requestID,
			Outcome:   wire,
			Reason:    attachmentReason(outcome, reason),
		}}
	})
}

// attachmentReason keeps one ack inside the contract: DISPATCHED carries no
// reason, UNKNOWN is always uncertain, and REFUSED never invents a reason Phi
// rejects. "uncertain" is reserved for UNKNOWN.
func attachmentReason(outcome AttachmentOutcome, reason string) string {
	switch outcome {
	case AttachmentDispatched:
		return ""
	case AttachmentUnknown:
		return "uncertain"
	}
	switch reason {
	case "unavailable", "stale", "busy", "queued", "compaction-active",
		"pending", "command", "unsupported-image", "unsupported-model",
		"missing-item", "invalid-item", "limit", "storage", "error":
		return reason
	default:
		return "unavailable"
	}
}

// The shape stays capability-specific so Phi can fall back correctly.
func (r Request) ReplyUnavailable() bool {
	switch r.kind {
	case RequestModels:
		return r.ReplyModelsUnavailable()
	case RequestSetModel:
		return r.ReplySetModel(false, "")
	case RequestThinking:
		return r.ReplyThinkingUnavailable()
	case RequestSetThinking:
		return r.ReplySetThinking(false, "")
	case RequestHistory:
		return r.ReplyHistoryUnavailable()
	case RequestSkills:
		return r.ReplySkillsUnavailable()
	case RequestInvokeSkill:
		return r.ReplyInvokeSkill(false, "unavailable")
	case RequestTranscript:
		return r.ReplyTranscriptUnavailable()
	case RequestCompact:
		return r.ReplyCompact(CompactRefused, "unavailable")
	case RequestReload:
		return r.ReplyReload(false, "unavailable")
	case RequestAttachment:
		return r.ReplyAttachment(AttachmentRefused, "unavailable")
	default:
		return false
	}
}

// Requests yields capability requests for the host. A nil client yields a
// nil channel, which blocks forever in a select.
func (c *Client) Requests() <-chan Request {
	if c == nil {
		return nil
	}
	return c.requests
}

// dispatch turns one validated stream message into a request for the host.
// It reports whether the payload belongs to a known capability.
func (c *Client) dispatch(msg *pibridgepb.PhiToPi, ctx context.Context, out *outbox, gen uint64) (Request, bool) {
	req := Request{
		client:     c,
		ctx:        ctx,
		out:        out,
		paneID:     msg.GetPaneId(),
		processID:  msg.GetProcessId(),
		generation: gen,
	}
	switch payload := msg.Payload.(type) {
	case *pibridgepb.PhiToPi_StateRequest:
		req.kind, req.requestID = RequestState, payload.StateRequest.GetRequestId()
	case *pibridgepb.PhiToPi_ModelsRequest:
		req.kind, req.requestID = RequestModels, payload.ModelsRequest.GetRequestId()
	case *pibridgepb.PhiToPi_SetModelRequest:
		req.kind, req.requestID = RequestSetModel, payload.SetModelRequest.GetRequestId()
		req.provider, req.model = payload.SetModelRequest.GetProvider(), payload.SetModelRequest.GetModel()
	case *pibridgepb.PhiToPi_ThinkingLevelsRequest:
		req.kind, req.requestID = RequestThinking, payload.ThinkingLevelsRequest.GetRequestId()
	case *pibridgepb.PhiToPi_SetThinkingRequest:
		req.kind, req.requestID = RequestSetThinking, payload.SetThinkingRequest.GetRequestId()
		req.thinkingLevel = payload.SetThinkingRequest.GetLevel()
		req.provider, req.model = payload.SetThinkingRequest.GetProvider(), payload.SetThinkingRequest.GetModel()
	case *pibridgepb.PhiToPi_HistoryRequest:
		req.kind, req.requestID = RequestHistory, payload.HistoryRequest.GetRequestId()
	case *pibridgepb.PhiToPi_SkillsRequest:
		req.kind, req.requestID = RequestSkills, payload.SkillsRequest.GetRequestId()
	case *pibridgepb.PhiToPi_InvokeSkillRequest:
		req.kind, req.requestID = RequestInvokeSkill, payload.InvokeSkillRequest.GetRequestId()
		req.name = payload.InvokeSkillRequest.GetName()
	case *pibridgepb.PhiToPi_TranscriptSearchRequest:
		search := payload.TranscriptSearchRequest
		req.kind, req.requestID = RequestTranscript, search.GetRequestId()
		req.query, req.sessionID = search.GetQuery(), search.GetSessionId()
		req.anchorID, req.startIndex, req.initial = search.GetAnchorLeafId(), int(search.GetStartIndex()), search.GetInitial()
	case *pibridgepb.PhiToPi_CompactRequest:
		compact := payload.CompactRequest
		req.kind, req.requestID = RequestCompact, compact.GetRequestId()
		req.compactSession, req.compactLeaf = compact.GetExpectedSessionId(), compact.GetExpectedLeafId()
		if model := compact.GetExpectedModel(); model != nil {
			req.compactModel, req.hasModel = ModelRef{Provider: model.GetProvider(), Model: model.GetModel()}, true
		}
	case *pibridgepb.PhiToPi_ReloadRequest:
		reload := payload.ReloadRequest
		req.kind, req.requestID = RequestReload, reload.GetRequestId()
		req.reloadSession, req.reloadLeaf = reload.GetExpectedSessionId(), reload.GetExpectedLeafId()
		if model := reload.GetExpectedModel(); model != nil {
			req.reloadModel, req.hasReloadModel = ModelRef{Provider: model.GetProvider(), Model: model.GetModel()}, true
		}
		req.reloadRunID, req.reloadContextRev = reload.ExpectedRunId, reload.ExpectedContextRevision
	case *pibridgepb.PhiToPi_AttachmentPromptRequest:
		attach := payload.AttachmentPromptRequest
		req.kind, req.requestID = RequestAttachment, attach.GetRequestId()
		req.attachSession, req.attachLeaf = attach.GetExpectedSessionId(), attach.GetExpectedLeafId()
		if model := attach.GetExpectedModel(); model != nil {
			req.attachModel, req.hasAttachModel = ModelRef{Provider: model.GetProvider(), Model: model.GetModel()}, true
		}
		req.attachText = attach.GetText()
		for _, ref := range attach.GetAttachments() {
			req.attachRefs = append(req.attachRefs, attachments.Ref{
				Basename: ref.GetBasename(), SizeBytes: ref.GetSizeBytes(), MIMEType: ref.GetMimeType(),
				Kind: ref.GetKind(), SHA256: ref.GetSha256(),
			})
		}
	default:
		return Request{}, false
	}
	return req, true
}
