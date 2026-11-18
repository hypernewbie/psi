package app

import (
	"errors"
	"testing"

	"psi/internal/config"
	"psi/internal/pibridge"
	pibridgepb "psi/internal/pibridge/pb"
	"psi/internal/provider"
	"psi/internal/session"
)

// newSettingsBridge is one idle, authenticated runtime with two spare models.
// Live settings need real credentials because the candidate builds a provider.
func newSettingsBridge(t *testing.T) (*runtime, *bridgePhi, *bridgeHandler) {
	t.Helper()
	r := testRuntime(t)
	addModel(r, "anthropic", config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	addModel(r, "anthropic", config.ModelMeta{ID: "m3", Reasoning: true, MaxTokens: 16384})
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-bridge-test")
	f, h := startBridgeRuntime(t, r)
	return r, f, h
}

func setModel(id, providerID, model string) *pibridgepb.PhiToPi {
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: id, Provider: providerID, Model: model},
	}}
}

func skillRequest(id, name string) *pibridgepb.PhiToPi {
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_InvokeSkillRequest{
		InvokeSkillRequest: &pibridgepb.InvokeSkillRequest{RequestId: id, Name: name},
	}}
}

func modelAck(t *testing.T, f *bridgePhi, id string) *pibridgepb.SetModelAck {
	t.Helper()
	return f.waitFrame(t, "set-model ack "+id, func(frame *pibridgepb.PiToPhi) bool {
		ack := frame.GetSetModelAck()
		return ack != nil && ack.GetRequestId() == id
	}).GetSetModelAck()
}

func thinkingAck(t *testing.T, f *bridgePhi, id string) *pibridgepb.SetThinkingAck {
	t.Helper()
	return f.waitFrame(t, "set-thinking ack "+id, func(frame *pibridgepb.PiToPhi) bool {
		ack := frame.GetSetThinkingAck()
		return ack != nil && ack.GetRequestId() == id
	}).GetSetThinkingAck()
}

// countSettingsAcks counts every answer for one id, so a duplicate cannot
// hide behind the first frame.
func countSettingsAcks(f *bridgePhi, id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, frame := range f.frames {
		switch {
		case frame.GetSetModelAck() != nil && frame.GetSetModelAck().GetRequestId() == id:
			n++
		case frame.GetSetThinkingAck() != nil && frame.GetSetThinkingAck().GetRequestId() == id:
			n++
		}
	}
	return n
}

// A live run and an owned compaction both accept a settings change; the
// shared loop reads the new selection at its next turn boundary. States that
// do not own a turn still refuse with an unknown outcome.
func TestBridgeSettingsDuringRunAndCompaction(t *testing.T) {
	r, f, h := newSettingsBridge(t)

	t.Run("owned run", func(t *testing.T) {
		r.state = StateWaiting
		defer func() { r.state = StateIdle }()
		before := countEntries(t, r)
		f.inject <- setModel("req-run-model", "anthropic", "m2")
		h.handle(nextRequest(t, r.bridge), true)
		waitPreparation(t, h)
		h.finishPreparation(true)
		if ack := modelAck(t, f, "req-run-model"); !ack.GetOk() {
			t.Fatalf("model change during a run was refused: %+v", ack)
		}
		if r.ov.Model != "m2" {
			t.Fatalf("live model = %q, want m2", r.ov.Model)
		}
		if got := countEntries(t, r) - before; got != 1 {
			t.Fatalf("model change entries = %d, want 1", got)
		}
	})

	t.Run("owned compaction", func(t *testing.T) {
		levels := provider.SupportedThinking(r.cfg.Model)
		level := levels[len(levels)-1]
		r.state = StateCompacting
		r.compactActive = true
		defer func() { r.state, r.compactActive = StateIdle, false }()
		before := countEntries(t, r)
		f.inject <- setThinking("req-compact-thinking", level, r.ov.Provider, r.ov.Model)
		h.handle(nextRequest(t, r.bridge), true)
		waitPreparation(t, h)
		h.finishPreparation(true)
		if ack := thinkingAck(t, f, "req-compact-thinking"); !ack.GetOk() {
			t.Fatalf("thinking change during compaction was refused: %+v", ack)
		}
		if r.ov.Thinking != level {
			t.Fatalf("live level = %q, want %q", r.ov.Thinking, level)
		}
		if got := countEntries(t, r) - before; got != 1 {
			t.Fatalf("thinking change entries = %d, want 1", got)
		}
	})

	t.Run("settling run refuses", func(t *testing.T) {
		r.state = StateCancelling
		defer func() { r.state = StateIdle }()
		before := countEntries(t, r)
		f.inject <- setModel("req-cancelling", "anthropic", "m3")
		h.handle(nextRequest(t, r.bridge), true)
		if h.busy() {
			t.Fatal("cancelling runtime started a preparation")
		}
		if ack := modelAck(t, f, "req-cancelling"); ack.GetOk() || ack.GetReason() != "" {
			t.Fatalf("cancelling refusal must leave the outcome unknown: %+v", ack)
		}
		if r.ov.Model != "m2" || countEntries(t, r) != before {
			t.Fatal("refused switch changed the runtime or the session")
		}
	})

	t.Run("run start before state moves", func(t *testing.T) {
		before := countEntries(t, r)
		f.inject <- setModel("req-starting", "anthropic", "m3")
		h.handle(nextRequest(t, r.bridge), true)
		if ack := modelAck(t, f, "req-starting"); ack.GetOk() {
			t.Fatalf("pre-state run start was treated as idle: %+v", ack)
		}
		if h.busy() || countEntries(t, r) != before {
			t.Fatal("pre-state run start admitted a preparation")
		}
	})

	t.Run("open picker refuses", func(t *testing.T) {
		r.selector = &Selector{}
		defer func() { r.selector = nil }()
		f.inject <- setModel("req-picker", "anthropic", "m3")
		h.handle(nextRequest(t, r.bridge), false)
		if ack := modelAck(t, f, "req-picker"); ack.GetOk() {
			t.Fatalf("picker refusal was admitted: %+v", ack)
		}
		if h.busy() {
			t.Fatal("picker refusal started a preparation")
		}
	})

	t.Run("blocked storage refuses", func(t *testing.T) {
		r.store.FailWritesForTest(errors.New("storage blocked"))
		f.inject <- setModel("req-storage", "anthropic", "m3")
		h.handle(nextRequest(t, r.bridge), true)
		if ack := modelAck(t, f, "req-storage"); ack.GetOk() {
			t.Fatalf("storage refusal was admitted: %+v", ack)
		}
		if h.busy() {
			t.Fatal("storage refusal started a preparation")
		}
	})
}

