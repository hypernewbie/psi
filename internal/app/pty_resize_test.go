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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"psi/internal/testutil"
)

func TestActualAppPTYResize(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	streamHold := make(chan struct{})
	var releaseStream sync.Once
	releaseStreamFn := func() { releaseStream.Do(func() { close(streamHold) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		encoded, _ := json.Marshal(body)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n")

		if strings.Contains(string(encoded), "resize-stream") {
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"STREAM_START\"}}\n\n")
			w.(http.Flusher).Flush()
			<-streamHold
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"STREAM_DONE\"}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n")
			return
		}

		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"APP_OK\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	defer releaseStreamFn()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model"}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192},{"id":"second-model","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	cmd := exec.Command(os.Args[0], "-test.run=^TestAppProcess$")
	cmd.Dir = ws
	cmd.Env = appTestEnv(home, agentDir)
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
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
				pending += chunk
				screen.Feed([]byte(chunk))
				if !ok {
					t.Fatalf("process exited waiting for %q: %q", want, pending)
				}
			case <-timer.C:
				t.Fatalf("timeout waiting for %q: %q screen=%q", want, pending, screen.Text())
			}
		}
	}
	write := func(s string) {
		t.Helper()
		if _, err := master.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	waitEditor := func() { waitForEditorScreen(t, chunks, screen, &pending) }
	wait("/ commands")
	waitEditor()

	write("my-draft-line")
	wait("my-draft-line")

	// Phase 1: Resize with active draft across sizes
	for _, size := range [][2]int{{120, 40}, {60, 16}, {40, 12}, {20, 6}, {80, 24}} {
		width, height := size[0], size[1]
		if err := pty.Setsize(master, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)}); err != nil {
			t.Fatal(err)
		}
		screen.Resize(width, height)
		beforeResize := screen.WriteCalls
		timer := time.NewTimer(4 * time.Second)
		redrawDetected := false
		for !redrawDetected {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("process exited on resize %dx%d", width, height)
				}
				pending += chunk
				screen.Feed([]byte(chunk))
				if screen.WriteCalls > beforeResize && semanticEditorReady(screen) && strings.Contains(screen.Text(), "my-draft-line") {
					redrawDetected = true
				}
			case <-timer.C:
				t.Fatalf("draft resize to %dx%d did not redraw within timeout: cursor=(%d,%d) visible=%v screen=%q", width, height, screen.CursorCol, screen.CursorRow, screen.CursorVisible, screen.Text())
			}
		}
		timer.Stop()
	}

	write("\x15") // Ctrl+U clear draft
	write("resize-stream\r")
	wait("STREAM_START")

	// Phase 2: Resize during streaming
	for _, size := range [][2]int{{120, 40}, {60, 16}, {40, 12}, {20, 6}, {80, 24}} {
		width, height := size[0], size[1]
		if err := pty.Setsize(master, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)}); err != nil {
			t.Fatal(err)
		}
		screen.Resize(width, height)
		beforeResize := screen.WriteCalls
		timer := time.NewTimer(4 * time.Second)
		redrawDetected := false
		for !redrawDetected {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("process exited on stream resize %dx%d", width, height)
				}
				pending += chunk
				screen.Feed([]byte(chunk))
				if screen.WriteCalls > beforeResize && (strings.Contains(screen.Text(), "Working") || strings.Contains(screen.Text(), "Esc/Ctrl+C") || strings.Contains(screen.Text(), "STREAM_START")) {
					redrawDetected = true
				}
			case <-timer.C:
				t.Fatalf("stream resize to %dx%d did not redraw within timeout: screen=%q", width, height, screen.Text())
			}
		}
		timer.Stop()
	}

	releaseStreamFn()
	wait("STREAM_DONE")
	waitEditor()

	// Phase 3: Resize while model picker is open
	write("/model\r")
	wait("Models ·")

	for _, size := range [][2]int{{120, 40}, {60, 16}, {40, 12}, {20, 6}, {80, 24}} {
		width, height := size[0], size[1]
		if err := pty.Setsize(master, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)}); err != nil {
			t.Fatal(err)
		}
		screen.Resize(width, height)
		beforeResize := screen.WriteCalls
		timer := time.NewTimer(4 * time.Second)
		redrawDetected := false
		for !redrawDetected {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("process exited on picker resize %dx%d", width, height)
				}
				pending += chunk
				screen.Feed([]byte(chunk))
				if screen.WriteCalls > beforeResize && strings.Contains(screen.Text(), "local-model") {
					redrawDetected = true
				}
			case <-timer.C:
				t.Fatalf("picker resize to %dx%d did not redraw within timeout: screen=%q (pending=%q)", width, height, screen.Text(), pending)
			}
		}
		timer.Stop()
	}

	write("\x1b") // Cancel picker
	timer := time.NewTimer(4 * time.Second)
	for strings.Contains(screen.Text(), "Models ·") {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatal("process exited waiting for picker to close")
			}
			pending += chunk
			screen.Feed([]byte(chunk))
		case <-timer.C:
			t.Fatalf("picker did not close within timeout: screen=%q", screen.Text())
		}
	}
	timer.Stop()
	waitEditor()

	write("\x15") // Picker cancellation restores its command draft; clear it before quitting.
	write("/quit\r")
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("quit did not exit: screen=%q pending=%q", screen.Text(), pending)
	}
}
