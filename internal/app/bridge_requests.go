package app

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/pibridge"
	"psi/internal/provider"
	"psi/internal/providerregistry"
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

	// work expands one skill off the loop. The same slot serves every
	// preparation job, so a second request waits instead of competing.
	work     func() (string, error)
	expanded string
}

// bridgeHandler serves capability requests on the TUI loop. It owns one
// preparation slot: a second request waits for the first to finish instead
// of starting a competing configuration.
type bridgeHandler struct {
	r       *runtime
	console *terminal.Console
	prep    *bridgePrep

	// Loop-owned submission hook and the run currently in flight. They are
	// refreshed by the loop, never by a worker.
	start      func(text, display, focus, invocation, source string, compact bool)
	activeLoop *agent.Loop

	// searchDone receives finished pages. searches counts live workers on the
	// loop only; two are enough because each page is already bounded.
	searchDone chan *bridgeSearch
	searches   int

	// Compact admission state. highWater rejects replay; compact is the one
	// unresolved operation this process owns.
	compactHigh uint64
	compact     *bridgeCompact
	compactRun  func(focus string) error
}

// bridgeCompact is one accepted, unanswered compaction.
type bridgeCompact struct {
	id      uint64
	request pibridge.Request
	session string
	leaf    string
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
// mutations are refused while it is true.
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
	default:
		req.ReplyUnavailable()
	}
}

// listModels answers from the loaded catalog without reconfiguring a provider
// or re-reading files. Only entries the activation path accepts are offered.
func (h *bridgeHandler) listModels(req pibridge.Request) {
	if h.r.models == nil {
		req.ReplyModelsUnavailable()
		return
	}
	var models []pibridge.ModelRef
	for _, providerID := range providerregistry.IDs() {
		for _, option := range h.r.models.ModelOptions(providerID) {
			if !option.Available {
				continue
			}
			models = append(models, pibridge.ModelRef{Provider: providerID, Model: option.Model.ID})
		}
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
	case h.queuedInputs() > 0:
		req.ReplyCompact(pibridge.CompactRefused, "queued")
		return
	case h.compactRun == nil:
		req.ReplyCompact(pibridge.CompactRefused, "unavailable")
		return
	}
	if err := h.compactRun(""); err != nil {
		req.ReplyCompact(compactFailure(err))
		return
	}
	h.compact = &bridgeCompact{id: id, request: req, session: expect.Session, leaf: expect.Leaf}
	h.r.compactPending = true
	h.r.syncBridge()
}

// resolveCompactRun answers the outstanding compaction from the settled run.
// Exactly one terminal answer is emitted per accepted request; the latch never
// outlives the operation it describes.
func (h *bridgeHandler) resolveCompactRun(err error) {
	if h == nil || h.compact == nil {
		return
	}
	outcome, reason := pibridge.CompactCompleted, ""
	var noWork *agent.CompactionNoWorkError
	switch {
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
	if h.refuse(req, runBusy) {
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
	if providerID == h.r.ov.Provider && model == h.r.ov.Model {
		// Already current: nothing to persist, so answer without a write.
		req.ReplySetModel(true, "")
		return
	}
	next := h.r.ov
	next.Provider, next.Model = providerID, model
	h.begin(req, next)
}

// startThinkingSwitch admits a level change for the live model. A level the
// model does not accept is refused before any candidate work.
func (h *bridgeHandler) startThinkingSwitch(req pibridge.Request, runBusy bool) {
	if h.refuse(req, runBusy) {
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
	if !supportedLevel(h.r.cfg.Model, level) {
		req.ReplySetThinking(false, "unsupported-level")
		return
	}
	if level == h.r.ov.Thinking {
		req.ReplySetThinking(true, "")
		return
	}
	next := h.r.ov
	next.Thinking = level
	h.begin(req, next)
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

// supportedLevel reports whether the model accepts this thinking level.
func supportedLevel(model provider.ModelMeta, level string) bool {
	for _, candidate := range provider.SupportedThinking(model) {
		if candidate == level {
			return true
		}
	}
	return false
}

// refuse reports whether native state is too busy for a bridge mutation. The
// wire has no reason for these, so the answer stays unknown.
func (h *bridgeHandler) refuse(req pibridge.Request, runBusy bool) bool {
	switch {
	case h.prep != nil, runBusy, req.Expired():
	case h.r.store == nil, h.r.state != StateIdle, h.r.selector != nil:
	case h.r.store.WriteError() != nil:
	default:
		return false
	}
	req.ReplyUnavailable()
	return true
}

// finishPreparation resolves the worker result on the loop. Every rejection
// closes the candidate provider so no idle client leaks.
func (h *bridgeHandler) finishPreparation(runBusy bool) {
	prep := h.prep
	if prep == nil {
		return
	}
	h.prep = nil
	if prep.kind == pibridge.RequestInvokeSkill {
		h.finishSkill(prep, runBusy)
		return
	}
	req, cand := prep.request, prep.cand
	discard := func(reason string) {
		if cand != nil && cand.prov != nil {
			cand.prov.CloseIdle()
		}
		if prep.kind == pibridge.RequestSetThinking {
			req.ReplySetThinking(false, reason)
			return
		}
		req.ReplySetModel(false, reason)
	}
	if prep.err != nil {
		discard(prep.failure)
		return
	}
	if cand.authErr != nil {
		discard("no-auth")
		return
	}
	if req.Expired() || runBusy || h.r.store == nil || h.r.selector != nil || h.r.state != StateIdle {
		discard("")
		return
	}
	// A native command or a session change while the worker ran invalidates
	// the candidate: its change would land on a different runtime.
	if h.r.ov != prep.base || h.r.generation != prep.gen || h.r.store.Header().ID != prep.session {
		discard("")
		return
	}
	if prep.kind == pibridge.RequestSetThinking {
		h.applyThinking(prep, cand, discard)
		return
	}
	if _, err := h.r.store.AppendModelChange(cand.ov.Provider, cand.ov.Model); err != nil {
		discard("")
		return
	}
	h.r.installConfiguration(cand)
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
	if !supportedLevel(cand.cfg.Model, cand.ov.Thinking) {
		discard("unsupported-level")
		return
	}
	if _, err := h.r.store.AppendThinkingLevelChange(cand.ov.Thinking); err != nil {
		discard("")
		return
	}
	h.r.installConfiguration(cand)
	h.r.syncBridge()
	req.ReplySetThinking(true, "")
}
