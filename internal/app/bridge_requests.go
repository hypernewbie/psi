package app

import (
	"context"
	"errors"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/pibridge"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/skills"
	"psi/internal/terminal"
)

// bridgeModelLimit caps one catalog answer. Phi marks such a list truncated
// and keeps it usable, so the cap never hides the whole answer.
const bridgeModelLimit = 500

// bridgeHistoryBytes is the bridge's message bound. A longer recall answer is
// unavailable as a whole: half a history would silently lose entries.
const bridgeHistoryBytes = 256 * 1024

// Bridge skill bounds. Phi shows the list as-is, so an entry that does not fit
// makes the whole list unavailable instead of vanishing.
const (
	bridgeSkillLimit            = 200
	bridgeSkillNameBytes        = 64
	bridgeSkillDescriptionBytes = 1024
)

// Search bounds. The query matches Phi's own wire limit and the page budget
// stays below the on-demand HTTP timeout.
const (
	bridgeSearchWorkers    = 2
	bridgeSearchQueryBytes = 1024
	bridgeSearchBudget     = 1500 * time.Millisecond
)

// bridgePrep is one candidate configuration built off the loop. configure
// can wait seconds for the shared auth-file lease, so it must never run
// inside the TUI select.
type bridgePrep struct {
	request pibridge.Request
	kind    string

	cand *runtime
	next config.StartupOverrides
	// base is the configuration the request was admitted against. A native
	// change while the worker runs makes the candidate stale.
	base    config.StartupOverrides
	session string
	gen     int64
	done    chan struct{}
	err     error
	failure string
	// superseded marks a settings worker that lost to a newer arrival. It
	// finishes on its own but cannot install or answer again.
	superseded bool

	// work expands one skill off the loop. The same slot serves every
	// preparation job, so a second request waits instead of competing.
	work     func() (string, error)
	expanded string

	// attach is the attachment state of this same slot. It is nil for every
	// other kind.
	attach *bridgeAttach
}

// bridgeHandler serves capability requests on the TUI loop. It owns one
// preparation slot: a second request waits for the first to finish instead
// of starting a competing configuration.
type bridgeHandler struct {
	r       *runtime
	console *terminal.Console
	prep    *bridgePrep
	// replacement is at most one validated live-settings request waiting for
	// the running worker to release the slot. Later valid arrivals replace it.
	replacement *bridgeSettings

	// Loop-owned submission hook and the run currently in flight. They are
	// refreshed by the loop, never by a worker.
	start      func(text, display, focus, invocation, source string, compact bool)
	startInput func(input agent.UserInput, focus string, compact bool) (bool, string)
	activeLoop *agent.Loop

	// searchDone receives finished pages. searches counts live workers on the
	// loop only; two are enough because each page is already bounded.
	searchDone chan *bridgeSearch
	searches   int

	// Compact admission state. highWater rejects replay; compact is the one
	// unresolved operation this process owns, bound to the worker it started.
	compactHigh uint64
	compact     *bridgeCompact
	compactRun  func(focus string) (uint64, error)

	// Reload admission state. Reload runs synchronously on the loop, so the
	// latch only ever spans one handle call, but it is still published for
	// Phi's mutual exclusion and retired by readiness evidence.
	reload    *bridgeReload
	reloadRun func() error
	// reloadInterrupt claims the native interrupt slot for a busy run. It
	// returns the worker token it owns, or an existing refusal reason.
	reloadInterrupt func() (uint64, string)

	// Attachment admission state. attachHigh rejects replayed ids; the one
	// live operation is the shared preparation slot above.
	attachHigh uint64
}

// bridgeCompact is one accepted, unanswered compaction.
type bridgeCompact struct {
	id      uint64
	request pibridge.Request
	session string
	leaf    string
	// op is the local worker token that actually started for this receipt.
	op uint64
}

// bridgeReload is one admitted native reload. op is the TUI-local worker
// token that owns the interruption; zero means the idle synchronous path.
type bridgeReload struct {
	id      uint64
	request pibridge.Request
	op      uint64
}

// bridgeSettings is one validated live-settings request waiting for the
// running preparation to finish. It carries the identity it was admitted
// against, so promotion cannot land on a different stream or session.
type bridgeSettings struct {
	request pibridge.Request
	kind    string
	next    config.StartupOverrides
	base    config.StartupOverrides
	session string
	gen     int64
}

