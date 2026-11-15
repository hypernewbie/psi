//go:build unix

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"psi/internal/provider"
)

func TestActualAppLogoutMVP(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("requires -psi-test-binary for the delivered executable")
	}

	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	authPath := filepath.Join(agentDir, "auth.json")
	writeAppFile(t, authPath, `{"anthropic":{"type":"api_key","key":"anthropic-secret-canary"},"deepseek":{"type":"api_key","key":"deepseek-secret"},"openai-codex":{"type":"oauth","access":"oauth-token"},"custom":{"type":"api_key","key":"custom-secret"}}`)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"deepseek","defaultModel":"deepseek-v4-pro"}`)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--tools", "read")
	closeLoginPTYWatch(t, q)
	waitLoginReady(t, q, "initial prompt", "anthropic-secret-canary")
	q.write("/logout\r")
	waitLoginScreen(t, q, "logout provider picker", "Stored API keys", "anthropic-secret-canary")
	q.write("anthropic\r")
	waitLoginOutput(t, q, "Anthropic key removed", "Removed stored API key for Anthropic", "anthropic-secret-canary")
	q.write("/quit\r")
	finishLoginPTY(t, q, "anthropic-secret-canary")

	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal("could not read auth.json")
	}
	var entries map[string]struct {
		Type   string `json:"type"`
		Key    string `json:"key"`
		Access string `json:"access"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal("could not decode auth.json")
	}
	if _, ok := entries["anthropic"]; ok {
		t.Fatal("logout retained the selected API key")
	}
	if entries["deepseek"].Key != "deepseek-secret" || entries["openai-codex"].Type != "oauth" || entries["openai-codex"].Access != "oauth-token" || entries["custom"].Key != "custom-secret" {
		t.Fatal("logout changed an unrelated API key, OAuth entry, or custom entry")
	}
}

func TestActualAppLoginMVP(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("requires -psi-test-binary for the delivered executable")
	}

	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	authPath := filepath.Join(agentDir, "auth.json")
	settingsPath := filepath.Join(agentDir, "settings.json")
	for _, path := range []string{authPath, settingsPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("fresh-home file %s exists before launch: %v", path, err)
		}
	}

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--tools", "read")
	closeLoginPTYWatch(t, q)
	waitLoginReady(t, q, "initial prompt", "")
	q.write("/login deepseek\r")
	waitLoginScreen(t, q, "login key prompt", "API key for DeepSeek", "")

	const canary = "sk-test-$-!"
	q.write(canary)
	masked := strings.Repeat("*", len([]rune(canary)))
	waitLoginScreen(t, q, "masked key row", masked, canary)
	q.write("\r")
	waitLoginOutput(t, q, "key saved", "Saved API key for DeepSeek", canary)
	waitLoginOutput(t, q, "first-login model selected", "Selected deepseek-v4-pro", canary)
	q.write("/model\r")
	waitLogin(t, q, "model picker lists DeepSeek", canary, func() bool {
		text := q.screen.Text()
		return strings.Contains(text, "Models ·") && strings.Contains(text, "deepseek-v4-pro")
	})
	q.write("\x1b")
	waitLogin(t, q, "model picker canceled", canary, func() bool {
		return semanticEditorReady(q.screen) && !strings.Contains(q.screen.Text(), "Models ·")
	})
	q.write("\x15/quit\r")
	finishLoginPTY(t, q, canary)

	q2 := startReviewPTY(t, home, work, agentDir, "--deny-project", "--tools", "read")
	closeLoginPTYWatch(t, q2)
	waitLoginReady(t, q2, "restart prompt", canary)
	startup := q2.raw.String()
	if strings.Contains(startup, "no DeepSeek credential") || strings.Contains(startup, "offline commands still work") {
		t.Fatal("restart reported a missing credential")
	}
	if !strings.Contains(q2.screen.Text(), "deepseek-v4-pro") {
		t.Fatal("restart did not select the saved default")
	}
	q2.write("/quit\r")
	finishLoginPTY(t, q2, canary)

	if err := checkLoginSessions(agentDir, canary); err != nil {
		t.Fatal(err)
	}
	authData, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal("could not read auth.json")
	}
	var auth map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(authData, &auth); err != nil {
		t.Fatal("could not decode auth.json")
	}
	if got := auth["deepseek"]; got.Type != "api_key" || got.Key != "sk-test-$$-!" {
		t.Fatal("saved DeepSeek auth entry was not Pi-compatible")
	}
	key, source, err := provider.SelectProviderAPIKey(map[string]string{}, authPath, "deepseek", "")
	if err != nil || key != canary || source != "auth.json:deepseek" {
		t.Fatal("saved DeepSeek credential did not resolve to the original key")
	}
	info, err := os.Stat(authPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("auth.json permissions are not 0600")
	}
	dirInfo, err := os.Stat(agentDir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatal("agent directory permissions are not 0700")
	}
	if _, err := os.Stat(settingsPath + ".psi.lock"); !os.IsNotExist(err) {
		t.Fatal("settings writer left a .psi.lock sidecar")
	}
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal("could not read settings.json")
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		t.Fatal("could not decode settings.json")
	}
	if settings["defaultProvider"] != "deepseek" || settings["defaultModel"] != "deepseek-v4-pro" {
		t.Fatal("first-login defaults were not persisted")
	}
}

