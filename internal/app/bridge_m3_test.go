//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/agent"
	"psi/internal/pibridge"
	pibridgepb "psi/internal/pibridge/pb"
	"psi/internal/session"
	"psi/internal/skills"
)

func skillsRequest(id string) *pibridgepb.PhiToPi {
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SkillsRequest{
		SkillsRequest: &pibridgepb.SkillsRequest{RequestId: id},
	}}
}

func invokeSkillRequest(id, name string) *pibridgepb.PhiToPi {
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_InvokeSkillRequest{
		InvokeSkillRequest: &pibridgepb.InvokeSkillRequest{RequestId: id, Name: name},
	}}
}

func appendUserText(t *testing.T, r *runtime, text string) {
	t.Helper()
	if _, err := r.store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: text, Timestamp: 1}}); err != nil {
		t.Fatalf("append user: %v", err)
	}
}

// bridgeSkill writes one real skill file so expansion runs the production path
// instead of a stub.
func bridgeSkill(t *testing.T, name, description, body string) skills.Skill {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return skills.Skill{Name: name, Description: description, FilePath: path, BaseDir: dir}
}

func TestBridgeSkillsListCarriesMetadataOnly(t *testing.T) {
	r := testRuntime(t)
	r.skills = []skills.Skill{
		bridgeSkill(t, "alpha", "first skill", "secret body one"),
		bridgeSkill(t, "beta", "second skill", "secret body two"),
	}
	f, h := startBridgeRuntime(t, r)

	f.inject <- skillsRequest("req-skills")
	h.handle(nextRequest(t, r.bridge), false)

	res := f.waitFrame(t, "skills response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSkillsResponse() != nil
	}).GetSkillsResponse()
	if res.GetRequestId() != "req-skills" || res.GetUnavailable() || res.GetTruncated() {
		t.Fatalf("list failed: %+v", res)
	}
	if len(res.GetSkills()) != 2 {
		t.Fatalf("skills = %+v, want two", res.GetSkills())
	}
	for _, entry := range res.GetSkills() {
		if entry.GetName() == "" || entry.GetDescription() == "" {
			t.Fatalf("skill entry lost its metadata: %+v", entry)
		}
		if strings.Contains(entry.GetDescription(), "secret") {
			t.Fatalf("skill body leaked into the list: %+v", entry)
		}
	}
}

func TestBridgeSkillsListTooWideIsUnavailable(t *testing.T) {
	r := testRuntime(t)
	r.skills = []skills.Skill{{Name: "wide", Description: strings.Repeat("d", bridgeSkillDescriptionBytes+1)}}
	f, h := startBridgeRuntime(t, r)

	f.inject <- skillsRequest("req-wide")
	h.handle(nextRequest(t, r.bridge), false)

	res := f.waitFrame(t, "wide skills response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSkillsResponse() != nil
	}).GetSkillsResponse()
	if !res.GetUnavailable() || len(res.GetSkills()) != 0 {
		t.Fatalf("oversized list = %+v, want unavailable", res)
	}
}

func TestBridgeInvokeSkillUnknownIsRefused(t *testing.T) {
	r := testRuntime(t)
	r.skills = []skills.Skill{bridgeSkill(t, "alpha", "first", "body")}
	f, h := startBridgeRuntime(t, r)
	before := countEntries(t, r)

	f.inject <- invokeSkillRequest("req-bad", "../alpha")
	h.handle(nextRequest(t, r.bridge), false)

	ack := f.waitFrame(t, "invoke ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetInvokeSkillAck() != nil
	}).GetInvokeSkillAck()
	if ack.GetOk() || ack.GetReason() != "unknown-skill" {
		t.Fatalf("ack = %+v, want unknown-skill refusal", ack)
	}
	if got := countEntries(t, r); got != before {
		t.Fatalf("entries = %d, want %d: a refused skill must change nothing", got, before)
	}
}

func TestBridgeInvokeSkillWhileBusyQueuesExpandedText(t *testing.T) {
	r := testRuntime(t)
	r.skills = []skills.Skill{bridgeSkill(t, "alpha", "first", "expanded alpha body")}
	loop := &agent.Loop{}
	f, h := startBridgeRuntime(t, r)
	h.activeLoop = loop
	h.start = func(string, string, string, string, string, bool) {
		t.Error("busy dispatch must queue, not start a second run")
	}

	f.inject <- invokeSkillRequest("req-queue", "alpha")
	h.handle(nextRequest(t, r.bridge), true)
	<-h.completed()
	h.finishPreparation(true)

	ack := f.waitFrame(t, "invoke ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetInvokeSkillAck() != nil
	}).GetInvokeSkillAck()
	if !ack.GetOk() {
		t.Fatalf("ack = %+v, want queued", ack)
	}
	pending := loop.PendingSteering()
	if len(pending) != 1 || !strings.Contains(pending[0], "expanded alpha body") {
		t.Fatalf("pending = %q, want the expanded skill body", pending)
	}
}

