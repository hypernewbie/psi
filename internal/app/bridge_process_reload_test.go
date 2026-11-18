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
	"time"

	pibridgepb "psi/internal/pibridge/pb"
	"psi/internal/session"
)

// bridgeState asks for one correlated state snapshot. Frame scans are
// cumulative, so callers pass a request id that has not been used before.
// waitReloadRetired waits for the latch to clear while the retry is still
// running, which proves retirement happened at admission, not completion.
func waitReloadRetired(t *testing.T, f *bridgePhi) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		snap := bridgeState(t, f, fmt.Sprintf("state-retired-%d", i))
		if !snap.GetReloadReadiness().GetReloadPending() {
			if got := snap.GetReloadReadiness().GetHighWater(); got < 31 {
				t.Fatalf("high water = %d, want at least the dispatched id 31", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("reload latch survived the bump retry admission")
}

func waitBridgeIdle(t *testing.T, f *bridgePhi) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		if bridgeState(t, f, fmt.Sprintf("state-idle-%d", i)).GetReloadReadiness().GetIdle() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("bridge never reported idle after the retry")
}

func bridgeState(t *testing.T, f *bridgePhi, id string) *pibridgepb.StateSnapshot {
	t.Helper()
	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_StateRequest{
		StateRequest: &pibridgepb.StateRequest{RequestId: id},
	}}
	return f.waitFrame(t, "state "+id, func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetStateSnapshot().GetRequestId() == id
	}).GetStateSnapshot()
}

// nextBridgeReload returns the next unread reload ack, so the dispatched and
// duplicate answers for one id cannot be confused.
func nextBridgeReload(t *testing.T, f *bridgePhi, cursor *int) *pibridgepb.ReloadAck {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for *cursor < len(f.frames) {
			frame := f.frames[*cursor]
			*cursor++
			if ack := frame.GetReloadAck(); ack != nil {
				f.mu.Unlock()
				return ack
			}
		}
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("reload ack did not arrive")
	return nil
}

// TestBridgeProcessBusyReloadSettlesBeforeRetry proves B1 on a built Psi:
// a stale identity cannot cancel, a fresh busy identity owns the native
// interrupt once, the settled run reloads and replays the original prompt,
// and the latch retires after the retry is admitted.
func TestBridgeProcessBusyReloadSettlesBeforeRetry(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("busy reload needs a Unix PTY")
	}
	bodies := make(chan map[string]any, 8)
	releaseRetry := make(chan struct{})
	var requests, cancelled atomic.Int32
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
		if requests.Load() == 1 {
			// The original run streams a partial, then holds until reload.
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"RELOAD_PARTIAL\"}}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-req.Context().Done():
				cancelled.Add(1)
				return
			}
		}
		// The retry stays held until the file and duplicate checks finish.
		select {
		case <-releaseRetry:
		case <-req.Context().Done():
			cancelled.Add(1)
			return
		}
		fmt.Fprint(w, liveTextTurn("RETRIED_DONE"))
	}))
	t.Cleanup(server.Close)

	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)
	writeAppFile(t, filepath.Join(ws, "AGENTS.md"), "# Instructions\n\nINITIAL_INSTRUCTIONS_MARKER\n")

	f := startBridgePhi(t)
	q := startReviewProcessEnv(t, home, ws, agentDir, *reviewBinary, "", bridgeChildEnv(t, f), "--deny-project", "--no-tools")
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})

	q.prompt()
	q.write("original work\r")
	q.wait("RELOAD_PARTIAL")
	first := liveWaitBody(t, bodies)

	// One distinct steering message and one follow-up: both are still
	// undelivered when the run is cancelled.
	q.write("STEER_MARKER\r")
	q.wait("STEER_MARKER")
	q.write("FOLLOW_MARKER\x1b\r")
	q.wait("FOLLOW_MARKER")

	held := bridgeState(t, f, "state-held")
	if held.GetReloadReadiness().GetReloadPending() {
		t.Fatal("fresh run already reads reload-pending")
	}
	runID := held.GetReloadReadiness().GetRunId()
	ctxRev := held.GetReloadReadiness().GetContextRevision()
	if runID == 0 || ctxRev == 0 {
		t.Fatalf("held identity = run %d ctx %d, want both present", runID, ctxRev)
	}

	// A stale run id cannot cancel: the frame is rejected before dispatch.
	oldRun := runID - 1
	stale := reloadRequest("1", held.GetSessionId(), held.GetLeafId(), held.GetModel().GetProvider(), held.GetModel().GetModel(), &oldRun, &ctxRev)
	f.inject <- stale
	cursor := 0
	if ack := nextBridgeReload(t, f, &cursor); ack.GetReason() != "stale" || ack.GetOutcome() != pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_REFUSED {
		t.Fatalf("stale reload = %+v, want stale refusal", ack)
	}
	if held2 := bridgeState(t, f, "state-after-stale"); held2.GetReloadReadiness().GetRunId() != runID {
		t.Fatalf("stale frame replaced the run: %+v", held2.GetReloadReadiness())
	}
	select {
	case body := <-bodies:
		t.Fatalf("stale frame reached the provider: %v", body["model"])
	default:
	}

	// The retry must read the instructions as they are at settlement.
	writeAppFile(t, filepath.Join(ws, "AGENTS.md"), "# Instructions\n\nUPDATED_INSTRUCTIONS_MARKER\n")

	valid := reloadRequest("2", held.GetSessionId(), held.GetLeafId(), held.GetModel().GetProvider(), held.GetModel().GetModel(), &runID, &ctxRev)
	f.inject <- valid
	if ack := nextBridgeReload(t, f, &cursor); ack.GetOutcome() != pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_DISPATCHED || ack.GetReason() != "" {
		t.Fatalf("busy reload = %+v, want DISPATCHED", ack)
	}

	// The retry request is the proof that settlement, reload, and replay ran.
	retry := liveWaitBody(t, bodies)
	retryRaw, _ := json.Marshal(retry)
	if !strings.Contains(string(retryRaw), "original work") {
		t.Fatalf("retry request lacks the original input: %s", retryRaw)
	}
	if !strings.Contains(requestSystem(t, retry), "UPDATED_INSTRUCTIONS_MARKER") {
		t.Fatalf("retry system prompt lacks the reloaded instructions")
	}
	retryText := requestText(t, retry)
	for _, marker := range []string{"STEER_MARKER", "FOLLOW_MARKER"} {
		if strings.Contains(retryText, marker) {
			t.Fatalf("stranded %s reached the retry: %q", marker, retryText)
		}
	}

	// History: original user, interrupted partial, repeated original user.
	raw, err := os.ReadFile(sessionFile(t, agentDir))
	if err != nil {
		t.Fatal(err)
	}
	history := string(raw)
	if got := strings.Count(history, "original work"); got != 2 {
		t.Fatalf("original user records = %d, want exactly 2:\n%s", got, history)
	}
	firstAt := strings.Index(history, "original work")
	partialAt := strings.Index(history, "RELOAD_PARTIAL")
	lastAt := strings.LastIndex(history, "original work")
	if firstAt < 0 || partialAt < 0 || !(firstAt < partialAt && partialAt < lastAt) {
		t.Fatalf("history order = original %d, partial %d, repeated %d", firstAt, partialAt, lastAt)
	}

	// The latch retires after the retry admission, before the retry ends.
	settled := bridgeState(t, f, "state-retry")
	if settled.GetReloadReadiness().GetReloadPending() {
		t.Fatal("reload latch survived the retry admission")
	}
	if got := settled.GetReloadReadiness().GetHighWater(); got < 2 {
		t.Fatalf("reload high water = %d, want at least the dispatched id 2", got)
	}

	// Replaying the accepted id against the retry identity is a duplicate:
	// it neither cancels nor reloads again.
	dupRun := settled.GetReloadReadiness().GetRunId()
	dupRev := settled.GetReloadReadiness().GetContextRevision()
	duplicate := reloadRequest("2", settled.GetSessionId(), settled.GetLeafId(), settled.GetModel().GetProvider(), settled.GetModel().GetModel(), &dupRun, &dupRev)
	f.inject <- duplicate
	if ack := nextBridgeReload(t, f, &cursor); ack.GetReason() != "duplicate" || ack.GetOutcome() != pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_REFUSED {
		t.Fatalf("replayed reload = %+v, want duplicate", ack)
	}

	releaseRetry <- struct{}{}
	q.wait("RETRIED_DONE")
	// Both stranded messages are back in the editor, not in the retry.
	q.waitScreen("STEER_MARKER")
	q.waitScreen("FOLLOW_MARKER")
	waitBridgeIdle(t, f)
	q.prompt()
	q.write("\x03") // idle Ctrl+C clears the restored draft
	q.write("/quit\r")
	q.finish()

	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want original plus one retry", got)
	}
	if got := cancelled.Load(); got == 0 {
		t.Fatal("original request was never cancelled")
	}
	if first["model"] != "local-model" {
		t.Fatalf("original model = %v", first["model"])
	}
}

