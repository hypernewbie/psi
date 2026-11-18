//go:build !windows

package app

import (
	"os"
	"strings"
	"testing"

	"psi/internal/config"
	pibridgepb "psi/internal/pibridge/pb"
	"psi/internal/skills"
	"psi/internal/terminal"
)

// Six read capabilities answer a held run without cancelling it or changing
// any selection, and every reply is correlated with its request.
func TestBridgeReadsDuringHeldRun(t *testing.T) {
	r := testRuntime(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-bridge-test")
	addModel(r, "anthropic", config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	r.skills = []skills.Skill{bridgeSkill(t, "alpha", "first skill", "secret body")}
	for _, text := range []string{"needle one", "no hit", "needle two"} {
		appendUserText(t, r, text)
	}
	f, h := startBridgeRuntime(t, r)

	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output strings.Builder
	console := terminal.NewConsole(input, &output)
	defer console.Close()
	console.Editor.SetHistory([]string{"oldest", "newest"})
	h.console = console

	// Hold a live run: the reads must not disturb it.
	r.state = StateWaiting
	r.bridgeRunStarted()
	r.contextRev = 7
	runID := r.bridge.RunID()
	if runID == 0 {
		t.Fatal("held run did not bind a logical id")
	}
	before := countEntries(t, r)
	selection := r.ov

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_StateRequest{
		StateRequest: &pibridgepb.StateRequest{RequestId: "read-state"},
	}}
	h.handle(nextRequest(t, r.bridge), true)
	state := f.waitFrame(t, "state reply", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetStateSnapshot().GetRequestId() == "read-state"
	}).GetStateSnapshot()
	if state.GetSessionId() != r.store.Header().ID || state.GetModel().GetModel() != selection.Model {
		t.Fatalf("state = %+v, want the live identity", state)
	}
	if got := state.GetReloadReadiness().GetRunId(); got != runID {
		t.Fatalf("state run id = %d, want %d", got, runID)
	}

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_ModelsRequest{
		ModelsRequest: &pibridgepb.ModelsRequest{RequestId: "read-models"},
	}}
	h.handle(nextRequest(t, r.bridge), true)
	models := f.waitFrame(t, "models reply", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetModelsResponse().GetRequestId() == "read-models"
	}).GetModelsResponse()
	listed := map[string]bool{}
	for _, entry := range models.GetModels() {
		listed[entry.GetModel()] = true
	}
	if models.GetUnavailable() || !listed[selection.Model] || !listed["m2"] {
		t.Fatalf("models = %+v, want the live and added entries", models.GetModels())
	}

	f.inject <- thinkingRequest("read-thinking")
	h.handle(nextRequest(t, r.bridge), true)
	levels := f.waitFrame(t, "thinking reply", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetThinkingLevelsResponse().GetRequestId() == "read-thinking"
	}).GetThinkingLevelsResponse()
	if levels.GetUnavailable() || len(levels.GetLevels()) == 0 {
		t.Fatalf("levels = %+v, want the live model's levels", levels)
	}

	f.inject <- historyRequest("read-history")
	h.handle(nextRequest(t, r.bridge), true)
	history := f.waitFrame(t, "history reply", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetHistoryResponse().GetRequestId() == "read-history"
	}).GetHistoryResponse()
	if got := history.GetEntries(); len(got) != 2 || got[0] != "newest" {
		t.Fatalf("history = %v, want newest first", got)
	}

	f.inject <- skillsRequest("read-skills")
	h.handle(nextRequest(t, r.bridge), true)
	list := f.waitFrame(t, "skills reply", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSkillsResponse().GetRequestId() == "read-skills"
	}).GetSkillsResponse()
	if len(list.GetSkills()) != 1 || list.GetSkills()[0].GetName() != "alpha" {
		t.Fatalf("skills = %+v, want the seeded skill", list.GetSkills())
	}
	if strings.Contains(list.GetSkills()[0].GetDescription(), "secret") {
		t.Fatal("skill body leaked into the read")
	}

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_TranscriptSearchRequest{
		TranscriptSearchRequest: &pibridgepb.TranscriptSearchRequest{RequestId: "read-search", Query: "needle", Initial: true},
	}}
	h.handle(nextRequest(t, r.bridge), true)
	job := <-h.searchResults()
	h.finishSearch(job)
	search := f.waitFrame(t, "search reply", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetTranscriptSearchResponse().GetRequestId() == "read-search"
	}).GetTranscriptSearchResponse()
	if len(search.GetMatches()) != 2 {
		t.Fatalf("matches = %+v, want two hits", search.GetMatches())
	}

	// Nothing was cancelled, started, or installed.
	if r.state != StateWaiting || r.ov != selection || countEntries(t, r) != before {
		t.Fatal("a read changed the held run or the session")
	}
	if r.bridge.RunID() != runID {
		t.Fatalf("run id moved to %d", r.bridge.RunID())
	}
	if h.prep != nil || h.reload != nil || h.compact != nil || r.reloadPending || r.compactPending {
		t.Fatal("a read left an operation latched")
	}
}
