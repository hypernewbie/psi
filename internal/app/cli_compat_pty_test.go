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
	"strings"
	"sync"
	"testing"

	"psi/internal/session"
)

func TestActualAppPiStyleInitialPromptAndSessionNameFlags(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var requestBody string
	var bodyMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		bodyMu.Lock()
		requestBody = string(raw)
		bodyMu.Unlock()
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, anthropicPTYText("CLI_INITIAL_DONE"))
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	sessionDir := filepath.Join(home, "sessions")
	q := startReviewPTY(t, home, work, agentDir,
		"INITIAL_CLI_PROMPT", "--deny-project", "--no-tools", "--provider", "anthropic", "--model", "synthetic-model", "--session-dir", sessionDir, "--name", "CLI named", "--system-prompt", "CLI_SYSTEM_PROMPT", "--append-system-prompt", "CLI_APPEND_PROMPT")
	q.wait("CLI_INITIAL_DONE")
	q.prompt()
	infos, err := session.Discover(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "CLI named" {
		t.Fatalf("discovered sessions = %+v, want one named CLI named", infos)
	}
	bodyMu.Lock()
	containsPrompt := strings.Contains(requestBody, "INITIAL_CLI_PROMPT")
	bodyMu.Unlock()
	if !containsPrompt || !strings.Contains(requestBody, "CLI_SYSTEM_PROMPT") || !strings.Contains(requestBody, "CLI_APPEND_PROMPT") {
		t.Fatalf("CLI prompt flags or initial message missing from request body: %s", requestBody)
	}
	if _, err := os.Stat(infos[0].Path); err != nil {
		t.Fatal(err)
	}
	q.write("/quit\r")
	q.finish()
}
