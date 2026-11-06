//go:build !windows

package app

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"psi/internal/config"
	"psi/internal/pibridge"
	pibridgepb "psi/internal/pibridge/pb"
	"psi/internal/provider"
	"psi/internal/terminal"
)

// bridgePhi is the smallest Phi the app tests need: it accepts the handshake
// and injects capability requests on demand.
type bridgePhi struct {
	pibridgepb.UnimplementedIntegrationServer

	target string

	mu     sync.Mutex
	hellos []*pibridgepb.PiToPhi
	frames []*pibridgepb.PiToPhi
	inject chan *pibridgepb.PhiToPi
}

func startBridgePhi(t *testing.T) *bridgePhi {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bridge.sock")
	if len(path) >= 100 {
		t.Skipf("socket path too long for this host: %d bytes", len(path))
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix listener unavailable: %v", err)
	}
	f := &bridgePhi{target: "unix:" + path, inject: make(chan *pibridgepb.PhiToPi, 8)}
	server := grpc.NewServer()
	pibridgepb.RegisterIntegrationServer(server, f)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	return f
}

func (f *bridgePhi) Connect(stream pibridgepb.Integration_ConnectServer) error {
	hello, err := stream.Recv()
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.hellos = append(f.hellos, hello)
	f.mu.Unlock()
	if err := stream.Send(&pibridgepb.PhiToPi{
		PaneId: hello.GetPaneId(), ProcessId: hello.GetProcessId(),
		RuntimeGeneration: hello.GetRuntimeGeneration(),
		Payload: &pibridgepb.PhiToPi_Accepted{Accepted: &pibridgepb.Accepted{
			ProtocolVersion:   1,
			Capabilities:      hello.GetHello().GetCapabilities(),
			RuntimeGeneration: hello.GetRuntimeGeneration(),
			RuntimeId:         hello.GetHello().GetRuntimeId(),
		}},
	}); err != nil {
		return err
	}
	frames := make(chan *pibridgepb.PiToPhi, 4)
	recvErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			frame, err := stream.Recv()
			if err != nil {
				select {
				case recvErr <- err:
				default:
				}
				return
			}
			select {
			case frames <- frame:
			case <-done:
				return
			}
		}
	}()
	for {
		select {
		case err := <-recvErr:
			return err
		case frame := <-frames:
			f.mu.Lock()
			f.frames = append(f.frames, frame)
			f.mu.Unlock()
		case msg := <-f.inject:
			msg.PaneId, msg.ProcessId = hello.GetPaneId(), hello.GetProcessId()
			msg.RuntimeGeneration = hello.GetRuntimeGeneration()
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

func (f *bridgePhi) waitFrame(t *testing.T, what string, pred func(*pibridgepb.PiToPhi) bool) *pibridgepb.PiToPhi {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		frames := append([]*pibridgepb.PiToPhi(nil), f.frames...)
		f.mu.Unlock()
		for _, frame := range frames {
			if pred(frame) {
				return frame
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return nil
}

// startBridgeRuntime wires a provisioned client to r, the way Run does.
func startBridgeRuntime(t *testing.T, r *runtime) (*bridgePhi, *bridgeHandler) {
	t.Helper()
	f := startBridgePhi(t)
	t.Setenv("PHI_PI_BRIDGE_ENDPOINT", f.target)
	t.Setenv("PHI_PI_BRIDGE_PANE_ID", "pane-app")
	t.Setenv("PHI_PI_BRIDGE_PROCESS_ID", "proc-app")
	t.Setenv("PHI_PI_BRIDGE_TOKEN", strings.Repeat("ab", 32))
	r.generation = 1
	// Run settles the runtime into StateIdle before the loop starts.
	r.state = StateIdle
	r.syncBridge()
	if r.bridge == nil {
		t.Fatal("bridge was not provisioned")
	}
	t.Cleanup(r.closeBridge)
	return f, newBridgeHandler(r, nil)
}

// nextRequest returns the next request the client forwarded.
func nextRequest(t *testing.T, c *pibridge.Client) pibridge.Request {
	t.Helper()
	select {
	case req := <-c.Requests():
		return req
	case <-time.After(10 * time.Second):
		t.Fatal("request was not forwarded to the app")
	}
	return pibridge.Request{}
}

// addModel registers one more selectable model in the runtime catalog.
func addModel(r *runtime, providerID string, meta config.ModelMeta) {
	providerMeta := r.models.Providers[providerID]
	providerMeta.Models = append(providerMeta.Models, meta)
	r.models.Providers[providerID] = providerMeta
}

func TestBridgeModelsListReportsOnlySelectableEntries(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	// testRuntime clears ANTHROPIC_API_KEY; restore it so the provider is
	// credentialed, and openai-codex stays without its OAuth login.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-bridge-test")
	addModel(r, "anthropic", config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	f, h := startBridgeRuntime(t, r)

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_ModelsRequest{
		ModelsRequest: &pibridgepb.ModelsRequest{RequestId: "req-list"},
	}}
	h.handle(nextRequest(t, r.bridge), false)

	frame := f.waitFrame(t, "models response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetModelsResponse() != nil
	})
	res := frame.GetModelsResponse()
	if res.GetRequestId() != "req-list" || res.GetUnavailable() {
		t.Fatalf("list failed: %+v", res)
	}
	seen := map[string]bool{}
	for _, entry := range res.GetModels() {
		if entry.GetProvider() == "" || entry.GetModel() == "" {
			t.Fatalf("catalog entry is not selectable: %+v", entry)
		}
		// The catalog also ships openai-codex models, but the fixture agent
		// directory has no OAuth login: an uncredentialed provider must not be
		// offered, or every switch to it would fail at request time.
		if entry.GetProvider() != "anthropic" {
			t.Fatalf("uncredentialed provider was listed: %+v", entry)
		}
		seen[entry.GetModel()] = true
	}
	if !seen["m"] || !seen["m2"] {
		t.Fatalf("catalog is missing selectable models: %v", seen)
	}
}

// A switch must persist before it is acknowledged, and the published snapshot
// must follow it. Both are the user-visible result Phi renders.
func TestBridgeSetModelPersistsThenAcknowledges(t *testing.T) {
	r := testRuntime(t)
	addModel(r, "anthropic", config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-bridge-test")
	f, h := startBridgeRuntime(t, r)

	before := countEntries(t, r)

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "req-set", Provider: "anthropic", Model: "m2"},
	}}
	h.handle(nextRequest(t, r.bridge), false)
	if !h.busy() {
		t.Fatal("switch did not start a preparation")
	}
	select {
	case <-h.completed():
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not finish")
	}
	h.finishPreparation(false)

	frame := f.waitFrame(t, "set-model ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetModelAck() != nil
	})
	ack := frame.GetSetModelAck()
	if ack.GetRequestId() != "req-set" || !ack.GetOk() || ack.GetReason() != "" {
		t.Fatalf("switch was not acknowledged: %+v", ack)
	}
	if r.ov.Provider != "anthropic" || r.ov.Model != "m2" {
		t.Fatalf("installed configuration is %s/%s", r.ov.Provider, r.ov.Model)
	}
	if got := countEntries(t, r) - before; got != 1 {
		t.Fatalf("switch persisted %d entries, want 1", got)
	}

	// The bridge snapshot follows the session without a separate publish.
	snap := f.waitFrame(t, "model snapshot", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetStateSnapshot().GetModel().GetModel() == "m2"
	})
	if snap.GetStateSnapshot().GetModel().GetProvider() != "anthropic" {
		t.Fatalf("published provider mismatch: %+v", snap.GetStateSnapshot())
	}
}