func TestBridgeInvokeSkillRefusedWhilePreparationRuns(t *testing.T) {
	r := testRuntime(t)
	r.skills = []skills.Skill{bridgeSkill(t, "alpha", "first", "body")}
	f, h := startBridgeRuntime(t, r)
	h.prep = &bridgePrep{request: pibridge.Request{}, kind: pibridge.RequestInvokeSkill, done: make(chan struct{})}

	f.inject <- invokeSkillRequest("req-busy", "alpha")
	h.handle(nextRequest(t, r.bridge), false)

	ack := f.waitFrame(t, "busy invoke ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetInvokeSkillAck() != nil
	}).GetInvokeSkillAck()
	if ack.GetOk() || ack.GetReason() != "unavailable" {
		t.Fatalf("ack = %+v, want unavailable while another preparation runs", ack)
	}
	close(h.prep.done)
	h.prep = nil
}

func TestBridgeTranscriptSearchAnswersOnePage(t *testing.T) {
	r := testRuntime(t)
	for _, text := range []string{"needle one", "no hit", "needle two"} {
		appendUserText(t, r, text)
	}
	f, h := startBridgeRuntime(t, r)

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_TranscriptSearchRequest{
		TranscriptSearchRequest: &pibridgepb.TranscriptSearchRequest{RequestId: "req-search", Query: "needle", Initial: true},
	}}
	h.handle(nextRequest(t, r.bridge), false)
	job := <-h.searchResults()
	h.finishSearch(job)

	res := f.waitFrame(t, "search response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetTranscriptSearchResponse() != nil
	}).GetTranscriptSearchResponse()
	if res.GetUnavailable() || res.GetRequestId() != "req-search" {
		t.Fatalf("search failed: %+v", res)
	}
	if len(res.GetMatches()) != 2 || res.GetMatches()[0].GetRole() != "user" {
		t.Fatalf("matches = %+v, want two user hits", res.GetMatches())
	}
	if !res.GetComplete() || res.GetAnchorLeafId() == "" {
		t.Fatalf("page = %+v, want a complete anchored page", res)
	}
	if strings.Contains(res.GetMatches()[0].GetSnippet(), "no hit") {
		t.Fatalf("snippet crossed entries: %q", res.GetMatches()[0].GetSnippet())
	}
}

func TestBridgeTranscriptSearchRefusesWithoutCapacity(t *testing.T) {
	r := testRuntime(t)
	f, h := startBridgeRuntime(t, r)
	h.searches = bridgeSearchWorkers

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_TranscriptSearchRequest{
		TranscriptSearchRequest: &pibridgepb.TranscriptSearchRequest{RequestId: "req-full", Query: "x", Initial: true},
	}}
	h.handle(nextRequest(t, r.bridge), false)

	res := f.waitFrame(t, "refused search", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetTranscriptSearchResponse() != nil
	}).GetTranscriptSearchResponse()
	if !res.GetUnavailable() || res.GetRequestId() != "req-full" {
		t.Fatalf("full search = %+v, want unavailable", res)
	}
}

// A selector or a non-submittable state is not a place the native skill
// path ever runs; the bridge must refuse rather than submit underneath it.
func TestBridgeInvokeSkillRefusedWhileSelectorOpen(t *testing.T) {
	r := testRuntime(t)
	r.skills = []skills.Skill{bridgeSkill(t, "alpha", "first", "expanded body")}
	f, h := startBridgeRuntime(t, r)
	h.start = func(string, string, string, string, string, bool) {
		t.Error("a skill must not start a run while a selector is open")
	}

	f.inject <- invokeSkillRequest("req-sel", "alpha")
	h.handle(nextRequest(t, r.bridge), false)
	<-h.completed()
	r.selector = &Selector{}
	h.finishPreparation(false)
	r.selector = nil

	ack := f.waitFrame(t, "selector invoke ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetInvokeSkillAck() != nil
	}).GetInvokeSkillAck()
	if ack.GetOk() || ack.GetReason() != "unavailable" {
		t.Fatalf("ack = %+v, want unavailable under an open selector", ack)
	}
}
