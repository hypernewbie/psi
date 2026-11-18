//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/session"
	"psi/internal/testutil"
)

const contextUsageAPIKey = "synthetic-context-usage-key"

// contextUsageFixture builds one isolated app launch: synthetic settings,
// models, auth, session header, HOME, work directory, and loopback provider.
type contextUsageFixture struct {
	home        string
	work        string
	agentDir    string
	sessions    string
	settings    string
	sessionPath string
	server      *httptest.Server
	requests    atomic.Int32
}

func newContextUsageFixture(t *testing.T, mode string) *contextUsageFixture {
	t.Helper()
	f := &contextUsageFixture{
		home:     t.TempDir(),
		work:     t.TempDir(),
		agentDir: t.TempDir(),
		sessions: t.TempDir(),
	}
	f.settings = filepath.Join(f.agentDir, "settings.json")
	f.writeSettings(t, mode)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != contextUsageAPIKey {
			t.Errorf("unexpected provider request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("x-api-key"))
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		f.requests.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("consume request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"ctx-1\",\"model\":\"m\",\"usage\":{\"input_tokens\":79000}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"CONTEXT_USAGE_DONE\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1000}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
	}))
	t.Cleanup(f.closeServer)

	writeAppFile(t, filepath.Join(f.agentDir, "models.json"), fmt.Sprintf(
		`{"providers":{"anthropic":{"baseUrl":%q,"models":[{"id":"m","maxTokens":4096,"contextWindow":200000}]}}}`,
		f.server.URL+"/v1",
	))
	writeAppFile(t, filepath.Join(f.agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"`+contextUsageAPIKey+`"}}`)

	store, err := session.New(session.OpenOptions{Cwd: f.work, SessionDir: f.sessions, AgentDir: f.agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(store.Header())
	if err != nil {
		t.Fatal(err)
	}
	f.sessionPath = store.File()
	writeAppFile(t, f.sessionPath, string(header)+"\n")
	if _, err := session.OpenFile(f.sessionPath, f.work, true); err != nil {
		t.Fatalf("synthetic header-only session is invalid: %v", err)
	}
	return f
}

func (f *contextUsageFixture) writeSettings(t *testing.T, mode string) {
	t.Helper()
	document := map[string]any{
		"defaultProvider": "anthropic",
		"defaultModel":    "m",
		"theme":           "dark",
		"terminal":        map[string]any{"trueColor": false},
		"compaction":      map[string]any{"enabled": false},
	}
	if mode != "" {
		document["contextUsageDisplay"] = mode
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, f.settings, string(data))
}

func (f *contextUsageFixture) closeServer() {
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
}

func (f *contextUsageFixture) start(t *testing.T) *reviewPTY {
	t.Helper()
	return startReviewPTY(t, f.home, f.work, f.agentDir,
		"--deny-project", "--no-tools", "--no-skills", "--no-context-files", "--session", f.sessionPath)
}

// waitScreenState drains PTY chunks until predicate holds for the current VT
// screen. It carries the same screen and pending state as the shared helper.
func waitScreenState(q *reviewPTY, description string, predicate func(*testutil.VTModel) bool) {
	q.t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if predicate(q.screen) {
			return
		}
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				q.t.Fatalf("process exited waiting for %s: %q", description, q.screen.Text())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timer.C:
			q.t.Fatalf("timeout waiting for %s: %q", description, q.screen.Text())
		}
	}
}

func completedContextUsageScreen(caption string) func(*testutil.VTModel) bool {
	return func(screen *testutil.VTModel) bool {
		text := screen.Text()
		return strings.Contains(text, "CONTEXT_USAGE_DONE") &&
			strings.Contains(text, caption) &&
			semanticEditorReady(screen) &&
			!strings.Contains(text, "Working")
	}
}

func TestActualAppContextUsageConfig(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("set -psi-test-binary to run actual-app context usage checks")
	}
	for _, tc := range []struct {
		name    string
		mode    string
		caption string
	}{
		{name: "absent", caption: "~40.0% / 200k"},
		{name: "tokens", mode: "tokens", caption: "~80k / 200k"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContextUsageFixture(t, tc.mode)
			q := f.start(t)
			waitScreenState(q, "ready editor", semanticEditorReady)
			q.write("show context usage\r")
			waitScreenState(q, tc.caption, completedContextUsageScreen(tc.caption))
			if got := f.requests.Load(); got != 1 {
				t.Fatalf("provider requests = %d, want 1", got)
			}
			q.write("/quit\r")
			q.finish()
		})
	}
}

var contextUsageSettingsRow = regexp.MustCompile(`Context usage +tokens`)

func TestActualAppContextUsageSettingsReload(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("set -psi-test-binary to run actual-app context usage checks")
	}
	f := newContextUsageFixture(t, "")
	q := f.start(t)
	waitScreenState(q, "ready editor", semanticEditorReady)
	q.write("show context usage\r")
	waitScreenState(q, "default percent caption", completedContextUsageScreen("~40.0% / 200k"))
	if got := f.requests.Load(); got != 1 {
		t.Fatalf("provider requests = %d, want 1", got)
	}
	f.closeServer()

	q.write("/settings\r")
	q.wait("Enter/Space change")
	q.write(strings.Repeat("\x1b[B", 9))
	q.waitScreen("→ Context usage")
	q.write("\r")
	waitScreenState(q, "selected tokens row", func(screen *testutil.VTModel) bool {
		return contextUsageSettingsRow.MatchString(screen.Text())
	})
	q.write("\x1b")
	waitScreenState(q, "token caption after picker", func(screen *testutil.VTModel) bool {
		text := screen.Text()
		return strings.Contains(text, "~80k / 200k") && semanticEditorReady(screen) &&
			!strings.Contains(text, "Enter/Space change") && !strings.Contains(text, "Working")
	})

	data, err := os.ReadFile(f.settings)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["contextUsageDisplay"] != "tokens" {
		t.Fatalf("saved contextUsageDisplay = %v, want tokens\n%s", stored["contextUsageDisplay"], data)
	}

	q.write("/quit\r")
	q.finish()

	q = f.start(t)
	waitScreenState(q, "token caption at startup", func(screen *testutil.VTModel) bool {
		return strings.Contains(screen.Text(), "~80k / 200k") && semanticEditorReady(screen)
	})

	f.writeSettings(t, "percent")
	q.write("/reload\r")
	waitScreenState(q, "percent caption after reload", func(screen *testutil.VTModel) bool {
		text := screen.Text()
		return strings.Contains(text, "~40.0% / 200k") &&
			strings.Contains(text, "Reloaded keybindings") && semanticEditorReady(screen)
	})

	f.writeSettings(t, "bogus")
	q.write("/reload\r")
	waitScreenState(q, "invalid display reload error", func(screen *testutil.VTModel) bool {
		text := screen.Text()
		return strings.Contains(text, "contextUsageDisplay") &&
			strings.Contains(text, "~40.0% / 200k") && semanticEditorReady(screen)
	})

	f.writeSettings(t, "percent")
	q.write("/quit\r")
	q.finish()
}