// Unknown entries are refused without touching the session or the provider.
func TestBridgeSetModelUnknownLeavesSessionUntouched(t *testing.T) {
	r := testRuntime(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-bridge-test")
	f, h := startBridgeRuntime(t, r)

	before := countEntries(t, r)
	oldModel := r.ov.Model

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "req-unknown", Provider: "anthropic", Model: "nope"},
	}}
	h.handle(nextRequest(t, r.bridge), false)
	if h.busy() {
		t.Fatal("unknown model started a preparation")
	}

	ack := f.waitFrame(t, "refusal", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetModelAck() != nil
	}).GetSetModelAck()
	if ack.GetOk() || ack.GetReason() != "unknown-model" {
		t.Fatalf("unexpected refusal: %+v", ack)
	}
	if r.ov.Model != oldModel || countEntries(t, r) != before {
		t.Fatal("refused switch changed the runtime or the session")
	}
}

// A missing credential is reported as no-auth, and it is not a switch.
func TestBridgeSetModelWithoutCredentialRefuses(t *testing.T) {
	r := testRuntime(t)
	addModel(r, "anthropic", config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	f, h := startBridgeRuntime(t, r)

	before := countEntries(t, r)
	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "req-noauth", Provider: "anthropic", Model: "m2"},
	}}
	h.handle(nextRequest(t, r.bridge), false)
	select {
	case <-h.completed():
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not finish")
	}
	h.finishPreparation(false)

	ack := f.waitFrame(t, "no-auth refusal", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetModelAck() != nil
	}).GetSetModelAck()
	if ack.GetOk() || ack.GetReason() != "no-auth" {
		t.Fatalf("unexpected refusal: %+v", ack)
	}
	if r.ov.Model != "m" || countEntries(t, r) != before {
		t.Fatal("refused switch changed the runtime or the session")
	}
}

