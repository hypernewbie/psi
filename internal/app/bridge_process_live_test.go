//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"

	pibridgepb "psi/internal/pibridge/pb"
	"psi/internal/session"
)

// bridgeChildEnv passes the fake Phi socket and identity to one app child.
// The token is synthetic and never leaves this test process.
func bridgeChildEnv(t *testing.T, f *bridgePhi) []string {
	t.Helper()
	return []string{
		"PHI_PI_BRIDGE_ENDPOINT=" + f.target,
		"PHI_PI_BRIDGE_PANE_ID=pane-m4",
		"PHI_PI_BRIDGE_PROCESS_ID=proc-m4",
		"PHI_PI_BRIDGE_TOKEN=" + strings.Repeat("ab", 32),
		"TMPDIR=" + os.TempDir(),
	}
}

// bridgeRunID reads the logical run reported by one state snapshot. Zero
// means no run has started yet.
func bridgeRunID(frame *pibridgepb.PiToPhi) uint64 {
	snap := frame.GetStateSnapshot()
	if snap == nil {
		return 0
	}
	return snap.GetReloadReadiness().GetRunId()
}

// sessionFile returns the one JSONL file the app wrote, so the test can
// reopen it without knowing the generated name.
func sessionFile(t *testing.T, agentDir string) string {
	t.Helper()
	var files []string
	_ = filepath.WalkDir(filepath.Join(agentDir, "sessions"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if len(files) != 1 {
		t.Fatalf("session files = %v, want exactly one", files)
	}
	return files[0]
}

// TestBridgeProcessLiveSettingsNextRequest proves that a model and thinking
// change accepted during a held run apply to that run's next provider
// request. The first request keeps its own selection, and nothing cancels or
// re-asks the prompt.
func TestBridgeProcessLiveSettingsNextRequest(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("live bridge settings need a Unix PTY")
	}
	bodies := make(chan map[string]any, 8)
	release := make(chan struct{}, 8)
	var turns, requests, cancelled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests.Add(1)
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
			cancelled.Add(1)
			return
		}
		fmt.Fprint(w, liveThinkingTurn(int(turns.Add(1))))
	}))
	defer server.Close()

	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	if err := os.WriteFile(filepath.Join(ws, "fixture.txt"), []byte("SYNTHETIC_READ_RESULT"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":["read"],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[`+
		`{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]},`+
		`{"id":"local-next","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	f := startBridgePhi(t)
	q := startReviewProcessEnv(t, home, ws, agentDir, *reviewBinary, "", bridgeChildEnv(t, f), "--deny-project", "--tools", "read")
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})

	q.prompt()
	q.write("read fixture.txt\r")
	first := liveWaitBody(t, bodies)
	if first["model"] != "local-model" {
		t.Fatalf("first request model = %v, want local-model", first["model"])
	}
	if think, ok := first["thinking"].(map[string]any); ok {
		if _, held := think["budget_tokens"]; held {
			t.Fatalf("first request already carries a thinking budget: %v", think)
		}
	}

	// The run is active once the client publishes a non-zero logical run.
	active := f.waitFrame(t, "active run snapshot", func(frame *pibridgepb.PiToPhi) bool {
		return bridgeRunID(frame) != 0
	})
	runID := bridgeRunID(active)

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "req-model", Provider: "anthropic", Model: "local-next"},
	}}
	model := f.waitFrame(t, "set-model ack", func(frame *pibridgepb.PiToPhi) bool {
		ack := frame.GetSetModelAck()
		return ack != nil && ack.GetRequestId() == "req-model"
	}).GetSetModelAck()
	if !model.GetOk() {
		t.Fatalf("model switch during the run was refused: %+v", model)
	}
	if model.GetReason() != "" {
		t.Fatalf("successful model switch carried reason %q", model.GetReason())
	}

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetThinkingRequest{
		SetThinkingRequest: &pibridgepb.SetThinkingRequest{RequestId: "req-thinking", Level: "high", Provider: "anthropic", Model: "local-next"},
	}}
	thinking := f.waitFrame(t, "set-thinking ack", func(frame *pibridgepb.PiToPhi) bool {
		ack := frame.GetSetThinkingAck()
		return ack != nil && ack.GetRequestId() == "req-thinking"
	}).GetSetThinkingAck()
	if !thinking.GetOk() {
		t.Fatalf("thinking switch during the run was refused: %+v", thinking)
	}

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_StateRequest{
		StateRequest: &pibridgepb.StateRequest{RequestId: "req-state"},
	}}
	state := f.waitFrame(t, "state reply", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetStateSnapshot().GetRequestId() == "req-state"
	}).GetStateSnapshot()
	if got := state.GetModel().GetModel(); got != "local-next" {
		t.Fatalf("state model = %q, want local-next", got)
	}
	if got := state.GetThinkingLevel(); got != "high" {
		t.Fatalf("state thinking = %q, want high", got)
	}

	// Request one was held the whole time: its selection must be unchanged
	// and the tool result must reach request two with the new settings.
	release <- struct{}{}
	second := liveWaitBody(t, bodies)
	if second["model"] != "local-next" {
		t.Fatalf("second request model = %v, want local-next", second["model"])
	}
	budget, ok := second["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("second request has no thinking config: %v", second["thinking"])
	}
	if budget["type"] != "enabled" || budget["budget_tokens"] != float64(8192) {
		t.Fatalf("second request thinking = %v, want enabled/8192", budget)
	}
	rawSecond, _ := json.Marshal(second)
	if !strings.Contains(string(rawSecond), "SYNTHETIC_READ_RESULT") {
		t.Fatalf("second request lacks the tool result: %s", rawSecond)
	}

	release <- struct{}{}
	q.wait("DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()

	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (no cancellation or resubmission)", got)
	}
	if got := cancelled.Load(); got != 0 {
		t.Fatalf("provider saw %d cancellations, want none", got)
	}
	liveAssertOneUserTranscript(t, ws, agentDir)

	store, err := session.OpenFile(sessionFile(t, agentDir), ws, false)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.ModelState()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Provider != "anthropic" || saved.ModelID != "local-next" || saved.ThinkingLevel != "high" {
		t.Fatalf("saved selection = %s/%s thinking %q, want anthropic/local-next thinking high",
			saved.Provider, saved.ModelID, saved.ThinkingLevel)
	}

	// One logical run for the whole test: a re-ask would open a second.
	f.mu.Lock()
	frames := append([]*pibridgepb.PiToPhi(nil), f.frames...)
	f.mu.Unlock()
	for _, frame := range frames {
		if id := bridgeRunID(frame); id != 0 && id != runID {
			t.Fatalf("bridge observed run %d, want the original run %d", id, runID)
		}
	}
	t.Logf("live settings: first=%v second=%v thinking=%v", first["model"], second["model"], budget)
}

// A skill steered into a held run and a live model switch both land in the
// next request exactly once, with the expanded block and its provenance.
func TestBridgeSkillSteeringSurvivesLiveSettings(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("live bridge settings need a Unix PTY")
	}
	bodies := make(chan map[string]any, 4)
	release := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		fmt.Fprint(w, liveTextTurn("SKILLED_DONE"))
	}))
	t.Cleanup(server.Close)

	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[`+
		`{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"input":["text"]},`+
		`{"id":"local-next","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)
	writeAppFile(t, filepath.Join(agentDir, "skills", "demo", "SKILL.md"), "---\nname: demo\ndescription: Demo skill\n---\nSKILL_BODY_MARKER\n")

	f := startBridgePhi(t)
	q := startReviewProcessEnv(t, home, ws, agentDir, *reviewBinary, "", bridgeChildEnv(t, f), "--deny-project", "--no-tools")
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})

	q.prompt()
	q.write("original work\r")
	first := liveWaitBody(t, bodies)
	if first["model"] != "local-model" {
		t.Fatalf("first model = %v", first["model"])
	}
	f.waitFrame(t, "active run", func(frame *pibridgepb.PiToPhi) bool {
		return bridgeRunID(frame) != 0
	})

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_InvokeSkillRequest{
		InvokeSkillRequest: &pibridgepb.InvokeSkillRequest{RequestId: "req-skill", Name: "demo"},
	}}
	skillAck := f.waitFrame(t, "skill ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetInvokeSkillAck().GetRequestId() == "req-skill"
	}).GetInvokeSkillAck()
	if !skillAck.GetOk() {
		t.Fatalf("skill steer = %+v", skillAck)
	}

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "req-model", Provider: "anthropic", Model: "local-next"},
	}}
	model := f.waitFrame(t, "set-model ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetModelAck().GetRequestId() == "req-model"
	}).GetSetModelAck()
	if !model.GetOk() {
		t.Fatalf("model switch = %+v", model)
	}

	release <- struct{}{}
	second := liveWaitBody(t, bodies)
	if second["model"] != "local-next" {
		t.Fatalf("second model = %v", second["model"])
	}
	secondText := requestText(t, second)
	if got := strings.Count(secondText, "SKILL_BODY_MARKER"); got != 1 {
		t.Fatalf("skill body occurrences = %d, want 1 in %q", got, secondText)
	}
	if got := strings.Count(secondText, `<skill name="demo"`); got != 1 {
		t.Fatalf("skill provenance occurrences = %d, want 1 in %q", got, secondText)
	}
	if strings.Contains(requestText(t, first), "SKILL_BODY_MARKER") {
		t.Fatal("skill reached the request already in flight")
	}
	release <- struct{}{}
	q.wait("SKILLED_DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()
}

// An attachment queued into a held run survives a compatible model switch and
// reaches the next request once, with text and image together.
func TestBridgeAttachmentSteeringSurvivesLiveSettings(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("live bridge settings need a Unix PTY")
	}
	bodies := make(chan map[string]any, 4)
	release := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		fmt.Fprint(w, liveTextTurn("ATTACHED_DONE"))
	}))
	t.Cleanup(server.Close)

	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[`+
		`{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"input":["text"]},`+
		`{"id":"local-image","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"input":["text","image"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)
	root := t.TempDir()
	data := writeAttachmentPNG(t, root)

	f := startBridgePhi(t)
	q := startReviewProcessEnv(t, home, ws, agentDir, *reviewBinary, "", append(bridgeChildEnv(t, f), "PHI_PI_ATTACHMENT_ROOT="+root), "--deny-project", "--no-tools")
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})

	q.prompt()
	q.write("original work\r")
	first := liveWaitBody(t, bodies)
	if first["model"] != "local-model" {
		t.Fatalf("first model = %v", first["model"])
	}
	f.waitFrame(t, "active run", func(frame *pibridgepb.PiToPhi) bool {
		return bridgeRunID(frame) != 0
	})

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "req-image-model", Provider: "anthropic", Model: "local-image"},
	}}
	if ack := f.waitFrame(t, "set-model ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetModelAck().GetRequestId() == "req-image-model"
	}).GetSetModelAck(); !ack.GetOk() {
		t.Fatalf("image model switch = %+v", ack)
	}
	held := bridgeState(t, f, "attach-held")

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_AttachmentPromptRequest{
		AttachmentPromptRequest: &pibridgepb.AttachmentPromptRequest{
			RequestId:         "41",
			ExpectedSessionId: held.GetSessionId(),
			ExpectedLeafId:    held.GetLeafId(),
			ExpectedModel:     &pibridgepb.ModelSnapshot{Provider: "anthropic", Model: "local-image"},
			Text:              "look here",
			Attachments:       []*pibridgepb.AttachmentRef{attachmentImageRef("a.png", data)},
		},
	}}
	att := attachmentAck(t, f, "41")
	if att.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_DISPATCHED || att.GetReason() != "" {
		t.Fatalf("attachment = %v %q, want DISPATCHED", att.GetOutcome(), att.GetReason())
	}

	release <- struct{}{}
	second := liveWaitBody(t, bodies)
	rawSecond, _ := json.Marshal(second)
	if second["model"] != "local-image" {
		t.Fatalf("second model = %v", second["model"])
	}
	if got := strings.Count(string(rawSecond), `"type":"image"`); got != 1 {
		t.Fatalf("image blocks = %d, want 1 in %s", got, rawSecond)
	}
	if got := strings.Count(requestText(t, second), "look here"); got != 1 {
		t.Fatalf("attachment text occurrences = %d, want 1", got)
	}
	release <- struct{}{}
	q.wait("ATTACHED_DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()
}
