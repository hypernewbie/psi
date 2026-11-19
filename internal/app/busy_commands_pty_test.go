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
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/session"
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
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		index := requests.Add(1)
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		if index == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		text := "FIRST_REPLY"
		if index > 1 {
			text = "AFTER_STEER"
		}
		fmt.Fprintf(w, "event: message_start\ndata: {\"message\":{\"id\":\"busy-%d\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":1}}}\n\n", index)
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"%s\"}}\n\n", text)
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()

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
	// An unsupported command must still be refused and restored, not steered.
	q.write("/resume\r")
	q.waitScreen("/resume")
	q.write(strings.Repeat("\x7f", len("/resume")))
	q.write("/not-a-command hello\r")
	q.waitScreen("Steering: /not-a-command hello")
	if got := requests.Load(); got != 1 {
		t.Fatalf("busy commands reached the provider: %d requests, want 1", got)
	}

	close(release)
	q.wait("FIRST_REPLY")
	q.wait("AFTER_STEER")
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (original + steering)", got)
	}
	second := busyWaitBody(t, bodies)
	if !strings.Contains(string(second), "/not-a-command hello") {
		t.Fatalf("steering text missing from the next request: %s", second)
	}

	q.prompt()
	q.write("/quit\r")
	q.finish()

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