// A running turn owns the configuration. The switch is refused before any
// candidate work, so nothing can race the active provider.
func TestBridgeSetModelRefusedWhileBusy(t *testing.T) {
	r := testRuntime(t)
	addModel(r, "anthropic", config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-bridge-test")
	f, h := startBridgeRuntime(t, r)

	before := countEntries(t, r)
	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "req-busy", Provider: "anthropic", Model: "m2"},
	}}
	h.handle(nextRequest(t, r.bridge), true)
	if h.busy() {
		t.Fatal("busy runtime started a preparation")
	}
	ack := f.waitFrame(t, "busy refusal", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetModelAck() != nil
	}).GetSetModelAck()
	if ack.GetOk() || ack.GetReason() != "" {
		t.Fatalf("busy refusal must leave the outcome unknown: %+v", ack)
	}
	if r.ov.Model != "m" || countEntries(t, r) != before {
		t.Fatal("refused switch changed the runtime or the session")
	}
}

// A native change while the candidate is building invalidates it: installing
// it would undo the user's own command.
func TestBridgeSetModelCandidateInvalidatedByNativeChange(t *testing.T) {
	r := testRuntime(t)
	addModel(r, "anthropic", config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	addModel(r, "anthropic", config.ModelMeta{ID: "m3", Reasoning: true, MaxTokens: 16384})
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-bridge-test")
	f, h := startBridgeRuntime(t, r)

	before := countEntries(t, r)
	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetModelRequest{
		SetModelRequest: &pibridgepb.SetModelRequest{RequestId: "req-race", Provider: "anthropic", Model: "m2"},
	}}
	h.handle(nextRequest(t, r.bridge), false)
	// A native /model lands while the candidate is being configured.
	if _, err := r.command(&terminal.Command{Name: "model", Arg: "anthropic/m3"}, io.Discard); err != nil {
		t.Fatalf("native model change failed: %v", err)
	}
	select {
	case <-h.completed():
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not finish")
	}
	h.finishPreparation(false)

	ack := f.waitFrame(t, "stale refusal", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetModelAck() != nil
	}).GetSetModelAck()
	if ack.GetOk() {
		t.Fatal("stale candidate was installed over the native change")
	}
	if r.ov.Model != "m3" {
		t.Fatalf("native change was lost: %s", r.ov.Model)
	}
	// Only the native switch persisted.
	if got := countEntries(t, r) - before; got != 1 {
		t.Fatalf("persisted %d entries, want only the native switch", got)
	}
}

func countEntries(t *testing.T, r *runtime) int {
	t.Helper()
	proj, err := r.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	return len(proj.Entries)
}

func historyRequest(id string) *pibridgepb.PhiToPi {
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_HistoryRequest{
		HistoryRequest: &pibridgepb.HistoryRequest{RequestId: id},
	}}
}

func thinkingRequest(id string) *pibridgepb.PhiToPi {
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_ThinkingLevelsRequest{
		ThinkingLevelsRequest: &pibridgepb.ThinkingLevelsRequest{RequestId: id},
	}}
}

func setThinking(id, level, providerID, model string) *pibridgepb.PhiToPi {
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SetThinkingRequest{
		SetThinkingRequest: &pibridgepb.SetThinkingRequest{
			RequestId: id, Level: level, Provider: providerID, Model: model,
		},
	}}
}