// bridgeSearch is one page read running outside the loop.
type bridgeSearch struct {
	request pibridge.Request
	page    session.SearchPage
	err     error
}

func newBridgeHandler(r *runtime, console *terminal.Console) *bridgeHandler {
	return &bridgeHandler{r: r, console: console, searchDone: make(chan *bridgeSearch, 2)}
}

// searchResults yields finished pages, or nil when no bridge was provisioned.
func (h *bridgeHandler) searchResults() <-chan *bridgeSearch {
	if h == nil || h.r == nil {
		return nil
	}
	return h.searchDone
}

// requests yields the client channel, or nil when no bridge was provisioned.
func (h *bridgeHandler) requests() <-chan pibridge.Request {
	if h == nil || h.r == nil {
		return nil
	}
	return h.r.bridge.Requests()
}

// completed yields a channel that fires when the current preparation worker
// returns. It is nil while no worker runs.
func (h *bridgeHandler) completed() <-chan struct{} {
	if h == nil || h.prep == nil {
		return nil
	}
	return h.prep.done
}

// busy reports whether a bridge mutation owns the runtime right now.
func (h *bridgeHandler) busy() bool {
	return h != nil && h.prep != nil
}

// handle dispatches one request. runBusy is the loop's current run state;
// live settings are admitted during owned work, other mutations are refused
// while it is true.
func (h *bridgeHandler) handle(req pibridge.Request, runBusy bool) {
	switch req.Kind() {
	case pibridge.RequestState:
		h.r.syncBridge()
		req.ReplyState()
	case pibridge.RequestModels:
		h.listModels(req)
	case pibridge.RequestSetModel:
		h.startModelSwitch(req, runBusy)
	case pibridge.RequestThinking:
		h.listThinking(req)
	case pibridge.RequestSetThinking:
		h.startThinkingSwitch(req, runBusy)
	case pibridge.RequestHistory:
		h.readHistory(req)
	case pibridge.RequestSkills:
		h.listSkills(req)
	case pibridge.RequestInvokeSkill:
		h.invokeSkill(req, runBusy)
	case pibridge.RequestTranscript:
		h.startSearch(req)
	case pibridge.RequestCompact:
		h.startCompact(req, runBusy)
	case pibridge.RequestReload:
		h.startReload(req, runBusy)
	case pibridge.RequestAttachment:
		h.startAttachment(req, runBusy)
	default:
		req.ReplyUnavailable()
	}
}

// listModels answers from the loaded catalog without reconfiguring a provider.
// Credential and request checks are local and use the same rule as /model.
func (h *bridgeHandler) listModels(req pibridge.Request) {
	if h.r.models == nil {
		req.ReplyModelsUnavailable()
		return
	}
	var models []pibridge.ModelRef
	for _, model := range h.r.usableModels() {
		models = append(models, pibridge.ModelRef{Provider: model.provider, Model: model.option.Model.ID})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Provider == models[j].Provider {
			return models[i].Model < models[j].Model
		}
		return models[i].Provider < models[j].Provider
	})
	truncated := len(models) > bridgeModelLimit
	if truncated {
		models = models[:bridgeModelLimit]
	}
	req.ReplyModels(models, truncated)
}

// listThinking answers with the levels the loaded model actually accepts, not
// the formatted footer string.
func (h *bridgeHandler) listThinking(req pibridge.Request) {
	if h.r.cfg.Model.ID == "" {
		req.ReplyThinkingUnavailable()
		return
	}
	levels := provider.SupportedThinking(h.r.cfg.Model)
	if len(levels) == 0 {
		req.ReplyThinkingUnavailable()
		return
	}
	req.ReplyThinkingLevels(levels, h.r.ov.Provider, h.r.ov.Model)
}

// readHistory answers with the live editor recall, newest first. It is read
// on the loop because the editor is loop-owned, and it changes nothing.
func (h *bridgeHandler) readHistory(req pibridge.Request) {
	if h.console == nil {
		req.ReplyHistoryUnavailable()
		return
	}
	entries := h.console.Editor.History()
	total := 0
	for _, entry := range entries {
		total += len(entry)
	}
	if total > bridgeHistoryBytes {
		req.ReplyHistoryUnavailable()
		return
	}
	newest := make([]string, len(entries))
	for i, entry := range entries {
		newest[len(entries)-1-i] = entry
	}
	req.ReplyHistory(newest)
}

