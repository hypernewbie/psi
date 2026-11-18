//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/testutil"
)

type settingsAppSession struct {
	t       *testing.T
	master  *os.File
	chunks  chan string
	pending string
	screen  *testutil.VTModel
	cmd     *exec.Cmd
}

func startSettingsApp(t *testing.T, home, agentDir, ws string) *settingsAppSession {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAppProcess$")
	cmd.Dir = ws
	cmd.Env = appTestEnv(home, agentDir)
	master, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	session := &settingsAppSession{t: t, master: master, chunks: make(chan string, 256), screen: testutil.NewVTModel(80, 24), cmd: cmd}
	go func() {
		defer close(session.chunks)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				session.chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
		master.Close()
	})
	return session
}

func (s *settingsAppSession) wait(want string) {
	s.t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if i := strings.Index(s.pending, want); i >= 0 {
			s.pending = s.pending[i+len(want):]
			return
		}
		select {
		case chunk, ok := <-s.chunks:
			s.pending += chunk
			s.screen.Feed([]byte(chunk))
			if !ok {
				s.t.Fatalf("process exited waiting for %q: %q", want, s.pending)
			}
		case <-timer.C:
			s.t.Fatalf("timeout waiting for %q: %q", want, s.pending)
		}
	}
}

func (s *settingsAppSession) waitEditor() {
	s.t.Helper()
	waitForEditorScreen(s.t, s.chunks, s.screen, &s.pending)
}

// waitScreen waits until the rendered screen matches want. Styled rows can
// split text with SGR sequences in the raw stream, so match the model text.
func (s *settingsAppSession) waitScreen(want string) {
	s.waitScreenMatch(regexp.MustCompile(regexp.QuoteMeta(want)))
}

func (s *settingsAppSession) waitScreenMatch(pattern *regexp.Regexp) {
	s.t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		if pattern.MatchString(s.screen.Text()) {
			return
		}
		select {
		case chunk, ok := <-s.chunks:
			s.pending += chunk
			s.screen.Feed([]byte(chunk))
			if !ok {
				s.t.Fatalf("process exited waiting for screen text %q", pattern)
			}
		case <-deadline.C:
			s.t.Fatalf("timeout waiting for screen text %q: %q", pattern, s.screen.Text())
		}
	}
}

func (s *settingsAppSession) write(text string) {
	s.t.Helper()
	if _, err := s.master.WriteString(text); err != nil {
		s.t.Fatal(err)
	}
}

func (s *settingsAppSession) quit() {
	s.t.Helper()
	s.write("/quit\r")
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-s.chunks:
			if !ok {
				return
			}
		case <-timer.C:
			s.t.Fatal("timeout waiting for app exit")
		}
	}
}

func TestActualAppSettings(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app settings workflow needs a Unix PTY")
	}
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"defaultProvider":"anthropic","defaultModel":"local-model","unknown":{"keep":true},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	first := startSettingsApp(t, home, agentDir, ws)
	first.wait("/ commands")
	first.waitEditor()
	first.write("/settings\r")
	first.wait("Auto-compact")
	first.wait("Enter/Space change")
	first.write("\x1b[B\x1b[B")
	first.waitScreen("→ Output padding")
	first.write("\r")
	first.waitScreenMatch(regexp.MustCompile(`Output padding +1`))
	first.write("\x1b")
	time.Sleep(250 * time.Millisecond)
	first.waitEditor()
	first.quit()

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["outputPad"] != float64(1) {
		t.Fatalf("outputPad not persisted: %s", data)
	}
	unknown, _ := stored["unknown"].(map[string]any)
	if unknown["keep"] != true {
		t.Fatalf("unknown key lost: %s", data)
	}

	second := startSettingsApp(t, home, agentDir, ws)
	second.wait("/ commands")
	second.waitEditor()
	second.write("/settings\r")
	second.wait("Output padding")
	second.waitScreenMatch(regexp.MustCompile(`Output padding +1`))
	second.write("\x1b")
	time.Sleep(250 * time.Millisecond)
	second.waitEditor()
	second.quit()
}

