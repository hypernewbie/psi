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
	"sync"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/session"
)

func waitLanguageScreenMatch(t *testing.T, q *reviewPTY, pattern *regexp.Regexp) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if pattern.MatchString(q.screen.Text()) {
			return
		}
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				t.Fatalf("process exited waiting for screen %q: %q", pattern, q.screen.Text())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timer.C:
			t.Fatalf("timeout waiting for screen %q: %q", pattern, q.screen.Text())
		}
	}
}

func waitLanguageScreenAbsent(t *testing.T, q *reviewPTY, text string) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if !strings.Contains(q.screen.Text(), text) {
			return
		}
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				t.Fatalf("process exited waiting for %q to disappear: %q", text, q.screen.Text())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timer.C:
			t.Fatalf("timeout waiting for %q to disappear: %q", text, q.screen.Text())
		}
	}
}

func waitLanguageScreenGone(t *testing.T, q *reviewPTY, pattern *regexp.Regexp) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if !pattern.MatchString(q.screen.Text()) {
			return
		}
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				t.Fatalf("process exited waiting for %q to disappear: %q", pattern, q.screen.Text())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timer.C:
			t.Fatalf("timeout waiting for %q to disappear: %q", pattern, q.screen.Text())
		}
	}
}

func waitLanguageEditorReady(t *testing.T, q *reviewPTY) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for !semanticEditorReady(q.screen) {
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				t.Fatalf("process exited before editor became ready: %q", q.raw.String())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timer.C:
			t.Fatalf("timeout waiting for editor: %q", q.raw.String())
		}
	}
}

// TestActualAppLanguageMVP proves the shipped app sends the Chinese built-in
// prompt from settings in its first provider request.
func TestActualAppLanguageMVP(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"lang-mvp\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"LANGUAGE_MVP_DONE\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	t.Cleanup(server.Close)
	reviewSettings(t, agentDir, server.URL)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","defaultThinkingLevel":"off","defaultTools":["read","bash"],"language":"zh","compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "skills", "demo", "SKILL.md"), "---\nname: demo\ndescription: Demo skill for prompt shape\n---\nSKILL_BODY\n")
	writeAppFile(t, filepath.Join(work, "AGENTS.md"), "PROJECT_CONTEXT_MARKER")
	writeAppFile(t, filepath.Join(agentDir, "APPEND_SYSTEM.md"), "APPEND_SYSTEM_MARKER")

	q := startReviewPTY(t, home, work, agentDir, "--deny-project")
	waitLanguageEditorReady(t, q)
	q.write("hello\r")
	q.wait("LANGUAGE_MVP_DONE")
	waitLanguageEditorReady(t, q)

	mu.Lock()
	if len(bodies) != 1 {
		t.Fatalf("provider requests before finish = %d, want 1", len(bodies))
	}
	first := bodies[0]
	mu.Unlock()

	var request map[string]any
	if err := json.Unmarshal(first, &request); err != nil {
		t.Fatal(err)
	}
	system, _ := request["system"].(string)
	if system == "" {
		t.Fatalf("request has no system prompt: %s", first)
	}
	for _, want := range []string{
		"你是 psi，一个极简的 Go 编程智能体。使用已声明的工具检查和修改工作区。",
		"<tools>\n- read: 读取文件内容\n- bash: 执行 shell 命令\n- read_session: 从较早的原始会话历史中读取一小段摘录\n</tools>",
		"默认使用简体中文回复，除非用户明确要求使用其他语言。代码、命令、路径和标识符保持原样。",
		"<docs>\nPsi 文档：",
		"运行 `psi --docs` 列出主题。",
		"<project_context>\n项目专用指令与指南：",
		"PROJECT_CONTEXT_MARKER",
		"APPEND_SYSTEM_MARKER",
		"Demo skill for prompt shape",
		"<available_skills>",
		"<cwd>",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, system)
		}
	}
	for _, absent := range []string{
		"You are psi, a minimal Go coding agent.",
		"Be concise in your responses",
		"Use bash for file operations like ls, rg, find",
		"The following skills provide specialized instructions",
		"SKILL_BODY",
	} {
		if strings.Contains(system, absent) {
			t.Fatalf("system prompt contains English/leaked text %q:\n%s", absent, system)
		}
	}

	matches, err := filepath.Glob(filepath.Join(agentDir, "sessions", "*", "*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("session files in %s = %v (%v)", agentDir, matches, err)
	}
	records := sessionSystems(t, matches[0])
	if len(records) != 1 {
		t.Fatalf("system records = %d, want 1", len(records))
	}
	keys, values := sectionBodies(t, records[0])
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, values[key])
	}
	if rendered := strings.Join(parts, "\n\n"); rendered != system {
		t.Fatalf("request system differs from stored sections:\n got %q\nwant %q", system, rendered)
	}
	for _, want := range []string{"preamble", "tools", "rules", "docs", "addendum", "project_context", "skills", "cwd"} {
		if !contains(keys, want) {
			t.Fatalf("stored sections missing %q: %v", want, keys)
		}
	}

	q.write("/quit\r")
	q.finish()
	server.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("provider requests = %d, want exactly 1", len(bodies))
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type languageRequestLog struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (l *languageRequestLog) add(body []byte) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bodies = append(l.bodies, body)
	return len(l.bodies)
}

