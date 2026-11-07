//go:build unix

package app

import (
	"bytes"
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

// sessionSystems returns the message payload of every system record in path.
func sessionSystems(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []json.RawMessage
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatalf("decode session line: %v", err)
		}
		var probe struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(envelope["message"], &probe); err != nil {
			continue
		}
		if probe.Role == "system" {
			out = append(out, envelope["message"])
		}
	}
	return out
}

// sectionBodies returns section bodies in prompt order, not JSON key order.
func sectionBodies(t *testing.T, record json.RawMessage) ([]string, map[string]string) {
	t.Helper()
	var message map[string]json.RawMessage
	if err := json.Unmarshal(record, &message); err != nil {
		t.Fatal(err)
	}
	if string(message["content"]) != `""` {
		t.Fatalf("system content = %s, want an empty string", message["content"])
	}
	var bodies map[string]string
	if err := json.Unmarshal(message["sections"], &bodies); err != nil {
		t.Fatalf("sections is not an object: %s", message["sections"])
	}
	return session.OrderedSectionNames(bodies), bodies
}

// TestActualAppPromptSections proves the real app sends the sectioned prompt:
// the first provider request body must carry pi's section order.
func TestActualAppPromptSections(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":["read","bash"],"compaction":{"enabled":false},"outputPad":0}`)
	var mu sync.Mutex
	var bodies [][]byte
	turns := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		turns++
		turn := turns
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"message\":{\"id\":\"shape-%d\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1}}}\n\n", turn)
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PROMPT_SHAPE_DONE\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-shape-key"}}`)
	writeAppFile(t, filepath.Join(agentDir, "skills", "demo", "SKILL.md"), "---\nname: demo\ndescription: Demo skill for prompt shape\n---\nSKILL_BODY\n")
	writeAppFile(t, filepath.Join(work, "AGENTS.md"), "PROJECT_CONTEXT_MARKER")
	writeAppFile(t, filepath.Join(agentDir, "APPEND_SYSTEM.md"), "APPEND_SYSTEM_MARKER")

	q := startReviewPTY(t, home, work, agentDir, "--deny-project")
	q.prompt()
	q.write("hello\r")
	q.wait("PROMPT_SHAPE_DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()

	mu.Lock()
	first := bodies[0]
	mu.Unlock()
	var request map[string]any
	if err := json.Unmarshal(first, &request); err != nil {
		t.Fatal(err)
	}
	system, _ := request["system"].(string)
	if system == "" {
		t.Fatalf("request has no system prompt: %s", bodies[0])
	}
	last := -1
	for _, want := range []string{"You are psi", "<tools>", "<rules>", "<docs>", "<addendum>", "<project_context>", "<skills>", "<cwd>"} {
		at := strings.Index(system, want)
		if at < 0 {
			t.Fatalf("system prompt missing %q: %s", want, system)
		}
		if at < last {
			t.Fatalf("system section %q out of order: %s", want, system)
		}
		last = at
	}
	for _, want := range []string{"- read: Read file contents", "PROJECT_CONTEXT_MARKER", "APPEND_SYSTEM_MARKER", "Demo skill for prompt shape"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt missing %q: %s", want, system)
		}
	}
	for _, absent := range []string{"Available tools:", "Workspace:", "SKILL_BODY"} {
		if strings.Contains(system, absent) {
			t.Fatalf("system prompt contains legacy/leaked text %q: %s", absent, system)
		}
	}

	sessionFile := filepath.Join(agentDir, "sessions")
	matches, err := filepath.Glob(filepath.Join(sessionFile, "*", "*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("session files in %s = %v (%v)", sessionFile, matches, err)
	}
	records := sessionSystems(t, matches[0])
	if len(records) != 1 {
		t.Fatalf("system records = %d, want 1", len(records))
	}
	sectionKeys, sectionText := sectionBodies(t, records[0])
	wantKeys := []string{"preamble", "tools", "rules", "docs", "addendum", "project_context", "skills", "cwd"}
	if strings.Join(sectionKeys, " ") != strings.Join(wantKeys, " ") {
		t.Fatalf("session section order = %v, want %v", sectionKeys, wantKeys)
	}
	parts := make([]string, 0, len(sectionKeys))
	for _, key := range sectionKeys {
		parts = append(parts, sectionText[key])
	}
	if rendered := strings.Join(parts, "\n\n"); rendered != system {
		t.Fatalf("request system differs from session sections:\n got %q\nwant %q", system, rendered)
	}

	// Reopening the same session must not append another system record.
	second := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", matches[0])
	second.prompt()
	second.write("again\r")
	second.wait("PROMPT_SHAPE_DONE")
	second.prompt()
	if again := sessionSystems(t, matches[0]); len(again) != len(records) {
		t.Fatalf("second run appended a system record: %d, want %d", len(again), len(records))
	}

	// A custom prompt removes the default sections. Restoring the default
	// after /reload must restore their position, not append them after cwd.
	systemFile := filepath.Join(agentDir, "SYSTEM.md")
	writeAppFile(t, systemFile, "RELOADED_CUSTOM_BASE")
	second.write("/reload\r")
	second.wait("Reloaded keybindings, extensions, skills, prompts, themes, and context files")
	second.prompt()
	second.write("custom prompt\r")
	second.wait("PROMPT_SHAPE_DONE")
	second.prompt()
	if err := os.Remove(systemFile); err != nil {
		t.Fatal(err)
	}
	second.write("/reload\r")
	second.wait("Reloaded keybindings, extensions, skills, prompts, themes, and context files")
	second.prompt()
	second.write("restored prompt\r")
	second.wait("PROMPT_SHAPE_DONE")
	second.prompt()
	second.write("/quit\r")
	second.finish()
	if records := sessionSystems(t, matches[0]); len(records) != 3 {
		t.Fatalf("system records after reload cycle = %d, want 3 without reset records", len(records))
	}

	mu.Lock()
	requests := append([][]byte(nil), bodies...)
	mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("provider requests = %d, want 4", len(requests))
	}
	for i, body := range requests[2:] {
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		got, _ := request["system"].(string)
		if i == 0 {
			if !strings.HasPrefix(got, "RELOADED_CUSTOM_BASE\n\n") || strings.Contains(got, "<tools>") || strings.Contains(got, "<docs>") {
				t.Fatalf("reload did not install the custom prompt: %q", got)
			}
		} else if got != system {
			t.Fatalf("reload changed the restored prompt order:\n got %q\nwant %q", got, system)
		}
	}
}
