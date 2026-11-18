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
)

func TestActualAppQuitReleasesTTYAndPersistsPartial(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `event: message_start
data: {"message":{"id":"quit","model":"synthetic-model","usage":{"input_tokens":1}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"index":0,"delta":{"type":"text_delta","text":"QUIT_PARTIAL"}}

`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hold the stream until /quit cancels: the turn never completes.
		<-r.Context().Done()
	}))
	// Close the server after the child dies: httptest Close waits for the
	// held stream, and the child's kill-cleanup registers next (LIFO runs
	// it first). A direct defer would deadlock teardown on failure.
	t.Cleanup(server.Close)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-quit-key"}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	q.prompt()
	q.write("partial work\r")
	q.wait("QUIT_PARTIAL")
	q.write("/quit\r")
	const releaseMarker = "\x1b[?2004l"
	q.wait(releaseMarker)
	q.finish()
	// Drain bytes that arrived after the last wait; the reader closes the
	// channel once the exited process releases the master side.
	drainDone := make(chan struct{})
	go func() {
		for chunk := range q.chunks {
			q.raw.Write(chunk)
		}
		close(drainDone)
	}()
	select {
	case <-drainDone:
	case <-time.After(8 * time.Second):
		t.Fatal("output did not settle after process exit")
	}

	raw := q.raw.String()
	idx := strings.LastIndex(raw, releaseMarker)
	if idx < 0 {
		t.Fatalf("no terminal release output: %q", raw)
	}
	if tail := raw[idx+len(releaseMarker):]; tail != "" {
		t.Fatalf("writes after terminal release: %q", tail)
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("provider requests = %d, want 1", n)
	}
	homeDir := filepath.Dir(filepath.Dir(agentDir))
	candidates := []string{filepath.Join(agentDir, "sessions"), filepath.Join(filepath.Dir(agentDir), "sessions"), filepath.Join(homeDir, "sessions")}
	persisted := false
	for _, dir := range candidates {
		filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			data, err := os.ReadFile(p)
			if err == nil && strings.Contains(string(data), "QUIT_PARTIAL") {
				persisted = true
			}
			return nil
		})
	}
	if !persisted {
		t.Fatal("aborted partial was not persisted")
	}
}
