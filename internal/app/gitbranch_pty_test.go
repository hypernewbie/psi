//go:build unix

package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"psi/internal/testutil"
)

func startBranchFooterApp(t *testing.T, dir, home, agentDir string) (screen *testutil.VTModel, wait func(string), waitIdle func(), waitScreen func(string), write func(string), quit func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/messages" || req.Header.Get("x-api-key") != "file-test-key" {
			t.Errorf("endpoint/auth not used: %s", req.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"APP_OK\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	t.Cleanup(server.Close)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)
	cmd := exec.Command(os.Args[0], "-test.run=^TestAppProcess$")
	cmd.Dir = dir
	cmd.Env = appTestEnv(home, agentDir)
	master, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		master.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
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
	screen = testutil.NewVTModel(80, 24)
	wait = func(want string) {
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
				screen.Feed([]byte(chunk))
				if !ok {
					t.Fatalf("process exited waiting for %q: %q", want, pending)
				}
			case <-timer.C:
				t.Fatalf("timeout waiting for %q: %q", want, pending)
			}
		}
	}
	write = func(s string) {
		t.Helper()
		if _, err := master.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	waitScreen = func(want string) {
		t.Helper()
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for {
			if strings.Contains(screen.Text(), want) {
				return
			}
			select {
			case chunk, ok := <-chunks:
				pending += chunk
				screen.Feed([]byte(chunk))
				if !ok {
					t.Fatalf("process exited waiting for screen %q: %q", want, screen.Text())
				}
			case <-timer.C:
				t.Fatalf("timeout waiting for screen %q: %q", want, screen.Text())
			}
		}
	}
	// waitIdle returns once the run has fully settled. The "Working…"
	// activity line is present for the whole run and cleared on settle, so
	// its absence means the app accepts commands again; a 40 ms quiet gap
	// absorbs the last in-flight settle frame. Submitting /quit before this
	// lands on the busy path, where the command is refused and restored as
	// a draft, so the process never exits.
	waitIdle = func() {
		t.Helper()
		deadline := time.NewTimer(8 * time.Second)
		defer deadline.Stop()
		quiet := time.NewTimer(40 * time.Millisecond)
		defer quiet.Stop()
		for {
			if strings.Contains(screen.Text(), "Working…") {
				if !quiet.Stop() {
					select {
					case <-quiet.C:
					default:
					}
				}
				select {
				case chunk, ok := <-chunks:
					if !ok {
						t.Fatalf("process exited before the run settled: %q", pending)
					}
					pending += chunk
					screen.Feed([]byte(chunk))
				case <-deadline.C:
					t.Fatalf("timeout waiting for the run to settle: %q", screen.Text())
				}
				continue
			}
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(40 * time.Millisecond)
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("process exited before the run settled: %q", pending)
				}
				pending += chunk
				screen.Feed([]byte(chunk))
			case <-quiet.C:
				return
			case <-deadline.C:
				t.Fatalf("timeout waiting for the run to settle: %q", screen.Text())
			}
		}
	}
	quit = func() {
		t.Helper()
		write("/quit\r")
		// Single owner of Wait: the timeout path kills and reaps through the
		// same goroutine, so cleanup never starts a second concurrent Wait.
		finished := make(chan error, 1)
		go func() { finished <- cmd.Wait() }()
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(8 * time.Second):
			_ = cmd.Process.Kill()
			err := <-finished
			t.Fatalf("process did not exit after /quit: %v (output=%q)", err, tail(pending, 2000))
		}
	}
	return screen, wait, waitIdle, waitScreen, write, quit
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func branchFooterHome(t *testing.T) string {
	t.Helper()
	// The app reports os.Getwd(), which resolves symlinks, while HOME comes
	// from the environment. Resolve the temporary home so the two agree and
	// the footer abbreviates the fixture to ~/repo (TMPDIR is a symlink on
	// macOS).
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestActualAppFooterShowsGitBranch(t *testing.T) {
	home := branchFooterHome(t)
	agentDir := filepath.Join(home, ".pi", "agent")
	fixture := filepath.Join(home, "repo")
	head := filepath.Join(fixture, ".git", "HEAD")
	if err := os.MkdirAll(filepath.Dir(head), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, wait, waitIdle, waitScreen, write, quit := startBranchFooterApp(t, fixture, home, agentDir)
	wait("/ commands")
	write("hello\r")
	wait("APP_OK")
	waitScreen("~/repo (main)")
	waitIdle()
	quit()
}

func TestActualAppFooterOmitsBranchOutsideARepository(t *testing.T) {
	home := branchFooterHome(t)
	fixture := filepath.Join(home, "plain")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatal(err)
	}
	for dir := fixture; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			t.Skip("temporary directory is inside a git repository")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	agentDir := filepath.Join(home, ".pi", "agent")
	screen, wait, waitIdle, waitScreen, write, quit := startBranchFooterApp(t, fixture, home, agentDir)
	wait("/ commands")
	write("hello\r")
	wait("APP_OK")
	waitScreen("~/plain")
	if text := screen.Text(); strings.Contains(text, "~/plain (") {
		t.Fatalf("footer shows a branch outside a repository: %q", text)
	}
	waitIdle()
	quit()
}
