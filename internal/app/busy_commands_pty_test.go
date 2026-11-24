//go:build unix

package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/session"
)

func busyWaitBody(t *testing.T, bodies <-chan []byte) []byte {
	t.Helper()
	select {
	case body := <-bodies:
		return body
	case <-time.After(8 * time.Second):
		t.Fatal("provider never received the request")
		return nil
	}
}

// TestActualAppBusyInformationalCommandsAndSlashSteering proves that the
// read-only metadata commands run without cancelling a live run, that an
// unsupported command still restores the draft, and that unknown slash text
// becomes steering for the next turn.
func TestActualAppBusyInformationalCommandsAndSlashSteering(t *testing.T) {
	var requests atomic.Int32
	bodies := make(chan []byte, 8)
	releaseCh := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseCh) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request %d: %v", index, err)
			http.Error(w, "cannot read request", http.StatusBadRequest)
			return
		}
		select {
		case bodies <- body:
		default:
			t.Errorf("too many provider requests: %d", index)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		if index == 1 {
			select {
			case <-releaseCh:
			case <-r.Context().Done():
				return
			}
		}
		text := "FIRST_REPLY"
		if index > 1 {
			text = "AFTER_STEER"
		}
		writeAnthropicTestResponse(w, fmt.Sprintf("busy-%d", index), text)
	}))
	defer server.Close()
	defer release()

	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`","models":[{"id":"local-model","api":"anthropic-messages","contextWindow":65536,"maxTokens":16384,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("ask first\r")
	first := busyWaitBody(t, bodies)
	if !strings.Contains(string(first), "ask first") {
		t.Fatalf("first request missing the original prompt: %s", first)
	}

	q.write("/session\r")
	q.wait("continuation:")
	q.write("/hotkeys\r")
	q.wait("Keys (fixed, no Kitty required):")
	q.write("/name PR3-busy\r")
	q.wait("Session name set: PR3-busy")
	q.write("/usage anthropic\r")
	q.waitScreen("provider is not supported")
	q.write("\x1b") // Esc closes the panel, not the held inference stream.
	q.waitScreenMatch("usage panel dismissed", func(text string) bool {
		return !strings.Contains(text, "provider is not supported")
	})
	// Idle-only commands must not appear in provider context. Adding a
	// character after submission proves the draft and its cursor survived,
	// not just that the command was visible in a pre-submit frame.
	const lineStart, lineEnd, enter, altEnter = "\x01", "\x05", "\r", "\x1b\r" // Ctrl+A, Ctrl+E, Enter, Alt+Enter.
	for _, draft := range []string{"/reload", "/loop KEEP_LOOP", "/resume", "/"} {
		submit := enter
		if draft == "/" || strings.HasPrefix(draft, "/loop ") {
			// Exercise the follow-up submission path too. Alt+Enter also
			// submits the bare slash without applying a completion.
			submit = altEnter
		}
		q.write(draft + lineStart + submit + "x")
		q.waitScreen("\nx" + draft + "\n")
		q.write(lineEnd + strings.Repeat("\x7f", len(draft)+1))
	}
	q.write("/not-a-command hello\r")
	q.waitScreen("Steering: /not-a-command hello")
	if got := requests.Load(); got != 1 {
		t.Fatalf("busy commands reached the provider: %d requests, want 1", got)
	}

	release()
	q.wait("FIRST_REPLY")
	q.wait("AFTER_STEER")
	second := busyWaitBody(t, bodies)
	if !strings.Contains(string(second), "/not-a-command hello") {
		t.Fatalf("steering text missing from the next request: %s", second)
	}

	if strings.Contains(string(second), "/usage anthropic") || strings.Contains(string(second), "provider is not supported") {
		t.Fatal("the usage command or refusal reached inference context")
	}

	q.prompt()
	q.write("/quit\r")
	q.finish()
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (original + steering)", got)
	}

	var files []string
	if err := filepath.WalkDir(filepath.Join(agentDir, "sessions"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("session files = %v, want exactly one", files)
	}
	reopened, err := session.OpenFile(files[0], work, false)
	if err != nil {
		t.Fatal(err)
	}
	if name := reopened.SessionName(); name != "PR3-busy" {
		t.Fatalf("session name = %q, want PR3-busy", name)
	}
	proj, err := reopened.Projection()
	if err != nil {
		t.Fatal(err)
	}
	users, assistants := 0, 0
	for _, m := range proj.Flat {
		switch {
		case m.User != nil:
			users++
		case m.Assistant != nil:
			assistants++
		}
	}
	if users != 2 || assistants != 2 {
		t.Fatalf("users=%d assistants=%d, want 2/2", users, assistants)
	}
}
