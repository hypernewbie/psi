package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"psi/internal/session"
	"psi/internal/testutil"
)

// This is entirely synthetic: a temporary HOME, dummy OAuth credentials,
// allowlisted child environment, and an in-process HTTP server. Never use the
// operator's real config as a fixture or enable a real provider in this test.
func TestCodexActualAppPTYToolRoundTrip(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app PTY workflow needs a Unix PTY")
	}
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	var requests atomic.Int32
	event := func(w io.Writer, typ string, fields map[string]any) {
		fields["type"] = typ
		data, _ := json.Marshal(fields)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/codex/responses" || r.Header.Get("Authorization") != "Bearer dummy-test-access" || r.Header.Get("chatgpt-account-id") != "test-account" {
			t.Error("Codex endpoint or synthetic OAuth credential not used")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != "gpt-5.6-luna" || body["store"] != false || body["reasoning"].(map[string]any)["effort"] != "xhigh" {
			t.Error("shared-config Codex selection did not reach the request")
		}
		hasResult, hasReasoning, hasCall := false, false, false
		for _, v := range body["input"].([]any) {
			item := v.(map[string]any)
			switch item["type"] {
			case "reasoning":
				hasReasoning = item["encrypted_content"] == "synthetic-ciphertext"
			case "function_call":
				hasCall = item["id"] == "fc_test_read" && item["call_id"] == "call_test_read"
			case "function_call_output":
				hasResult = true
				if item["call_id"] != "call_test_read" || !strings.Contains(item["output"].(string), "SYNTHETIC_READ_RESULT") {
					t.Error("tool result lost its content or call ID")
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if !hasResult {
			reasoning := map[string]any{"type": "reasoning", "id": "rs_test", "summary": []any{}, "encrypted_content": "synthetic-ciphertext"}
			call := map[string]any{"type": "function_call", "id": "fc_test_read", "call_id": "call_test_read", "name": "read", "arguments": `{"path":"fixture.txt"}`, "namespace": "functions"}
			for i, item := range []map[string]any{reasoning, call} {
				event(w, "response.output_item.added", map[string]any{"output_index": i, "item": item})
				event(w, "response.output_item.done", map[string]any{"output_index": i, "item": item})
			}
			event(w, "response.completed", map[string]any{"response": map[string]any{"id": "resp_test_tool", "status": "completed", "output": []any{reasoning, call}}})
			return
		}
		if !hasReasoning || !hasCall {
			t.Error("stateless continuation dropped signed reasoning or tool items")
		}
		message := map[string]any{"type": "message", "id": "msg_test_final", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "CODEX_APP_OK"}}}
		event(w, "response.output_item.added", map[string]any{"output_index": 0, "item": message})
		event(w, "response.output_text.delta", map[string]any{"output_index": 0, "delta": "CODEX_APP_OK"})
		event(w, "response.output_item.done", map[string]any{"output_index": 0, "item": message})
		event(w, "response.completed", map[string]any{"response": map[string]any{"id": "resp_test_final", "status": "completed", "output": []any{message}}})
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"openai-codex","defaultModel":"gpt-5.6-luna","defaultThinkingLevel":"xhigh","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"openai-codex":{"baseUrl":"`+server.URL+`","modelOverrides":{"gpt-5.6-luna":{"contextWindow":424242}}}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), fmt.Sprintf(`{"openai-codex":{"type":"oauth","access":"dummy-test-access","refresh":"dummy-test-refresh","accountId":"test-account","expires":%d}}`, time.Now().Add(time.Hour).UnixMilli()))
	writeAppFile(t, filepath.Join(ws, "fixture.txt"), "SYNTHETIC_READ_RESULT\n")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAppProcess$")
	cmd.Dir, cmd.Env = ws, appTestEnv(home, agentDir)
	master, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	chunks := make(chan string, 256)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				select {
				case chunks <- string(buf[:n]):
				case <-stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	pending := ""
	screen := testutil.NewVTModel(80, 24)
	wait := func(want string) {
		t.Helper()
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for {
			if i := strings.Index(pending, want); i >= 0 {
				pending = pending[i+len(want):]
				return
			}
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("synthetic app process exited waiting for %q", want)
				}
				pending += chunk
				screen.Feed([]byte(chunk))
			case <-timer.C:
				t.Fatalf("synthetic app timed out waiting for %q", want)
			}
		}
	}
	waitEditor := func() { waitForEditorScreen(t, chunks, screen, &pending) }
	wait("/ commands")
	waitEditor()
	master.WriteString("read fixture\r")
	// The compact success row omits redundant "done" text but keeps target identity.
	wait("CODEX_APP_OK")
	waitEditor()
	if !strings.Contains(screen.Text(), "• read") || !strings.Contains(screen.Text(), "fixture.txt") {
		t.Fatalf("settled Codex tool row is missing its action summary: %s", screen.Text())
	}
	master.WriteString("/quit\r")
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic app did not exit")
	}
	if requests.Load() != 2 {
		t.Fatal("expected a model-tool-model round trip")
	}
	infos, err := session.Discover(session.DefaultSessionDir(ws, agentDir))
	if err != nil || len(infos) != 1 {
		t.Fatal("synthetic session was not persisted")
	}
	store, err := session.OpenFile(infos[0].Path, ws, false)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if projection.State.Provider != "openai-codex" || projection.State.ModelID != "gpt-5.6-luna" || projection.State.ThinkingLevel != "xhigh" {
		t.Fatal("session state was hardcoded to another provider")
	}
	found := false
	for _, m := range projection.Flat {
		if m.Assistant == nil {
			continue
		}
		if m.Assistant.Provider != "openai-codex" || m.Assistant.API != "openai-codex-responses" {
			t.Fatal("assistant provenance was not persisted")
		}
		for _, b := range m.Assistant.Content {
			if b.Text != nil && b.Text.TextSignature != nil {
				found = strings.Contains(*b.Text.TextSignature, "final_answer")
			}
		}
	}
	if !found {
		t.Fatal("Codex text signature was not persisted")
	}
}