// Later arrivals win by arrival order. One worker runs, at most one validated
// replacement waits, and an invalid arrival cannot displace either of them.
func TestBridgeSettingsLatestValidatedRequestWins(t *testing.T) {
	r, f, h := newSettingsBridge(t)
	before := countEntries(t, r)
	levels := provider.SupportedThinking(r.cfg.Model)
	level := levels[len(levels)-1]
	if level == r.ov.Thinking {
		t.Fatalf("test model needs a level other than the default %q", level)
	}

	f.inject <- setModel("req-latest-a", "anthropic", "m2")
	h.handle(nextRequest(t, r.bridge), false)
	f.inject <- setThinking("req-latest-b", level, r.ov.Provider, r.ov.Model)
	h.handle(nextRequest(t, r.bridge), false)
	f.inject <- setModel("req-latest-c", "anthropic", "m3")
	h.handle(nextRequest(t, r.bridge), false)

	// An unknown model is refused before it can touch the waiting request.
	f.inject <- setModel("req-latest-bad", "anthropic", "missing")
	h.handle(nextRequest(t, r.bridge), false)
	if ack := modelAck(t, f, "req-latest-bad"); ack.GetOk() || ack.GetReason() != "unknown-model" {
		t.Fatalf("invalid arrival = %+v, want unknown-model", ack)
	}

	waitPreparation(t, h)
	h.finishPreparation(false) // discards A and starts C
	waitPreparation(t, h)
	h.finishPreparation(false) // installs C

	for _, id := range []string{"req-latest-a", "req-latest-b"} {
		if got := countSettingsAcks(f, id); got != 1 {
			t.Fatalf("%s acks = %d, want exactly 1", id, got)
		}
	}
	if ack := modelAck(t, f, "req-latest-a"); ack.GetOk() || ack.GetReason() != "" {
		t.Fatalf("superseded model = %+v, want unknown outcome", ack)
	}
	if ack := thinkingAck(t, f, "req-latest-b"); ack.GetOk() || ack.GetReason() != "" {
		t.Fatalf("superseded thinking = %+v, want unknown outcome", ack)
	}
	if ack := modelAck(t, f, "req-latest-c"); !ack.GetOk() {
		t.Fatalf("latest arrival was refused: %+v", ack)
	}
	if r.ov.Model != "m3" {
		t.Fatalf("live model = %q, want the last arrival m3", r.ov.Model)
	}
	if got := countEntries(t, r) - before; got != 1 {
		t.Fatalf("durable entries = %d, want only the last arrival", got)
	}
}