// A busy reload on a bump run retries the bump, not a prompt: no user record
// is appended and the retry stays in bump mode.
func TestBridgeProcessBusyReloadBumpRetry(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("busy reload needs a Unix PTY")
	}
	bodies := make(chan map[string]any, 4)
	release := make(chan struct{})
	var requests atomic.Int32
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
		if requests.Load() == 1 {
			select {
			case <-req.Context().Done():
				return
			}
		}
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		fmt.Fprint(w, liveTextTurn("BUMP_DONE"))
	}))
	t.Cleanup(server.Close)

	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed context")); err != nil {
		t.Fatal(err)
	}

	f := startBridgePhi(t)
	q := startReviewProcessEnv(t, home, ws, agentDir, *reviewBinary, "", bridgeChildEnv(t, f), "--deny-project", "--no-tools", "--session", store.File())
	t.Cleanup(func() {
		if output := q.raw.String(); strings.Contains(output, "WARNING: DATA RACE") {
			t.Errorf("race detector report in app subprocess:\n%s", output)
		}
	})

	q.prompt()
	q.write("/bump\r")
	if body := liveWaitBody(t, bodies); len(body) == 0 {
		t.Fatal("bump sent no request")
	}
	held := bridgeState(t, f, "bump-held")
	runID := held.GetReloadReadiness().GetRunId()
	if runID == 0 {
		t.Fatal("bump run did not bind a live run id")
	}

	f.inject <- reloadRequest("31", held.GetSessionId(), held.GetLeafId(), held.GetModel().GetProvider(), held.GetModel().GetModel(), &runID, nil)
	cursor := 0
	if ack := nextBridgeReload(t, f, &cursor); ack.GetOutcome() != pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_DISPATCHED {
		t.Fatalf("bump reload = %+v, want DISPATCHED", ack)
	}
	if retry := liveWaitBody(t, bodies); len(retry) == 0 {
		t.Fatal("bump retry sent no request")
	}
	raw, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(raw), "seed context"); got != 1 {
		t.Fatalf("seed user records = %d, want 1 (a bump retry adds none)", got)
	}
	waitReloadRetired(t, f)
	release <- struct{}{}
	q.wait("BUMP_DONE")
	waitBridgeIdle(t, f)
	q.prompt()
	q.write("/quit\r")
	q.finish()

	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want the bump and its retry", got)
	}
}