func TestActualAppSettingsCorruptFileStaysSessionOnly(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app settings workflow needs a Unix PTY")
	}
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"defaultProvider":"anthropic","defaultModel":"local-model","outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	session := startSettingsApp(t, home, agentDir, ws)
	session.wait("/ commands")
	session.waitEditor()
	if err := os.WriteFile(settingsPath, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	session.write("/settings\r")
	session.wait("Auto-compact")
	session.write("\x1b[B\x1b[B")
	session.waitScreen("→ Output padding")
	session.write("\r")
	session.wait("Saved for this session only")
	session.waitScreenMatch(regexp.MustCompile(`Output padding +1`))
	session.write("\x1b")
	time.Sleep(250 * time.Millisecond)
	session.waitEditor()
	session.quit()

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"broken":` {
		t.Fatalf("corrupt file was changed: %q", data)
	}
}

func TestActualAppSettingsAllowedWhileBusy(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app settings workflow needs a Unix PTY")
	}
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"DONE\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	session := startSettingsApp(t, home, agentDir, ws)
	session.wait("/ commands")
	session.waitEditor()
	session.write("work\r")
	session.wait("Working")
	// Live configuration: /settings opens during a run instead of refusing.
	session.write("/settings\r")
	session.waitScreen("Auto-compact")
	session.write("\x1b")
	close(release)
	session.wait("DONE")
	session.waitEditor()
	session.write("\x15")
	session.quit()
}

func TestActualAppSettingsShortTerminal(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app settings workflow needs a Unix PTY")
	}
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"defaultProvider":"anthropic","defaultModel":"local-model","outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	session := startSettingsApp(t, home, agentDir, ws)
	session.wait("/ commands")
	if err := pty.Setsize(session.master, &pty.Winsize{Cols: 40, Rows: 10}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	session.waitEditor()
	session.write("/settings\r")
	session.wait("Auto-compact")
	for i := 0; i < 9; i++ {
		session.write("\x1b[B")
	}
	session.waitScreen("→ Color mode")
	session.write("\r")
	session.waitScreenMatch(regexp.MustCompile(`Color mode +true`))
	session.write("\x1b")
	time.Sleep(250 * time.Millisecond)
	session.waitEditor()
	session.quit()

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Terminal map[string]any `json:"terminal"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Terminal["trueColor"] != true {
		t.Fatalf("terminal.trueColor = %v\n%s", stored.Terminal["trueColor"], data)
	}
}