// The editor is loop-owned, so recall is read there and never re-ordered by
// the wire. Phi renders the first entry as the newest prompt.
func TestBridgeHistoryIsNewestFirstAndChangesNothing(t *testing.T) {
	r := testRuntime(t)
	f, h := startBridgeRuntime(t, r)
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output strings.Builder
	console := terminal.NewConsole(input, &output)
	defer console.Close()
	console.Editor.SetHistory([]string{"oldest", "middle", "newest"})
	console.Editor.RestoreDraft("keep this draft", 5)
	h.console = console

	f.inject <- historyRequest("req-history")
	h.handle(nextRequest(t, r.bridge), false)

	res := f.waitFrame(t, "history response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetHistoryResponse() != nil
	}).GetHistoryResponse()
	if res.GetUnavailable() {
		t.Fatal("history answered unavailable")
	}
	if got := res.GetEntries(); len(got) != 3 || got[0] != "newest" || got[2] != "oldest" {
		t.Fatalf("history order: %v", got)
	}
	if console.Editor.Text() != "keep this draft" || console.Editor.Pos() != 5 {
		t.Fatal("history read changed the draft or cursor")
	}
}

// A recall answer beyond the message bound is wholly unavailable: a trimmed
// list would silently drop prompts Phi shows as selectable.
func TestBridgeHistoryOverBoundIsUnavailable(t *testing.T) {
	r := testRuntime(t)
	f, h := startBridgeRuntime(t, r)
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output strings.Builder
	console := terminal.NewConsole(input, &output)
	defer console.Close()
	big := strings.Repeat("x", bridgeHistoryBytes)
	console.Editor.SetHistory([]string{big, "more"})
	h.console = console

	f.inject <- historyRequest("req-big")
	h.handle(nextRequest(t, r.bridge), false)

	res := f.waitFrame(t, "oversized history response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetHistoryResponse() != nil
	}).GetHistoryResponse()
	if !res.GetUnavailable() || len(res.GetEntries()) != 0 {
		t.Fatalf("oversized history was trimmed instead of refused: %d entries", len(res.GetEntries()))
	}
}

// The list must describe the live model, so Phi can render exactly the levels
// a set request will accept.
func TestBridgeThinkingListMatchesLiveModel(t *testing.T) {
	r := testRuntime(t)
	f, h := startBridgeRuntime(t, r)
	f.inject <- thinkingRequest("req-levels")
	h.handle(nextRequest(t, r.bridge), false)

	res := f.waitFrame(t, "thinking levels", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetThinkingLevelsResponse() != nil
	}).GetThinkingLevelsResponse()
	if res.GetUnavailable() || res.GetRequestId() != "req-levels" {
		t.Fatalf("level list failed: %+v", res)
	}
	if res.GetProvider() != r.ov.Provider || res.GetModel() != r.ov.Model {
		t.Fatalf("level list named the wrong model: %+v", res)
	}
	want := provider.SupportedThinking(r.cfg.Model)
	if len(res.GetLevels()) != len(want) {
		t.Fatalf("levels: got %v, want %v", res.GetLevels(), want)
	}
}

// A level the model can apply is applied even when it is not advertised: the
// provider clamps it, so a valid level is not a rejection.
func TestBridgeThinkingClampsUnadvertisedLevel(t *testing.T) {
	r := testRuntime(t)
	r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{}}
	t.Setenv("ZAI_API_KEY", "synthetic-zai-key")
	if err := r.configure(config.StartupOverrides{Provider: "zai", Model: "glm-5.3", Thinking: "high"}); err != nil {
		t.Fatal(err)
	}
	for _, advertised := range provider.SupportedThinking(r.cfg.Model) {
		if advertised == "minimal" {
			t.Skip("fixture model advertises minimal")
		}
	}
	f, h := startBridgeRuntime(t, r)
	before := countEntries(t, r)

	f.inject <- setThinking("req-clamped", "minimal", r.ov.Provider, r.ov.Model)
	h.handle(nextRequest(t, r.bridge), false)
	select {
	case <-h.completed():
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not finish")
	}
	h.finishPreparation(false)
	ack := f.waitFrame(t, "level clamp", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetThinkingAck() != nil
	}).GetSetThinkingAck()
	if !ack.GetOk() {
		t.Fatalf("unadvertised level was refused: %+v", ack)
	}
	if r.ov.Thinking != "minimal" {
		t.Fatalf("requested level not applied: %q", r.ov.Thinking)
	}
	if countEntries(t, r) != before+1 {
		t.Fatal("accepted level did not record a thinking change")
	}
}