func (l *languageRequestLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

func (l *languageRequestLog) system(t *testing.T, index int) string {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if index >= len(l.bodies) {
		t.Fatalf("provider request %d missing; got %d", index+1, len(l.bodies))
	}
	var request map[string]any
	if err := json.Unmarshal(l.bodies[index], &request); err != nil {
		t.Fatal(err)
	}
	system, _ := request["system"].(string)
	if system == "" {
		t.Fatalf("provider request %d has no system prompt", index+1)
	}
	return system
}

func newLanguageProvider(t *testing.T) (*httptest.Server, *languageRequestLog) {
	t.Helper()
	log := &languageRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		turn := log.add(body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"message\":{\"id\":\"lang-%d\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n", turn)
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"LANG_DONE_%d\"}}\n\n", turn)
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	t.Cleanup(server.Close)
	return server, log
}

func TestActualAppLanguageControls(t *testing.T) {
	t.Run("cli override survives reload", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		server, requests := newLanguageProvider(t)
		reviewSettings(t, agentDir, server.URL)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--language", "zh")
		waitLanguageEditorReady(t, q)

		q.write("first\r")
		q.wait("LANG_DONE_1")
		waitLanguageEditorReady(t, q)
		assertPromptLanguage(t, requests.system(t, 0), config.LanguageChinese)

		q.write("/reload\r")
		q.wait("Reloaded keybindings, extensions, skills, prompts, themes, and context files")
		waitLanguageEditorReady(t, q)
		q.write("second\r")
		q.wait("LANG_DONE_2")
		waitLanguageEditorReady(t, q)
		assertPromptLanguage(t, requests.system(t, 1), config.LanguageChinese)

		q.write("/quit\r")
		q.finish()
		server.Close()
		if got := requests.count(); got != 2 {
			t.Fatalf("provider requests = %d, want 2", got)
		}
	})

	t.Run("settings reload changes the next request", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		server, requests := newLanguageProvider(t)
		reviewSettings(t, agentDir, server.URL)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project")
		waitLanguageEditorReady(t, q)

		q.write("first\r")
		q.wait("LANG_DONE_1")
		waitLanguageEditorReady(t, q)
		assertPromptLanguage(t, requests.system(t, 0), config.LanguageEnglish)

		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","language":"zh","compaction":{"enabled":false}}`)
		q.write("/reload\r")
		q.wait("Reloaded keybindings, extensions, skills, prompts, themes, and context files")
		waitLanguageEditorReady(t, q)
		q.write("second\r")
		q.wait("LANG_DONE_2")
		waitLanguageEditorReady(t, q)
		assertPromptLanguage(t, requests.system(t, 1), config.LanguageChinese)

		q.write("/quit\r")
		q.finish()
		server.Close()
		if got := requests.count(); got != 2 {
			t.Fatalf("provider requests = %d, want 2", got)
		}
	})

	t.Run("resume ignores the stored prompt language", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		server, requests := newLanguageProvider(t)
		reviewSettings(t, agentDir, server.URL)
		store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
		if err != nil {
			t.Fatal(err)
		}
		stored := "You are psi, a minimal Go coding agent. Stored English prompt."
		if _, err := store.AppendMessage(session.AgentMessage{Role: "system", System: &session.SystemMessage{Role: "system", Content: "", Sections: map[string]*string{"preamble": &stored}, Timestamp: 1}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(agentMessageUser("old question")); err != nil {
			t.Fatal(err)
		}
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--language", "zh", "--session", store.File())
		q.waitScreen("old question")
		waitLanguageEditorReady(t, q)
		q.write("new question\r")
		q.wait("LANG_DONE_1")
		waitLanguageEditorReady(t, q)
		system := requests.system(t, 0)
		assertPromptLanguage(t, system, config.LanguageChinese)
		if strings.Contains(system, "Stored English prompt") {
			t.Fatalf("resume replayed the stored English prompt:\n%s", system)
		}
		q.write("/quit\r")
		q.finish()
		server.Close()
		if got := requests.count(); got != 1 {
			t.Fatalf("provider requests = %d, want 1", got)
		}
	})
}