func TestActualAppModelThinkingDefaults(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app settings workflow needs a Unix PTY")
	}
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"defaultProvider":"anthropic","defaultModel":"local-model","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"local-model","reasoning":true,"maxTokens":64000,"contextWindow":200000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	models, err := config.LoadModels(filepath.Join(agentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := models.SelectModel("anthropic", "local-model")
	if err != nil {
		t.Fatal(err)
	}
	levels := provider.SupportedThinking(providerModel("anthropic", meta))
	if len(levels) < 2 {
		t.Fatalf("fixture advertises %d thinking levels, want at least 2", len(levels))
	}
	want := levels[1]

	openLastSettingsRow := func(session *settingsAppSession) {
		session.write("/settings\r")
		session.wait("Auto-compact")
		session.write(strings.Repeat("\x1b[B", 11))
		session.waitScreen("→ Default thinking level per model")
		session.write("\r")
	}

	session := startSettingsApp(t, home, agentDir, ws)
	session.wait("/ commands")
	session.waitEditor()
	openLastSettingsRow(session)

	// The active model is already selected in the model picker.
	session.waitScreen("local-model")
	session.write("\r")
	// Down once from the first supported level.
	session.waitScreen("minimal")
	session.write("\x1b[B\r")
	// Saving reopens settings at the first row; return to the last row to read
	// its updated summary.
	session.write(strings.Repeat("\x1b[B", 11))
	session.waitScreen("1 configured")
	session.write("\x03")
	session.waitEditor()
	session.write("/session\r")
	session.waitScreen("continuation: ok")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Levels map[string]string `json:"modelThinkingLevels"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Levels["anthropic/local-model"] != want {
		t.Fatalf("stored level = %q, want %q in %s", stored.Levels["anthropic/local-model"], want, data)
	}

	// Cancel from the model step, then from the level step. Each must return
	// to settings; /session proves no hidden selector consumed later input.
	openLastSettingsRow(session)
	session.waitScreen("local-model")
	session.write("\x03")
	session.waitScreen("Default thinking level per model")
	session.write("\x03")
	session.waitEditor()
	session.write("/session\r")
	session.waitScreen("continuation: ok")

	openLastSettingsRow(session)
	session.waitScreen("local-model")
	session.write("\r")
	session.waitScreen("minimal")
	session.write("\x03")
	session.waitScreen("Default thinking level per model")
	session.write("\x03")
	session.waitEditor()
	session.write("/session\r")
	session.waitScreen("continuation: ok")

	data, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var final struct {
		Levels  map[string]string `json:"modelThinkingLevels"`
		Default string            `json:"defaultThinkingLevel"`
	}
	if err := json.Unmarshal(data, &final); err != nil {
		t.Fatal(err)
	}
	if len(final.Levels) != 1 || final.Levels["anthropic/local-model"] != want {
		t.Fatalf("cancelled flow changed the map: %s", data)
	}
	if final.Default != "" {
		t.Fatalf("cancelled flow wrote a global default: %s", data)
	}

	session.quit()
}

func TestActualAppSaveThinkingDefault(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app settings workflow needs a Unix PTY")
	}
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"defaultProvider":"anthropic","defaultModel":"local-model","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"local-model","reasoning":true,"maxTokens":64000,"contextWindow":200000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	models, err := config.LoadModels(filepath.Join(agentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := models.SelectModel("anthropic", "local-model")
	if err != nil {
		t.Fatal(err)
	}
	levels := provider.SupportedThinking(providerModel("anthropic", meta))
	if len(levels) < 2 {
		t.Fatalf("fixture advertises %d thinking levels, want at least 2", len(levels))
	}
	want := levels[1]

	session := startSettingsApp(t, home, agentDir, ws)
	session.wait("/ commands")
	session.waitEditor()
	session.write("/thinking\r")
	session.waitScreen("Thinking levels")
	session.write("\x1b[B\x13")
	session.waitScreen("Saved default thinking level")
	session.waitEditor()
	session.write("/session\r")
	session.waitScreen("thinking " + want)

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Default string `json:"defaultThinkingLevel"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Default != want {
		t.Fatalf("defaultThinkingLevel = %q, want %q in %s", stored.Default, want, data)
	}

	// The per-model level picker shares the Thinking rendering flag but must
	// not honor Ctrl+S. Cancel returns to settings.
	session.write("/settings\r")
	session.wait("Auto-compact")
	session.write(strings.Repeat("\x1b[B", 11))
	session.waitScreen("→ Default thinking level per model")
	session.write("\r")
	session.waitScreen("local-model")
	session.write("\r")
	session.waitScreen("Thinking levels")
	session.write("\x13\x03")
	session.write(strings.Repeat("\x1b[B", 11))
	session.waitScreen("Default thinking level per model")
	session.write("\x03")
	session.waitEditor()
	session.write("/session\r")
	session.waitScreen("continuation: ok")

	data, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var after struct {
		Default string                     `json:"defaultThinkingLevel"`
		Levels  map[string]json.RawMessage `json:"modelThinkingLevels"`
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if after.Default != want {
		t.Fatalf("per-model Ctrl+S changed the global default to %q: %s", after.Default, data)
	}
	if len(after.Levels) != 0 {
		t.Fatalf("per-model Ctrl+S wrote a per-model entry: %s", data)
	}

	session.quit()
}