// An arrival for the selection already in use supersedes older work and
// succeeds without creating a worker.
func TestBridgeSettingsAlreadyCurrentSupersedes(t *testing.T) {
	r, f, h := newSettingsBridge(t)
	before := countEntries(t, r)

	f.inject <- setModel("req-current-a", "anthropic", "m2")
	h.handle(nextRequest(t, r.bridge), false)
	f.inject <- setModel("req-current-b", "anthropic", "m")
	h.handle(nextRequest(t, r.bridge), false)
	if ack := modelAck(t, f, "req-current-b"); !ack.GetOk() {
		t.Fatalf("already-current arrival = %+v, want success", ack)
	}
	if !h.busy() {
		t.Fatal("superseding arrival released the running worker early")
	}

	waitPreparation(t, h)
	h.finishPreparation(false)
	if h.busy() {
		t.Fatal("superseded worker was not discarded")
	}
	if ack := modelAck(t, f, "req-current-a"); ack.GetOk() || ack.GetReason() != "" {
		t.Fatalf("superseded work = %+v, want unknown outcome", ack)
	}
	if r.ov.Model != "m" || countEntries(t, r) != before {
		t.Fatal("superseded work changed the runtime or the session")
	}
}

// The preparation slot has one owner. Neither direction evicts work that was
// already admitted.
func TestBridgeSettingsPreservePreparationOwner(t *testing.T) {
	t.Run("skill prep is not evicted", func(t *testing.T) {
		r, f, h := newSettingsBridge(t)
		f.inject <- skillRequest("req-skill-owner", "demo")
		req := nextRequest(t, r.bridge)
		gate := make(chan struct{})
		h.beginWork(req, func() (string, error) {
			<-gate
			return "", errors.New("expansion failed")
		})

		f.inject <- setModel("req-settings-blocked", "anthropic", "m2")
		h.handle(nextRequest(t, r.bridge), false)
		if ack := modelAck(t, f, "req-settings-blocked"); ack.GetOk() {
			t.Fatalf("settings evicted a skill preparation: %+v", ack)
		}
		if h.prep == nil || h.prep.superseded {
			t.Fatal("settings displaced the skill worker")
		}

		close(gate)
		waitPreparation(t, h)
		h.finishPreparation(false)
		if r.ov.Model != "m" {
			t.Fatalf("evicted skill prep changed the model to %q", r.ov.Model)
		}
	})

	t.Run("attachment cannot evict settings prep", func(t *testing.T) {
		r, f, h := newSettingsBridge(t)
		r.cfg.Model.InputImage = true
		root := t.TempDir()
		data := writeAttachmentPNG(t, root)
		t.Setenv("PHI_PI_ATTACHMENT_ROOT", root)

		f.inject <- setModel("req-settings-owner", "anthropic", "m2")
		h.handle(nextRequest(t, r.bridge), false)
		f.inject <- attachmentPrompt(r, "12", "look", attachmentImageRef("a.png", data))
		h.handle(nextRequest(t, r.bridge), false)
		ack := attachmentAck(t, f, "12")
		if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED || ack.GetReason() != "pending" {
			t.Fatalf("attachment = %v %q, want REFUSED pending", ack.GetOutcome(), ack.GetReason())
		}
		if h.prep == nil || h.prep.kind != pibridge.RequestSetModel {
			t.Fatal("attachment displaced the settings worker")
		}

		waitPreparation(t, h)
		h.finishPreparation(false)
		if r.ov.Model != "m2" {
			t.Fatalf("live model = %q, want the admitted settings change", r.ov.Model)
		}
	})
}

