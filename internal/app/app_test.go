package app

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"psi/internal/config"
	"psi/internal/session"
	"psi/internal/terminal"
)

func TestParseBoundary(t *testing.T) {
	o, _, err := Parse([]string{"--provider", "anthropic", "--model", "m", "--thinking", "high", "--session-dir", "/tmp", "--tools", "read,bash", "--no-session"})
	if err != nil || o.Model != "m" || !o.NoSession {
		t.Fatalf("%+v %v", o, err)
	}
	for _, args := range [][]string{{"positional"}, {"--continue", "--resume"}, {"--allow-project", "--deny-project"}} {
		if _, _, err := Parse(args); err == nil {
			t.Errorf("accepted conflicting/unused arguments: %v", args)
		}
	}
}

func testRuntime(t *testing.T) *runtime {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	s, err := session.New(session.OpenOptions{Cwd: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{store: s, agentDir: t.TempDir(), settings: config.Settings{DefaultProvider: "anthropic", DefaultModel: "m", DefaultThinking: "off", DefaultTools: []string{"read"}},
		models: &config.ModelsFile{Providers: map[string]config.ProviderMeta{"anthropic": {Models: []config.ModelMeta{{ID: "m", Reasoning: true, MaxTokens: 16384}}}}}, prompt: "workspace instructions"}
	if err := s.TryLock(); err != nil {
		t.Fatal(err)
	}
	if err := r.restore(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.store.Unlock()
		if r.prov != nil {
			r.prov.CloseIdle()
		}
	})
	return r
}

func TestCommandsReplaceSessionAndValidateBeforeMutation(t *testing.T) {
	r := testRuntime(t)
	if r.authErr == nil {
		t.Fatal("missing credentials must remain an error, not a fake provider")
	}
	if r.cfg.System != r.prompt || len(r.cfg.Tools[0].Schema["required"].([]string)) == 0 {
		t.Fatal("prompt/tool schemas not wired")
	}
	old := r.store
	if _, err := r.command(&terminal.Command{Name: "new"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.store == old || r.store.Header().ID == old.Header().ID {
		t.Fatal("new did not replace active session")
	}
	before := len(r.store.Entries())
	if _, err := r.command(&terminal.Command{Name: "model", Arg: "openai bad"}, io.Discard); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	if r.ov.Provider != "anthropic" || len(r.store.Entries()) != before {
		t.Fatal("failed selection changed state")
	}
	if _, err := r.command(&terminal.Command{Name: "thinking", Arg: "high"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.ov.Thinking != "high" {
		t.Fatal("thinking did not change")
	}
	var b bytes.Buffer
	if _, err := r.command(&terminal.Command{Name: "session"}, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), r.store.Header().ID) {
		t.Fatal(b.String())
	}
}

// Run the real entrypoint in a PTY, with an isolated HOME and a local HTTP
// provider. This catches wiring bugs that unit tests of unused helpers miss.
func TestAppProcess(t *testing.T) {
	if os.Getenv("PSI_APP_TEST_PROCESS") != "1" || flag.Lookup("test.run").Value.String() != "^TestAppProcess$" {
		return
	}
	if err := Run([]string{"--deny-project", "--tools", "read"}, "test"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Use an allowlist instead of os.Environ: test subprocesses must not inherit
// developer credentials, Pi directories, proxies, or live-test opt-in flags.
func appTestEnv(home, agentDir string) []string {
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "PI_CODING_AGENT_DIR=" + agentDir,
		"PI_CODING_AGENT_SESSION_DIR=", "TERM=xterm", "LANG=C.UTF-8", "PSI_APP_TEST_PROCESS=1"}
}

func TestAppSubprocessEnvironmentIsIsolated(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "synthetic-parent-secret")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-parent-secret")
	t.Setenv("PI_CODING_AGENT_DIR", "/synthetic-parent-pi-directory")
	t.Setenv("PSI_LIVE_PROVIDER", "1")
	t.Setenv("HTTPS_PROXY", "http://synthetic-parent-proxy.invalid")
	env := strings.Join(appTestEnv("/synthetic-home", "/synthetic-agent"), "\n")
	for _, forbidden := range []string{"synthetic-parent", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "PSI_LIVE_PROVIDER", "HTTPS_PROXY"} {
		if strings.Contains(env, forbidden) {
			t.Fatal("test subprocess inherited a forbidden environment value")
		}
	}
}

func writeAppFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestActualAppPTYWorkflow(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app PTY workflow needs a Unix PTY")
	}
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	requests := make(chan map[string]any, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/messages" || req.Header.Get("x-api-key") != "file-test-key" {
			t.Errorf("endpoint/auth not used: %s", req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		messages, _ := body["messages"].([]any)
		last, _ := messages[len(messages)-1].(map[string]any)
		encoded, _ := json.Marshal(last)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n")
		if strings.Contains(string(encoded), "cancel-this") {
			w.(http.Flusher).Flush()
			<-req.Context().Done()
			return
		}
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"APP_OK\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)
	writeAppFile(t, filepath.Join(agentDir, "AGENTS.md"), "FOLLOW_WORKSPACE_INSTRUCTIONS")
	writeAppFile(t, filepath.Join(agentDir, "skills", "doer", "SKILL.md"), "---\nname: doer\ndescription: >\n  Do important work.\n---\nSKILL_BODY\n")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAppProcess$")
	cmd.Dir = ws
	cmd.Env = appTestEnv(home, agentDir)
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
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	var pending string
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
				pending += chunk
				if !ok {
					t.Fatalf("process exited waiting for %q: %q", want, pending)
				}
			case <-timer.C:
				t.Fatalf("timeout waiting for %q: %q", want, pending)
			}
		}
	}
	write := func(s string) {
		t.Helper()
		if _, err := master.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	wait("/help")
	wait("> \r\x1b[2C")
	write("hello\r")
	wait("APP_OK")
	wait("> \r\x1b[2C")
	body := <-requests
	initialSessions, err := session.Discover(session.DefaultSessionDir(ws, agentDir))
	if err != nil || len(initialSessions) != 1 {
		t.Fatalf("first session not persisted: %v", err)
	}
	contender, err := session.OpenFile(initialSessions[0].Path, ws, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := contender.TryLock(); err == nil {
		contender.Unlock()
		t.Fatal("running app did not lock its session")
	}
	if !strings.Contains(body["system"].(string), "FOLLOW_WORKSPACE_INSTRUCTIONS") || !strings.Contains(body["system"].(string), "doer") {
		t.Fatal("instructions or skills not sent")
	}
	defs := body["tools"].([]any)
	if len(defs) != 1 || defs[0].(map[string]any)["name"] != "read" {
		t.Fatal("CLI tools selection ignored")
	}
	write("/skill:doer run-it\r")
	wait("APP_OK")
	wait("> \r\x1b[2C")
	skillRequest := <-requests
	encoded, _ := json.Marshal(skillRequest["messages"])
	if !bytes.Contains(encoded, []byte("SKILL_BODY")) || !bytes.Contains(encoded, []byte("run-it")) {
		t.Fatalf("skill was not submitted: %s", encoded)
	}
	write("/new\r")
	wait("new session")
	wait("> \r\x1b[2C")
	write("fresh-session\r")
	wait("APP_OK")
	wait("> \r\x1b[2C")
	fresh := <-requests
	encoded, _ = json.Marshal(fresh["messages"])
	if bytes.Contains(encoded, []byte("hello")) {
		t.Fatal("new command retained old history")
	}
	if err := contender.TryLock(); err != nil {
		t.Fatalf("new leaked the old session lock: %v", err)
	}
	contender.Unlock()
	write("/resume\r")
	wait("Sessions (page")
	wait("> \r\x1b[2C")
	write("2\r")
	wait("resumed ")
	wait("> \r\x1b[2C")
	write("resumed-session\r")
	wait("APP_OK")
	wait("> \r\x1b[2C")
	resumed := <-requests
	encoded, _ = json.Marshal(resumed["messages"])
	if !bytes.Contains(encoded, []byte("SKILL_BODY")) || bytes.Contains(encoded, []byte("fresh-session")) {
		t.Fatal("resume did not switch back to the selected history")
	}
	write("cancel-this\r")
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel request not received")
	}
	write("\x03")
	wait("(cancelled)")
	wait("> \r\x1b[2C")
	write("/quit\r")
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quit did not exit")
	}
	infos, err := session.Discover(session.DefaultSessionDir(ws, agentDir))
	if err != nil || len(infos) != 2 {
		t.Fatalf("session lifecycle: %d %v", len(infos), err)
	}
	for _, info := range infos {
		s, err := session.OpenFile(info.Path, ws, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.TryLock(); err != nil {
			t.Fatalf("lock leaked on exit: %v", err)
		}
		s.Unlock()
	}
}