func openLanguageRow(t *testing.T, q *reviewPTY) {
	t.Helper()
	q.write("/settings\r")
	waitLanguageScreenMatch(t, q, regexp.MustCompile(`→ (Auto-compact|自\s*动\s*压\s*缩)`))
	for i := 0; i < 11; i++ {
		q.write("\x1b[B")
	}
	waitLanguageScreenMatch(t, q, regexp.MustCompile(`→ (Language|语\s*言)`))
}

func closeLanguageRow(t *testing.T, q *reviewPTY) {
	t.Helper()
	q.write("\x03")
	// The hint follows the settings language, so wait for either wording to go.
	waitLanguageScreenGone(t, q, regexp.MustCompile(`Enter/(Space change|空\s*格)`))
	waitLanguageEditorReady(t, q)
}

func readStoredLanguage(t *testing.T, agentDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(agentDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	language, _ := stored["language"].(string)
	return language
}

func TestActualAppLanguageSettings(t *testing.T) {
	t.Run("menu switches and persists", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		server, requests := newLanguageProvider(t)
		reviewSettings(t, agentDir, server.URL)
		first := startReviewPTY(t, home, work, agentDir, "--deny-project")
		waitLanguageEditorReady(t, first)
		openLanguageRow(t, first)
		first.write("\r")
		waitLanguageScreenMatch(t, first, regexp.MustCompile(`→ (Language|语\s*言)\s+简\s*体\s*中\s*文`))
		closeLanguageRow(t, first)
		if got := readStoredLanguage(t, agentDir); got != config.LanguageChinese {
			t.Fatalf("stored language = %q, want zh", got)
		}
		first.write("hello\r")
		first.wait("LANG_DONE_1")
		waitLanguageEditorReady(t, first)
		assertPromptLanguage(t, requests.system(t, 0), config.LanguageChinese)
		first.write("/quit\r")
		first.finish()

		second := startReviewPTY(t, home, work, agentDir, "--deny-project")
		waitLanguageEditorReady(t, second)
		openLanguageRow(t, second)
		waitLanguageScreenMatch(t, second, regexp.MustCompile(`→ (Language|语\s*言)\s+简\s*体\s*中\s*文`))
		closeLanguageRow(t, second)
		second.write("/quit\r")
		second.finish()
		server.Close()
		if got := requests.count(); got != 1 {
			t.Fatalf("provider requests = %d, want 1", got)
		}
	})

	t.Run("menu follows the stored language", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		reviewSettings(t, agentDir, "http://127.0.0.1:9")
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","language":"zh","compaction":{"enabled":false}}`)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project")
		waitLanguageEditorReady(t, q)
		q.write("/settings\r")
		waitLanguageScreenMatch(t, q, regexp.MustCompile(`→ 自\s*动\s*压\s*缩`))
		waitLanguageScreenMatch(t, q, regexp.MustCompile(`Enter/空\s*格`))
		if strings.Contains(q.screen.Text(), "Enter/Space change") {
			t.Fatalf("English settings hint kept in Chinese mode:\n%s", q.screen.Text())
		}
		q.write("\x03")
		waitLanguageEditorReady(t, q)
		q.write("/quit\r")
		q.finish()
	})

	t.Run("cli override notice", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		reviewSettings(t, agentDir, "http://127.0.0.1:9")
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--language", "zh")
		waitLanguageEditorReady(t, q)
		openLanguageRow(t, q)
		q.write("\r")
		waitLanguageScreenMatch(t, q, regexp.MustCompile(`Stored the default language`))
		if strings.Contains(q.screen.Text(), "Saved for this session only") {
			t.Fatalf("stored-default notice was replaced by session-only:\n%s", q.screen.Text())
		}
		closeLanguageRow(t, q)
		if got := readStoredLanguage(t, agentDir); got != config.LanguageChinese {
			t.Fatalf("stored language = %q, want zh", got)
		}
		q.write("/quit\r")
		q.finish()
	})

	t.Run("corrupt file stays session only", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		reviewSettings(t, agentDir, "http://127.0.0.1:9")
		settingsPath := filepath.Join(agentDir, "settings.json")
		corrupt := []byte(`{"broken":`)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project")
		waitLanguageEditorReady(t, q)
		if err := os.WriteFile(settingsPath, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		openLanguageRow(t, q)
		q.write("\r")
		waitLanguageScreenMatch(t, q, regexp.MustCompile(`Saved for this session only`))
		if strings.Contains(q.screen.Text(), "Stored the default language") {
			t.Fatalf("false stored-default notice:\n%s", q.screen.Text())
		}
		closeLanguageRow(t, q)
		after, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(corrupt) {
			t.Fatalf("corrupt file changed: %q", after)
		}
		q.write("/quit\r")
		q.finish()
	})

	t.Run("cli override with corrupt file shows only session-only", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, "agent")
		reviewSettings(t, agentDir, "http://127.0.0.1:9")
		settingsPath := filepath.Join(agentDir, "settings.json")
		corrupt := []byte(`{"broken":`)
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--language", "zh")
		waitLanguageEditorReady(t, q)
		if err := os.WriteFile(settingsPath, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		openLanguageRow(t, q)
		q.write("\r")
		waitLanguageScreenMatch(t, q, regexp.MustCompile(`Saved for this session only`))
		if strings.Contains(q.screen.Text(), "Stored the default language") {
			t.Fatalf("false stored-default notice with corrupt settings:\n%s", q.screen.Text())
		}
		closeLanguageRow(t, q)
		after, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(corrupt) {
			t.Fatalf("corrupt file changed: %q", after)
		}
		q.write("/quit\r")
		q.finish()
	})
}

