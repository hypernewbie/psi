//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	pibridgepb "psi/internal/pibridge/pb"
)

func TestBridgeProcessLiveSettingsNextRequest(t *testing.T) {
	bodies := make(chan map[string]any, 4)
	release := make(chan struct{}, 2)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		turn := int(requests.Add(1))
		select {
		case bodies <- body:
		case <-req.Context().Done():
			return
		}
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, liveThinkingTurn(turn))
	}))
	defer server.Close()
	defer close(release)

	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(ws, "fixture.txt"), "BRIDGE_READ_RESULT")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":["read"],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"api":"anthropic-messages","baseUrl":"`+server.URL+`","models":[`+
		`{"id":"local-model","contextWindow":65536,"maxTokens":16384,"reasoning":true},`+
		`{"id":"local-next","contextWindow":65536,"maxTokens":16384,"reasoning":true}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	f := startBridgePhi(t)
	q := startReviewProcessEnv(t, home, ws, agentDir, *reviewBinary, "", []string{
		"PHI_PI_BRIDGE_ENDPOINT=" + f.target,
		"PHI_PI_BRIDGE_PANE_ID=pane-app",
		"PHI_PI_BRIDGE_PROCESS_ID=proc-app",
		"PHI_PI_BRIDGE_TOKEN=" + strings.Repeat("ab", 32),
		"TMPDIR=" + os.TempDir(),
	}, "--deny-project", "--tools", "read")
	q.prompt()
	q.write("read fixture.txt\r")
	first := liveWaitBody(t, bodies)
	if first["model"] != "local-model" || first["thinking"] != nil {
		t.Fatalf("first request: model=%v thinking=%v, want local-model/off", first["model"], first["thinking"])
	}
	started := f.waitFrame(t, "run start", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetAgentStarted() != nil
	}).GetAgentStarted()

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "live-model", Provider: "anthropic", Model: "local-next"},
	}}
	model := f.waitFrame(t, "set-model ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetModelAck().GetRequestId() == "live-model"
	}).GetSetModelAck()
	if !model.GetOk() || model.GetReason() != "" {
		t.Fatalf("model switch during the run was refused: %v", model)
	}
	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetThinkingRequest{
		SetThinkingRequest: &pibridgepb.SetThinkingRequest{RequestId: "live-thinking", Provider: "anthropic", Model: "local-next", Level: "high"},
	}}
	thinking := f.waitFrame(t, "set-thinking ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetThinkingAck().GetRequestId() == "live-thinking"
	}).GetSetThinkingAck()
	if !thinking.GetOk() || thinking.GetReason() != "" {
		t.Fatalf("thinking switch during the run was refused: %v", thinking)
	}

	release <- struct{}{}
	second := liveWaitBody(t, bodies)
	budget, _ := second["thinking"].(map[string]any)
	if second["model"] != "local-next" || budget["budget_tokens"] != float64(8192) {
		t.Fatalf("second request: model=%v thinking=%v, want local-next/high", second["model"], second["thinking"])
	}
	messages, err := json.Marshal(second["messages"])
	if err != nil || !strings.Contains(string(messages), "BRIDGE_READ_RESULT") {
		t.Fatalf("second request lost the active run's tool result: %s (%v)", messages, err)
	}
	release <- struct{}{}
	settled := f.waitFrame(t, "run settlement", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetAgentSettled() != nil
	}).GetAgentSettled()
	if settled.GetRunId() != started.GetRunId() || settled.GetOutcome() != pibridgepb.RunOutcome_RUN_OUTCOME_COMPLETED {
		t.Fatalf("run did not settle successfully under its original ID %d: %v", started.GetRunId(), settled)
	}
	q.prompt()
	q.write("/quit\r")
	q.finish()
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want two without a replay", got)
	}
	if saved := liveAssertOneUserTranscript(t, ws, agentDir); saved.Provider != "anthropic" || saved.ModelID != "local-next" || saved.ThinkingLevel != "high" {
		t.Fatalf("saved selection = %+v, want anthropic/local-next/high", saved)
	}
}