// A candidate that went stale, lost its stream, or could not be persisted
// never installs and never publishes.
func TestBridgeSettingsRejectStaleOrUnpersistedCandidate(t *testing.T) {
	t.Run("generation changed", func(t *testing.T) {
		r, f, h := newSettingsBridge(t)
		before := countEntries(t, r)
		f.inject <- setModel("req-gen", "anthropic", "m2")
		h.handle(nextRequest(t, r.bridge), false)
		waitPreparation(t, h)
		r.generation++
		h.finishPreparation(false)
		if ack := modelAck(t, f, "req-gen"); ack.GetOk() || ack.GetReason() != "" {
			t.Fatalf("stale candidate = %+v, want unknown outcome", ack)
		}
		if r.ov.Model != "m" || countEntries(t, r) != before {
			t.Fatal("stale candidate changed the runtime or the session")
		}
	})

	t.Run("defaults changed", func(t *testing.T) {
		r, f, h := newSettingsBridge(t)
		before := countEntries(t, r)
		f.inject <- setThinking("req-defaults", "off", r.ov.Provider, r.ov.Model)
		h.handle(nextRequest(t, r.bridge), false)
		waitPreparation(t, h)
		r.settings.DefaultThinking = "high"
		h.finishPreparation(false)
		if ack := thinkingAck(t, f, "req-defaults"); ack.GetOk() {
			t.Fatalf("stale defaults were installed: %+v", ack)
		}
		if r.ov.Thinking != "off" || countEntries(t, r) != before {
			t.Fatal("stale candidate changed the runtime or the session")
		}
	})

	t.Run("session changed", func(t *testing.T) {
		r, f, h := newSettingsBridge(t)
		f.inject <- setModel("req-session", "anthropic", "m2")
		h.handle(nextRequest(t, r.bridge), false)
		waitPreparation(t, h)
		other, err := session.New(session.OpenOptions{Cwd: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(other.Unlock)
		r.store = other
		h.finishPreparation(false)
		if ack := modelAck(t, f, "req-session"); ack.GetOk() {
			t.Fatalf("candidate for another session was installed: %+v", ack)
		}
		if r.ov.Model != "m" {
			t.Fatalf("live model = %q, want the old session selection", r.ov.Model)
		}
		if countEntries(t, r) != 0 {
			t.Fatal("candidate wrote into the new session")
		}
	})

	t.Run("lost receipt after commit", func(t *testing.T) {
		r, f, h := newSettingsBridge(t)
		before := countEntries(t, r)
		f.inject <- setModel("req-receipt", "anthropic", "m2")
		h.handle(nextRequest(t, r.bridge), false)
		waitPreparation(t, h)
		h.finishPreparation(false)
		if ack := modelAck(t, f, "req-receipt"); !ack.GetOk() {
			t.Fatalf("committed selection was refused: %+v", ack)
		}
		// The install precedes the reply. A stream that dies now must keep
		// the published selection and must not reapply it.
		r.closeBridge()
		if r.ov.Model != "m2" {
			t.Fatalf("lost reply rolled the selection back to %q", r.ov.Model)
		}
		if got := countEntries(t, r) - before; got != 1 {
			t.Fatalf("durable entries = %d, want the one committed change", got)
		}
		if got := countSettingsAcks(f, "req-receipt"); got > 1 {
			t.Fatalf("lost reply was retried %d times", got)
		}
	})

	t.Run("append failure", func(t *testing.T) {
		r, f, h := newSettingsBridge(t)
		before := countEntries(t, r)
		f.inject <- setModel("req-append", "anthropic", "m2")
		h.handle(nextRequest(t, r.bridge), false)
		r.store.FailWritesForTest(errors.New("disk full"))
		waitPreparation(t, h)
		h.finishPreparation(false)
		if ack := modelAck(t, f, "req-append"); ack.GetOk() || ack.GetReason() != "" {
			t.Fatalf("unpersisted candidate = %+v, want unknown outcome", ack)
		}
		if r.ov.Model != "m" || countEntries(t, r) != before {
			t.Fatal("unpersisted candidate changed the runtime or the session")
		}
		if r.store.WriteError() == nil {
			t.Fatal("failed append did not keep the storage error sticky")
		}
	})

	t.Run("stream lost before commit", func(t *testing.T) {
		r, f, h := newSettingsBridge(t)
		before := countEntries(t, r)
		f.inject <- setModel("req-lost", "anthropic", "m2")
		h.handle(nextRequest(t, r.bridge), false)
		r.closeBridge()
		waitPreparation(t, h)
		h.finishPreparation(false)
		if r.ov.Model != "m" || countEntries(t, r) != before {
			t.Fatal("lost stream committed a selection")
		}
	})

	t.Run("thinking without auth", func(t *testing.T) {
		r := testRuntime(t)
		level := provider.SupportedThinking(r.cfg.Model)[0]
		f, h := startBridgeRuntime(t, r)
		before := countEntries(t, r)
		f.inject <- setThinking("req-noauth-thinking", level, r.ov.Provider, r.ov.Model)
		h.handle(nextRequest(t, r.bridge), false)
		waitPreparation(t, h)
		h.finishPreparation(false)
		if ack := thinkingAck(t, f, "req-noauth-thinking"); ack.GetOk() || ack.GetReason() != "" {
			t.Fatalf("unauthenticated level = %+v, want unknown outcome", ack)
		}
		if r.ov.Thinking != "off" || countEntries(t, r) != before {
			t.Fatal("unauthenticated candidate changed the runtime or the session")
		}
	})
}