// startCompact admits one native compaction. It answers immediately when
// admission refuses; an accepted request stays unanswered until the host
// reports a terminal outcome, so Phi never treats a lost receipt as success.
func (h *bridgeHandler) startCompact(req pibridge.Request, runBusy bool) {
	id, ok := canonicalDecimal(req.RequestID())
	switch {
	case !ok:
		req.ReplyCompact(pibridge.CompactRefused, "stale")
		return
	case id <= h.compactHigh:
		req.ReplyCompact(pibridge.CompactRefused, "duplicate")
		return
	case h.compact != nil:
		req.ReplyCompact(pibridge.CompactRefused, "pending")
		return
	}
	h.compactHigh = id
	expect := req.CompactExpectation()
	switch {
	case req.Expired():
		req.ReplyCompact(pibridge.CompactRefused, "unavailable")
		return
	case h.r.store == nil, h.r.selector != nil:
		req.ReplyCompact(pibridge.CompactRefused, "unavailable")
		return
	case h.r.state == StateStorageBlocked || h.r.store.WriteError() != nil:
		req.ReplyCompact(pibridge.CompactRefused, "unavailable")
		return
	case h.prep != nil:
		// A preparation can still install a configuration; a compaction
		// started beside it would summarize the wrong context.
		req.ReplyCompact(pibridge.CompactRefused, "busy")
		return
	case expect.Session != h.r.store.Header().ID, expect.Leaf != h.r.store.LeafID():
		req.ReplyCompact(pibridge.CompactRefused, "stale")
		return
	case expect.Model.Provider != h.r.ov.Provider, expect.Model.Model != h.r.ov.Model:
		req.ReplyCompact(pibridge.CompactRefused, "stale")
		return
	case runBusy || h.r.state != StateIdle:
		req.ReplyCompact(pibridge.CompactRefused, "busy")
		return
	case h.r.compactActive:
		req.ReplyCompact(pibridge.CompactRefused, "compaction-active")
		return
	case h.reload != nil:
		req.ReplyCompact(pibridge.CompactRefused, "pending")
		return
	case h.queuedInputs() > 0:
		req.ReplyCompact(pibridge.CompactRefused, "queued")
		return
	case h.compactRun == nil:
		req.ReplyCompact(pibridge.CompactRefused, "unavailable")
		return
	}
	op, err := h.compactRun("")
	if err != nil {
		req.ReplyCompact(compactFailure(err))
		return
	}
	h.compact = &bridgeCompact{id: id, request: req, session: expect.Session, leaf: expect.Leaf, op: op}
	h.r.compactPending = true
	h.r.syncBridge()
}