// A level the model cannot apply is refused before any candidate work, so the
// bridge keeps answering with a known reason instead of an empty outcome.
func TestBridgeThinkingUnappliableLevelRefused(t *testing.T) {
	r := testRuntime(t)
	r.models.Providers["anthropic"] = config.ProviderMeta{Models: []config.ModelMeta{{ID: "m", Reasoning: false, MaxTokens: 16384}}}
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off"}); err != nil {
		t.Fatal(err)
	}
	f, h := startBridgeRuntime(t, r)
	before := countEntries(t, r)

	f.inject <- setThinking("req-unusable", "high", r.ov.Provider, r.ov.Model)
	h.handle(nextRequest(t, r.bridge), false)
	if h.busy() {
		t.Fatal("unappliable level started a preparation")
	}
	ack := f.waitFrame(t, "level refusal", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetThinkingAck() != nil
	}).GetSetThinkingAck()
	if ack.GetOk() || ack.GetReason() != "unsupported-level" {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	if countEntries(t, r) != before {
		t.Fatal("refused level changed the session")
	}
}

// A level the live model does not accept is refused before any candidate work,
// so a stale picker cannot install an unsupported level.
func TestBridgeThinkingUnsupportedLevelRefused(t *testing.T) {
	r := testRuntime(t)
	f, h := startBridgeRuntime(t, r)
	before := countEntries(t, r)

	f.inject <- setThinking("req-level", "ultra", r.ov.Provider, r.ov.Model)
	h.handle(nextRequest(t, r.bridge), false)
	if h.busy() {
		t.Fatal("unsupported level started a preparation")
	}
	ack := f.waitFrame(t, "level refusal", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetThinkingAck() != nil
	}).GetSetThinkingAck()
	if ack.GetOk() || ack.GetReason() != "unsupported-level" {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	if countEntries(t, r) != before {
		t.Fatal("refused level changed the session")
	}
}

// The request names the model its list came from. A session that moved on must
// refuse instead of applying the level to a different model.
func TestBridgeThinkingModelChangedRefused(t *testing.T) {
	r := testRuntime(t)
	f, h := startBridgeRuntime(t, r)
	before := countEntries(t, r)

	f.inject <- setThinking("req-moved", "high", r.ov.Provider, "some-other-model")
	h.handle(nextRequest(t, r.bridge), false)
	ack := f.waitFrame(t, "model-changed refusal", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetThinkingAck() != nil
	}).GetSetThinkingAck()
	if ack.GetOk() || ack.GetReason() != "model-changed" {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	if countEntries(t, r) != before || r.ov.Thinking != "off" {
		t.Fatal("stale thinking request changed the runtime")
	}
}

// A supported level persists and is installed, so the footer and Phi agree.
func TestBridgeThinkingSwitchPersists(t *testing.T) {
	r := testRuntime(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-bridge-test")
	f, h := startBridgeRuntime(t, r)
	levels := provider.SupportedThinking(r.cfg.Model)
	if len(levels) < 2 {
		t.Skipf("model supports %d thinking levels", len(levels))
	}
	level := levels[len(levels)-1]
	before := countEntries(t, r)

	f.inject <- setThinking("req-set", level, r.ov.Provider, r.ov.Model)
	h.handle(nextRequest(t, r.bridge), false)
	select {
	case <-h.completed():
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not finish")
	}
	h.finishPreparation(false)

	ack := f.waitFrame(t, "thinking ack", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSetThinkingAck() != nil
	}).GetSetThinkingAck()
	if !ack.GetOk() || ack.GetReason() != "" {
		t.Fatalf("level change refused: %+v", ack)
	}
	if r.ov.Thinking != level {
		t.Fatalf("thinking is %q, want %q", r.ov.Thinking, level)
	}
	if got := countEntries(t, r) - before; got != 1 {
		t.Fatalf("level change persisted %d entries, want 1", got)
	}
	snap := f.waitFrame(t, "thinking snapshot", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetStateSnapshot().GetThinkingLevel() == level
	})
	if snap.GetStateSnapshot().GetThinkingLevel() != level {
		t.Fatalf("snapshot did not publish the level: %+v", snap.GetStateSnapshot())
	}
}