func TestActualAppLanguageStreamFailure(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	requests := &languageRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		turn := requests.add(body)
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"partial\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PARTIAL_OUTPUT\"}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
			return
		}
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"complete\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"LANG_DONE_%d\"}}\n\n", turn)
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	t.Cleanup(server.Close)
	reviewSettings(t, agentDir, server.URL)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","language":"zh","compaction":{"enabled":false}}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project")
	waitLanguageEditorReady(t, q)
	q.write("partial\r")
	q.wait("PARTIAL_OUTPUT")
	waitLanguageScreenMatch(t, q, regexp.MustCompile(`incomplete provider\s+response`))
	waitLanguageEditorReady(t, q)
	assertPromptLanguage(t, requests.system(t, 0), config.LanguageChinese)

	openLanguageRow(t, q)
	waitLanguageScreenMatch(t, q, regexp.MustCompile(`→ (Language|语\s*言)\s+简\s*体\s*中\s*文`))
	q.write("\r")
	waitLanguageScreenMatch(t, q, regexp.MustCompile(`→ Language\s+English`))
	closeLanguageRow(t, q)
	q.write("second\r")
	q.wait("LANG_DONE_2")
	waitLanguageEditorReady(t, q)
	assertPromptLanguage(t, requests.system(t, 1), config.LanguageEnglish)

	q.write("/quit\r")
	q.finish()
	server.Close()
	if got := requests.count(); got != 2 {
		t.Fatalf("provider requests = %d, want exactly 2", got)
	}
}