// startReload admits one native resource reload. Admission mirrors
// compaction: identity and readiness are rechecked immediately before
// callback entry, and anything unexpected refuses without invoking the
// native command. The native command runs synchronously on the loop, so
// the dispatch acknowledgment reports entry and the latch releases on
// completion in the same call.
func (h *bridgeHandler) startReload(req pibridge.Request, runBusy bool) {
	id, ok := canonicalDecimal(req.RequestID())
	switch {
	case !ok:
		req.ReplyReload(false, "stale")
		return
	case id <= h.r.reloadHigh:
		req.ReplyReload(false, "duplicate")
		return
	case h.reload != nil:
		req.ReplyReload(false, "pending")
		return
	}
	h.r.reloadHigh = id
	expect := req.ReloadExpectation()
	switch {
	case req.Expired():
		req.ReplyReload(false, "unavailable")
		return
	case h.r.store == nil, h.r.selector != nil:
		req.ReplyReload(false, "unavailable")
		return
	case h.r.state == StateStorageBlocked || h.r.store.WriteError() != nil:
		req.ReplyReload(false, "unavailable")
		return
	case h.prep != nil:
		req.ReplyReload(false, "busy")
		return
	case expect.Session != h.r.store.Header().ID, expect.Leaf != h.r.store.LeafID():
		req.ReplyReload(false, "stale")
		return
	case expect.Model.Provider != h.r.ov.Provider, expect.Model.Model != h.r.ov.Model:
		req.ReplyReload(false, "stale")
		return
	case expect.HasRunID && expect.RunID != h.r.bridge.RunID(),
		expect.HasContextRev && expect.ContextRev != h.r.contextRev:
		req.ReplyReload(false, "stale")
		return
	case runBusy:
		// Until RunStart binds the logical id, no local worker owns a
		// prompt to interrupt or replay.
		if h.r.bridge == nil || h.r.bridge.RunID() == 0 || h.reloadInterrupt == nil {
			req.ReplyReload(false, "busy")
			return
		}
		op, reason := h.reloadInterrupt()
		if reason != "" {
			req.ReplyReload(false, reason)
			return
		}
		h.reload = &bridgeReload{id: id, request: req, op: op}
		h.r.reloadPending = true
		h.r.syncBridge()
		// Entry is acknowledged before cancellation runs: DISPATCHED reports
		// callback entry, never completion. The latch retires at settlement.
		req.ReplyReload(true, "")
		return
	case h.r.state != StateIdle:
		req.ReplyReload(false, "busy")
		return
	case h.r.compactActive:
		req.ReplyReload(false, "compaction-active")
		return
	case h.queuedInputs() > 0:
		req.ReplyReload(false, "queued")
		return
	case h.compact != nil:
		req.ReplyReload(false, "pending")
		return
	case h.reloadRun == nil:
		req.ReplyReload(false, "unavailable")
		return
	}
	h.reload = &bridgeReload{id: id, request: req}
	h.r.reloadPending = true
	h.r.syncBridge()
	// Entry is acknowledged before the native command runs: DISPATCHED
	// reports callback entry, never completion.
	req.ReplyReload(true, "")
	runErr := h.reloadRun()
	h.reload = nil
	h.r.reloadPending = false
	h.r.syncBridge()
	_ = runErr // entry already acknowledged; the native path reports locally
}

// retireReload releases this owner's latch once the retry decision and
// admission are final. A newer owner's latch is left untouched.
func (h *bridgeHandler) retireReload(op uint64) {
	if h == nil || h.reload == nil || h.reload.op != op {
		return
	}
	h.reload = nil
	h.r.reloadPending = false
	h.r.syncBridge()
}

// resolveCompactRun answers the outstanding compaction from the settled run.
// Exactly one terminal answer is emitted per accepted request; the latch never
// outlives the operation it describes. A receipt bound to another worker is
// left for its own settlement. A committed checkpoint wins over the
// cancellation that followed it.
func (h *bridgeHandler) resolveCompactRun(err error, op uint64, committed bool) {
	if h == nil || h.compact == nil {
		return
	}
	if op != 0 && h.compact.op != 0 && h.compact.op != op {
		return
	}
	outcome, reason := pibridge.CompactCompleted, ""
	var noWork *agent.CompactionNoWorkError
	switch {
	case committed:
	case errors.Is(err, context.Canceled):
		outcome, reason = pibridge.CompactCancelled, "cancelled"
	case errors.As(err, &noWork):
		outcome, reason = pibridge.CompactFailed, "already-compacted"
	case err != nil:
		outcome, reason = pibridge.CompactFailed, "error"
	}
	h.compact.request.ReplyCompact(outcome, reason)
	h.compact = nil
	h.r.compactPending = false
	h.r.syncBridge()
}

// compactFailure maps a synchronous admission error to a terminal answer.
func compactFailure(err error) (pibridge.CompactOutcome, string) {
	var noWork *agent.CompactionNoWorkError
	if errors.As(err, &noWork) {
		return pibridge.CompactFailed, "already-compacted"
	}
	return pibridge.CompactFailed, "error"
}