func TestCheckLoginSessions(t *testing.T) {
	const canary = "login-session-secret-canary"
	for _, tc := range []struct {
		name    string
		setup   func(*testing.T, string)
		wantErr bool
	}{
		{
			name: "empty directory",
			setup: func(t *testing.T, root string) {
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "clean session files",
			setup: func(t *testing.T, root string) {
				writeAppFile(t, filepath.Join(root, "first.jsonl"), `{}`)
				writeAppFile(t, filepath.Join(root, "workspace", "second.jsonl"), `{}`)
			},
		},
		{
			name: "nested canary",
			setup: func(t *testing.T, root string) {
				writeAppFile(t, filepath.Join(root, "first.jsonl"), `{}`)
				writeAppFile(t, filepath.Join(root, "workspace", "second.jsonl"), canary)
			},
			wantErr: true,
		},
		{
			name:    "missing directory",
			wantErr: true,
		},
		{
			name: "non-directory root",
			setup: func(t *testing.T, root string) {
				writeAppFile(t, root, "")
			},
			wantErr: true,
		},
		{
			name: "session read error",
			setup: func(t *testing.T, root string) {
				writeAppFile(t, filepath.Join(root, "first.jsonl"), `{}`)
				if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "unreadable.jsonl")); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentDir := t.TempDir()
			if tc.setup != nil {
				tc.setup(t, filepath.Join(agentDir, "sessions"))
			}
			if err := checkLoginSessions(agentDir, canary); (err != nil) != tc.wantErr {
				t.Fatalf("session scan error = %v, want error = %t", err, tc.wantErr)
			}
		})
	}
}

func closeLoginPTYWatch(t *testing.T, q *reviewPTY) {
	t.Helper()
	if q.tty == nil {
		return
	}
	if err := q.tty.Close(); err != nil {
		t.Fatal("could not close PTY watch descriptor")
	}
	q.tty = nil
}

func waitLoginReady(t *testing.T, q *reviewPTY, stage, canary string) {
	t.Helper()
	waitLogin(t, q, stage, canary, func() bool {
		return strings.Contains(q.screen.Text(), "/ commands") && semanticEditorReady(q.screen)
	})
}

func waitLoginScreen(t *testing.T, q *reviewPTY, stage, want, canary string) {
	t.Helper()
	waitLogin(t, q, stage, canary, func() bool { return strings.Contains(q.screen.Text(), want) })
}

func waitLoginOutput(t *testing.T, q *reviewPTY, stage, want, canary string) {
	t.Helper()
	waitLogin(t, q, stage, canary, func() bool { return strings.Contains(q.raw.String(), want) })
}

func waitLogin(t *testing.T, q *reviewPTY, stage, canary string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for !ready() {
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				t.Fatalf("actual binary closed its PTY before %s", stage)
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
			assertLoginCanaryAbsent(t, q, chunk, canary)
		case <-deadline.C:
			t.Fatalf("actual binary did not reach %s", stage)
		}
	}
}

func assertLoginCanaryAbsent(t *testing.T, q *reviewPTY, chunk []byte, canary string) {
	t.Helper()
	if canary != "" && (bytes.Contains(chunk, []byte(canary)) || strings.Contains(q.raw.String(), canary) || strings.Contains(q.screen.Text(), canary)) {
		t.Fatal("API key appeared in the PTY stream")
	}
}

func finishLoginPTY(t *testing.T, q *reviewPTY, canary string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- q.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("actual binary exited with an error")
		}
	case <-time.After(15 * time.Second):
		_ = q.cmd.Process.Kill()
		<-done
		drainLoginPTY(t, q, canary)
		_ = q.master.Close()
		t.Fatal("actual binary did not exit")
	}
	drainLoginPTY(t, q, canary)
	if err := q.master.Close(); err != nil {
		t.Fatal("could not close PTY master")
	}
}

func drainLoginPTY(t *testing.T, q *reviewPTY, canary string) {
	t.Helper()
	for chunk := range q.chunks {
		q.pending += string(chunk)
		q.raw.Write(chunk)
		q.screen.Feed(chunk)
		assertLoginCanaryAbsent(t, q, chunk, canary)
	}
}

func checkLoginSessions(agentDir, canary string) error {
	root := filepath.Join(agentDir, "sessions")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return os.ErrInvalid
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(canary)) {
			return os.ErrInvalid
		}
		return nil
	})
	if err != nil {
		return os.ErrInvalid
	}
	return nil
}