// canonicalDecimal accepts the canonical positive decimal form only, so a
// replay cannot hide behind a different spelling of the same value.
func canonicalDecimal(value string) (uint64, bool) {
	if value == "" || len(value) > 20 || value[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return parsed, err == nil
}

// queuedInputs counts undelivered steering and follow-ups on the live run.
func (h *bridgeHandler) queuedInputs() int {
	if h.activeLoop == nil {
		return 0
	}
	return len(h.activeLoop.PendingSteering()) + len(h.activeLoop.PendingFollowUp())
}

// startSearch reads one page on a worker. A third concurrent request is
// refused rather than queued: the caller retries with the same anchor.
func (h *bridgeHandler) startSearch(req pibridge.Request) {
	if h.r.store == nil || h.searches >= bridgeSearchWorkers {
		req.ReplyTranscriptUnavailable()
		return
	}
	query := req.Query()
	if strings.TrimSpace(query) == "" || len(query) > bridgeSearchQueryBytes || req.SearchStart() < 0 {
		req.ReplyTranscriptUnavailable()
		return
	}
	h.searches++
	job := &bridgeSearch{request: req}
	store := h.r.store
	go func() {
		job.page, job.err = store.SearchVisible(req.Context(), session.SearchOptions{
			Query:        query,
			AnchorLeafID: req.SearchAnchor(),
			StartIndex:   req.SearchStart(),
			Budget:       bridgeSearchBudget,
		})
		h.searchDone <- job
	}()
}

// finishSearch answers a finished page on the loop, after rechecking that the
// branch the caller pinned is still the branch this session is on.
func (h *bridgeHandler) finishSearch(job *bridgeSearch) {
	h.searches--
	if job.err != nil {
		job.request.ReplyTranscriptUnavailable()
		return
	}
	matches := make([]pibridge.TranscriptMatch, 0, len(job.page.Matches))
	for _, match := range job.page.Matches {
		matches = append(matches, pibridge.TranscriptMatch{EntryID: match.EntryID, Role: match.Role, Snippet: match.Snippet})
	}
	job.request.ReplyTranscript(job.page.SessionID, job.page.AnchorID, job.page.NextIndex, matches, job.page.Complete)
}

// listSkills answers with live session skills: name and description only.
// One entry that cannot be represented makes the whole list unavailable,
// because silently dropping a skill would hide a row the user could invoke.
func (h *bridgeHandler) listSkills(req pibridge.Request) {
	if len(h.r.skills) == 0 {
		req.ReplySkills(nil, false)
		return
	}
	skills := make([]pibridge.SkillRef, 0, len(h.r.skills))
	for i, skill := range h.r.skills {
		if i >= bridgeSkillLimit {
			req.ReplySkills(skills, true)
			return
		}
		if len(skill.Name) > bridgeSkillNameBytes || len(skill.Description) > bridgeSkillDescriptionBytes {
			req.ReplySkillsUnavailable()
			return
		}
		skills = append(skills, pibridge.SkillRef{Name: skill.Name, Description: skill.Description})
	}
	req.ReplySkills(skills, false)
}

// invokeSkill validates a dispatch on the loop and expands it on the shared
// worker: reading a skill file must never block typing. The answer means
// queued or submitted, never completed.
func (h *bridgeHandler) invokeSkill(req pibridge.Request, runBusy bool) {
	name := strings.TrimSpace(req.Name())
	if name != req.Name() || name == "" || !knownSkill(h.r.skills, name) {
		req.ReplyInvokeSkill(false, "unknown-skill")
		return
	}
	switch {
	case h.prep != nil, req.Expired():
		req.ReplyInvokeSkill(false, "unavailable")
		return
	case h.r.store == nil, h.r.state == StateStorageBlocked, h.r.state == StateCompacting, h.r.selector != nil:
		req.ReplyInvokeSkill(false, "unavailable")
		return
	case h.r.store.WriteError() != nil:
		req.ReplyInvokeSkill(false, "unavailable")
		return
	}
	list := h.r.skills
	h.beginWork(req, func() (string, error) { return skills.Invoke(list, name, "") })
}

// finishSkill dispatches an expanded skill after the worker returns, once the
// session, generation, and skill list still match what the request assumed.
func (h *bridgeHandler) finishSkill(prep *bridgePrep, runBusy bool) {
	switch {
	case prep.err != nil, prep.expanded == "", prep.request.Expired():
		prep.request.ReplyInvokeSkill(false, "unavailable")
		return
	case h.r.store == nil, h.r.generation != prep.gen, h.r.store.Header().ID != prep.session:
		prep.request.ReplyInvokeSkill(false, "unavailable")
		return
	case !knownSkill(h.r.skills, prep.request.Name()):
		prep.request.ReplyInvokeSkill(false, "unavailable")
		return
	}
	// The native skill path never runs under an open selector or a state
	// that cannot submit; the bridge must not start one there either.
	if !runBusy && (h.r.selector != nil || !h.r.state.CanSubmit()) {
		prep.request.ReplyInvokeSkill(false, "unavailable")
		return
	}
	if runBusy {
		if h.activeLoop == nil {
			prep.request.ReplyInvokeSkill(false, "unavailable")
			return
		}
		h.activeLoop.SteerWithProvenance(prep.expanded, "/skill:"+prep.request.Name(), "skill")
		h.r.bumpContextRev()
		prep.request.ReplyInvokeSkill(true, "")
		return
	}
	if h.start == nil {
		prep.request.ReplyInvokeSkill(false, "unavailable")
		return
	}
	h.start(prep.expanded, prep.expanded, "", "/skill:"+prep.request.Name(), "skill", false)
	prep.request.ReplyInvokeSkill(true, "")
}

// knownSkill reports whether this exact bare name is in the live session.
func knownSkill(list []skills.Skill, name string) bool {
	for _, skill := range list {
		if skill.Name == name {
			return true
		}
	}
	return false
}

// startModelSwitch admits a switch and hands the candidate configuration to
// the preparation worker. Nothing is appended or installed here.
func (h *bridgeHandler) startModelSwitch(req pibridge.Request, runBusy bool) {
	if h.refuseSettings(req, runBusy) {
		return
	}
	providerID, model := strings.TrimSpace(req.Provider()), strings.TrimSpace(req.Model())
	if providerID == "" || model == "" || h.r.models == nil {
		req.ReplySetModel(false, "")
		return
	}
	if _, err := h.r.models.SelectModel(providerID, model); err != nil {
		req.ReplySetModel(false, "unknown-model")
		return
	}
	next := h.r.ov
	next.Provider, next.Model = providerID, model
	next.ThinkingExplicit = false
	// Already current: nothing to persist, so answer without a worker.
	alreadyCurrent := providerID == h.r.ov.Provider && model == h.r.ov.Model
	h.admitSettings(req, pibridge.RequestSetModel, next, alreadyCurrent)
}

// startThinkingSwitch admits a level change for the live model. An
// unadvertised level is admitted because the provider clamps it.
func (h *bridgeHandler) startThinkingSwitch(req pibridge.Request, runBusy bool) {
	if h.refuseSettings(req, runBusy) {
		return
	}
	level := strings.TrimSpace(req.Level())
	if level == "" {
		req.ReplySetThinking(false, "")
		return
	}
	if req.Provider() != h.r.ov.Provider || req.Model() != h.r.ov.Model {
		req.ReplySetThinking(false, "model-changed")
		return
	}
	if !provider.SupportsThinking(h.r.cfg.Model, level) {
		req.ReplySetThinking(false, "unsupported-level")
		return
	}
	next := h.r.ov
	next.Thinking = level
	next.ThinkingExplicit = true
	h.admitSettings(req, pibridge.RequestSetThinking, next, level == h.r.ov.Thinking)
}

// admitSettings decides what one validated live-settings request does to the
// preparation slot. Later arrivals win by arrival order: a running settings
// worker is marked superseded once, and at most one replacement waits for it.
// A skill or attachment preparation is never evicted.
func (h *bridgeHandler) admitSettings(req pibridge.Request, kind string, next config.StartupOverrides, alreadyCurrent bool) {
	if h.prep == nil {
		if h.replacement != nil {
			// The slot is free, so a waiting descriptor never survives a
			// finished worker; refuse it instead of starting stale work.
			replySettings(h.replacement.request, h.replacement.kind, false, "")
			h.replacement = nil
		}
		if alreadyCurrent {
			replySettings(req, kind, true, "")
			return
		}
		h.begin(req, next)
		return
	}
	if h.prep.kind != pibridge.RequestSetModel && h.prep.kind != pibridge.RequestSetThinking {
		// Never evict skill or attachment preparation.
		replySettings(req, kind, false, "")
		return
	}
	h.supersedePrep()
	if h.replacement != nil {
		replySettings(h.replacement.request, h.replacement.kind, false, "")
		h.replacement = nil
	}
	if alreadyCurrent {
		replySettings(req, kind, true, "")
		return
	}
	h.replacement = &bridgeSettings{
		request: req,
		kind:    kind,
		next:    next,
		base:    h.r.ov,
		session: h.r.store.Header().ID,
		gen:     h.r.generation,
	}
}

// supersedePrep retires the running settings worker with one unknown outcome.
// The worker keeps running so its candidate can be discarded and its provider
// closed, but it can no longer install or answer.
func (h *bridgeHandler) supersedePrep() {
	if h.prep == nil || h.prep.superseded {
		return
	}
	h.prep.superseded = true
	replySettings(h.prep.request, h.prep.kind, false, "")
}

// promoteReplacement starts the newest validated request after the worker it
// displaced finishes. Every fence is rechecked against the live runtime, so a
// replacement never lands on a different stream, session, or selection.
func (h *bridgeHandler) promoteReplacement() {
	repl := h.replacement
	h.replacement = nil
	if repl == nil {
		return
	}
	req := repl.request
	switch {
	case req.Expired(), h.r.reloadPending, h.r.store == nil, h.r.selector != nil, h.r.settingsPicker != nil:
	case h.r.store.WriteError() != nil, !settingsAdmitted(h.r.state):
	case h.r.generation != repl.gen, h.r.store.Header().ID != repl.session, h.r.ov != repl.base:
	default:
		h.begin(req, repl.next)
		return
	}
	replySettings(req, repl.kind, false, "")
}

// replySettings answers one model or thinking request once.
func replySettings(req pibridge.Request, kind string, ok bool, reason string) {
	if kind == pibridge.RequestSetThinking {
		req.ReplySetThinking(ok, reason)
		return
	}
	req.ReplySetModel(ok, reason)
}

// begin starts the single preparation worker for one admitted mutation.
func (h *bridgeHandler) begin(req pibridge.Request, next config.StartupOverrides) {
	cand := *h.r
	cand.prov = nil
	prep := &bridgePrep{
		request: req,
		kind:    req.Kind(),
		cand:    &cand,
		next:    next,
		base:    h.r.ov,
		session: h.r.store.Header().ID,
		gen:     h.r.generation,
		done:    make(chan struct{}),
	}
	h.prep = prep
	go func() {
		if err := prep.cand.configure(prep.next); err != nil {
			prep.err = err
			if prep.kind == pibridge.RequestSetModel {
				prep.failure = "unknown-model"
			}
		}
		close(prep.done)
	}()
}

// beginWork starts one preparation job that reads files instead of building a
// configuration. The single slot keeps a slow disk from stacking work.
func (h *bridgeHandler) beginWork(req pibridge.Request, work func() (string, error)) {
	prep := &bridgePrep{
		request: req,
		kind:    req.Kind(),
		base:    h.r.ov,
		session: h.r.store.Header().ID,
		gen:     h.r.generation,
		done:    make(chan struct{}),
		work:    work,
	}
	h.prep = prep
	go func() {
		prep.expanded, prep.err = prep.work()
		close(prep.done)
	}()
}

// refuseSettings reports whether a live model or thinking change must wait.
// Unlike other mutations, these are admitted during an owned, non-cancelling
// run or compaction: the running loop reads the new selection at its next
// turn boundary. A built-in picker, a pending reload, cancellation, exit, or
// blocked storage still fences the change. The wire has no reason for these
// refusals, so the answer stays unknown.
func (h *bridgeHandler) refuseSettings(req pibridge.Request, runBusy bool) bool {
	switch {
	case req.Expired(), h.r.reloadPending:
	case h.r.store == nil, h.r.selector != nil, h.r.settingsPicker != nil:
	case h.r.store.WriteError() != nil:
	case !settingsAdmitted(h.r.state):
	case runBusy && h.r.state == StateIdle:
	default:
		return false
	}
	req.ReplyUnavailable()
	return true
}

// settingsAdmitted reports whether a live selection may change in this state.
// Replay, selection UIs, cancellation, offline, exit and blocked storage are
// excluded; an owned run or compaction keeps accepting changes.
func settingsAdmitted(state State) bool {
	switch state {
	case StateIdle, StateWaiting, StateStreaming, StateRunningTool, StateCompacting:
		return true
	}
	return false
}

// finishPreparation resolves the worker result on the loop. Every rejection
// closes the candidate provider so no idle client leaks.
func (h *bridgeHandler) finishPreparation(runBusy bool) {
	prep := h.prep
	if prep == nil {
		return
	}
	h.prep = nil
	if prep.superseded {
		// The newest arrival owns the answer; this worker may only release
		// its candidate and hand the slot to the replacement.
		if prep.cand != nil && prep.cand.prov != nil {
			prep.cand.prov.CloseIdle()
		}
		h.promoteReplacement()
		return
	}
	if prep.kind == pibridge.RequestAttachment {
		h.finishAttach(prep, runBusy)
		return
	}
	if prep.kind == pibridge.RequestInvokeSkill {
		h.finishSkill(prep, runBusy)
		return
	}
	req, cand := prep.request, prep.cand
	discard := func(reason string) {
		if cand != nil && cand.prov != nil {
			cand.prov.CloseIdle()
		}
		replySettings(req, prep.kind, false, reason)
	}
	if prep.err != nil {
		discard(prep.failure)
		return
	}
	if cand.authErr != nil {
		// no-auth is only a valid SetModelAck reason; thinking reports an
		// unknown outcome instead of inventing a new vocabulary value.
		reason := ""
		if prep.kind == pibridge.RequestSetModel {
			reason = "no-auth"
		}
		discard(reason)
		return
	}
	if req.Expired() || h.r.store == nil || h.r.selector != nil || h.r.settingsPicker != nil || !settingsAdmitted(h.r.state) || h.r.reloadPending {
		discard("")
		return
	}
	// A native command or a session change while the worker ran invalidates
	// the candidate: its change would land on a different runtime.
	if h.r.ov != prep.base || h.r.generation != prep.gen || h.r.store.Header().ID != prep.session {
		discard("")
		return
	}
	// Settings saves replace the published map instead of mutating it, so the
	// candidate holds an immutable snapshot. A change after the worker started
	// makes the candidate's defaults stale.
	if !maps.Equal(h.r.settings.ModelThinkingLevels, cand.settings.ModelThinkingLevels) ||
		h.r.settings.DefaultThinking != cand.settings.DefaultThinking ||
		h.r.settings.DefaultThinkingSet != cand.settings.DefaultThinkingSet {
		discard("")
		return
	}
	// Compaction settings feed the candidate's request configuration too; a
	// change after the worker started makes them stale.
	liveCompact, snapCompact := h.r.settings.Compaction, cand.settings.Compaction
	if liveCompact.Enabled != snapCompact.Enabled ||
		liveCompact.KeepRecentTokens != snapCompact.KeepRecentTokens ||
		liveCompact.ReserveForModel(cand.ov.Provider, cand.ov.Model) != snapCompact.ReserveForModel(cand.ov.Provider, cand.ov.Model) {
		discard("")
		return
	}
	if prep.kind == pibridge.RequestSetThinking {
		h.applyThinking(prep, cand, discard)
		return
	}
	h.r.lockSelection()
	if _, err := h.r.store.AppendModelChange(cand.ov.Provider, cand.ov.Model); err != nil {
		h.r.unlockSelection()
		discard("")
		return
	}
	h.r.installConfiguration(cand)
	h.r.unlockSelection()
	if h.r.ac != nil {
		h.r.ac.ResetGrowth()
	}
	h.r.syncBridge()
	req.ReplySetModel(true, "")
}

// applyThinking persists and installs one level change. The model must still
// be the one the request named, or the level may mean something else.
func (h *bridgeHandler) applyThinking(prep *bridgePrep, cand *runtime, discard func(string)) {
	req := prep.request
	if cand.ov.Provider != req.Provider() || cand.ov.Model != req.Model() {
		discard("model-changed")
		return
	}
	h.r.lockSelection()
	if _, err := h.r.store.AppendThinkingLevelChange(cand.ov.Thinking); err != nil {
		h.r.unlockSelection()
		discard("")
		return
	}
	h.r.installConfiguration(cand)
	h.r.unlockSelection()
	h.r.syncBridge()
	req.ReplySetThinking(true, "")
}
