//go:build unix

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/provider"
)

func TestActualAppLogoutMVP(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("requires -psi-test-binary for the delivered executable")
	}

	t.Run("anthropic", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, ".pi", "agent")
		authPath := filepath.Join(agentDir, "auth.json")
		writeAppFile(t, authPath, `{"anthropic":{"type":"api_key","key":"anthropic-secret-canary"},"deepseek":{"type":"api_key","key":"deepseek-secret"},"openai-codex":{"type":"oauth","access":"oauth-token"},"custom":{"type":"api_key","key":"custom-secret"}}`)
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"deepseek","defaultModel":"deepseek-v4-pro"}`)

		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--tools", "read")
		closeLoginPTYWatch(t, q)
		waitLoginReady(t, q, "initial prompt", "anthropic-secret-canary")
		q.write("/logout\r")
		waitLoginScreen(t, q, "logout provider picker", "Stored credentials", "anthropic-secret-canary")
		baseline, err := os.ReadFile(authPath)
		if err != nil {
			t.Fatal("could not read auth.json before confirmation")
		}
		q.write("anthropic\r")
		waitLoginScreen(t, q, "logout confirmation", "Log out of Anthropic?", "anthropic-secret-canary")
		waitLoginScreen(t, q, "logout confirmation choices", "Y — Yes", "anthropic-secret-canary")
		if data, err := os.ReadFile(authPath); err != nil || !bytes.Equal(data, baseline) {
			t.Fatalf("logout changed auth.json before confirmation: err=%v", err)
		}
		q.write("n")
		waitLoginScreen(t, q, "logout cancellation", "Stored credentials", "anthropic-secret-canary")
		if data, err := os.ReadFile(authPath); err != nil || !bytes.Equal(data, baseline) {
			t.Fatalf("cancel changed auth.json: err=%v", err)
		}
		q.write("\r")
		waitLoginScreen(t, q, "logout confirmation after cancel", "Log out of Anthropic?", "anthropic-secret-canary")
		q.write("Y")
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
	})

	t.Run("codex", func(t *testing.T) {
		const (
			canary     = "codex-pty-fixture-canary"
			targetMode = 0o640
		)
		home, work := t.TempDir(), t.TempDir()
		agentDir := filepath.Join(home, ".pi", "agent")
		authPath := filepath.Join(agentDir, "auth.json")
		writeAppFile(t, authPath, `{"custom":{"type":"api_key","key":"custom-secret"},"deepseek":{"type":"api_key","key":"`+canary+`"},"openai-codex":{"type":"oauth"}}`)
		if err := os.Chmod(authPath, targetMode); err != nil {
			t.Fatal("could not set auth mode")
		}
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"deepseek","defaultModel":"deepseek-v4-pro"}`)

		q := startReviewProcessEnv(t, home, work, agentDir, *reviewBinary, "", []string{
			"HTTP_PROXY=http://127.0.0.1:0",
			"HTTPS_PROXY=http://127.0.0.1:0",
		}, "--deny-project", "--tools", "read")
		closeLoginPTYWatch(t, q)
		waitLoginReady(t, q, "initial prompt", canary)
		q.write("/logout\r")
		waitLoginScreen(t, q, "logout provider picker", "OpenAI Codex · OAuth credential", canary)
		q.write("openai-codex\r")
		waitLoginScreen(t, q, "Codex logout confirmation", "Log out of OpenAI Codex?", canary)
		q.write("y")
		waitLoginOutput(t, q, "Codex OAuth credential removed", "Removed stored OAuth credential for OpenAI Codex", canary)
		waitLoginOutput(t, q, "Codex remote outcome", "No usable token", canary)
		q.write("/quit\r")
		finishLoginPTY(t, q, canary)

		data, err := os.ReadFile(authPath)
		if err != nil {
			t.Fatal("could not read auth.json")
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal("could not decode auth.json")
		}
		want := map[string]any{
			"custom":   map[string]any{"type": "api_key", "key": "custom-secret"},
			"deepseek": map[string]any{"type": "api_key", "key": canary},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("logout changed unrelated entries: %v", got)
		}
		info, err := os.Stat(authPath)
		if err != nil {
			t.Fatal("could not inspect auth mode")
		}
		if info.Mode().Perm() != targetMode {
			t.Fatalf("auth mode = %o, want %o", info.Mode().Perm(), targetMode)
		}
	})

	t.Run("openai-oauth", func(t *testing.T) {
		runOpenAILogoutFixture(t, `{"type":"oauth"}`, "OpenAI · OAuth credential", "Removed stored OAuth credential for OpenAI", "No usable token")
	})

	t.Run("openai-api-key", func(t *testing.T) {
		runOpenAILogoutFixture(t, `{"type":"api_key","key":"openai-pty-key-canary"}`, "OpenAI · API key", "Removed stored API key for OpenAI", "")
	})
}

// runOpenAILogoutFixture exercises one direct-openai stored credential through
// the delivered binary. The DeepSeek key keeps the active model offline from
// Codex quota polling and gives the PTY a secret canary.
func runOpenAILogoutFixture(t *testing.T, openAIEntry, row, success, remote string) {
	t.Helper()
	const canary = "openai-pty-fixture-canary"
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	authPath := filepath.Join(agentDir, "auth.json")
	doc := `{"custom":{"type":"api_key","key":"custom-secret"},"deepseek":{"type":"api_key","key":"` + canary + `"},"openai":` + openAIEntry + `}`
	writeAppFile(t, authPath, doc)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"deepseek","defaultModel":"deepseek-v4-pro"}`)

	q := startReviewProcessEnv(t, home, work, agentDir, *reviewBinary, "", []string{
		"HTTP_PROXY=http://127.0.0.1:0",
		"HTTPS_PROXY=http://127.0.0.1:0",
	}, "--deny-project", "--tools", "read")
	closeLoginPTYWatch(t, q)
	waitLoginReady(t, q, "initial prompt", canary)
	q.write("/logout\r")
	waitLoginScreen(t, q, "logout provider picker", row, canary)
	q.write("openai\r")
	waitLoginScreen(t, q, "OpenAI logout confirmation", "Log out of OpenAI?", canary)
	q.write("y")
	waitLoginOutput(t, q, "OpenAI credential removed", success, canary)
	if remote != "" {
		waitLoginOutput(t, q, "OpenAI remote outcome", remote, canary)
	}
	q.write("/quit\r")
	finishLoginPTY(t, q, canary)

	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal("could not read auth.json")
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal("could not decode auth.json")
	}
	want := map[string]any{
		"custom":   map[string]any{"type": "api_key", "key": "custom-secret"},
		"deepseek": map[string]any{"type": "api_key", "key": canary},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logout changed unrelated entries: %v", got)
	}
}

func TestActualAppAuthSymlinkMVP(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("requires -psi-test-binary for the delivered executable")
	}

	const (
		enteredKey = "!sk-test-$-!"
		storedKey  = "$!sk-test-$$-!"
		customKey  = "custom-fixture-canary"
		targetMode = 0o640
	)

	home, work, targetDir := t.TempDir(), t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	authPath := filepath.Join(agentDir, "auth.json")
	targetPath := filepath.Join(targetDir, "auth.json")
	writeAppFile(t, targetPath, `{"deepseek":{"type":"api_key","unknown":{"keep":true}},"custom":{"type":"api_key","key":"`+customKey+`"}}`)
	if err := os.Chmod(targetPath, targetMode); err != nil {
		t.Fatal("could not set target auth mode")
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal("could not create agent directory")
	}
	rel, err := filepath.Rel(agentDir, targetPath)
	if err != nil {
		t.Fatal("could not compute auth link")
	}
	if err := os.Symlink(rel, authPath); err != nil {
		t.Fatal("could not create auth symlink")
	}
	assertAuthSymlink(t, authPath, targetPath, rel, targetMode)

	proxyGuard := []string{
		"HTTP_PROXY=http://127.0.0.1:0",
		"HTTPS_PROXY=http://127.0.0.1:0",
	}
	q := startReviewProcessEnv(t, home, work, agentDir, *reviewBinary, "", proxyGuard, "--deny-project", "--tools", "read")
	closeLoginPTYWatch(t, q)
	waitLoginReady(t, q, "initial prompt", "")
	q.write("/login deepseek\r")
	waitLoginScreen(t, q, "login key prompt", "API key for DeepSeek", "")

	q.write(enteredKey)
	masked := strings.Repeat("*", len([]rune(enteredKey)))
	waitLoginScreen(t, q, "masked key row", masked, enteredKey)
	q.write("\r")
	waitLogin(t, q, "linked save outcome", enteredKey, func() bool {
		out := q.raw.String()
		return strings.Contains(out, "Saved API key for DeepSeek") || strings.Contains(out, "refusing to write a symlinked auth.json")
	})
	if strings.Contains(q.raw.String(), "refusing to write a symlinked auth.json") {
		t.Fatal("linked auth save refused: refusing to write a symlinked auth.json")
	}
	waitLoginOutput(t, q, "key saved", "Saved API key for DeepSeek", enteredKey)
	waitLoginOutput(t, q, "first-login model selected", "Selected deepseek-v4-pro", enteredKey)
	assertAuthSymlink(t, authPath, targetPath, rel, targetMode)

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal("could not read target auth.json")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal("could not decode target auth.json")
	}
	var deepseek struct {
		Type    string         `json:"type"`
		Key     string         `json:"key"`
		Unknown map[string]any `json:"unknown"`
	}
	if err := json.Unmarshal(doc["deepseek"], &deepseek); err != nil {
		t.Fatal("could not decode saved DeepSeek entry")
	}
	if deepseek.Type != "api_key" || deepseek.Key != storedKey || deepseek.Unknown["keep"] != true {
		t.Fatal("saved target entry or unknown field changed")
	}
	key, source, err := provider.SelectProviderAPIKey(map[string]string{}, authPath, "deepseek", "")
	if err != nil || key != enteredKey || source != "auth.json:deepseek" {
		t.Fatal("linked auth credential did not resolve to the entered key")
	}
	var custom struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(doc["custom"], &custom); err != nil || custom.Type != "api_key" || custom.Key != customKey {
		t.Fatal("login changed the unrelated custom entry")
	}
	q.write("/quit\r")
	finishLoginPTY(t, q, enteredKey)

	q2 := startReviewProcessEnv(t, home, work, agentDir, *reviewBinary, "", proxyGuard, "--deny-project", "--tools", "read")
	closeLoginPTYWatch(t, q2)
	waitLoginReady(t, q2, "restart prompt", enteredKey)
	if strings.Contains(q2.raw.String(), "no DeepSeek credential") || strings.Contains(q2.raw.String(), "offline commands still work") {
		t.Fatal("restart reported a missing credential")
	}
	if !strings.Contains(q2.screen.Text(), "deepseek-v4-pro") {
		t.Fatal("restart did not select the saved model")
	}
	assertAuthSymlink(t, authPath, targetPath, rel, targetMode)
	q2.write("/logout\r")
	waitLoginScreen(t, q2, "logout provider picker", "Stored credentials", enteredKey)
	q2.write("deepseek\r")
	waitLoginScreen(t, q2, "DeepSeek logout confirmation", "Log out of DeepSeek?", enteredKey)
	q2.write("y")
	waitLoginOutput(t, q2, "DeepSeek key removed", "Removed stored API key for DeepSeek", enteredKey)
	assertAuthSymlink(t, authPath, targetPath, rel, targetMode)
	q2.write("/quit\r")
	finishLoginPTY(t, q2, enteredKey)

	data, err = os.ReadFile(targetPath)
	if err != nil {
		t.Fatal("could not reread target auth.json")
	}
	doc = nil
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal("could not decode target auth.json after logout")
	}
	if _, ok := doc["deepseek"]; ok {
		t.Fatal("logout retained the selected key")
	}
	if err := json.Unmarshal(doc["custom"], &custom); err != nil || custom.Type != "api_key" || custom.Key != customKey {
		t.Fatal("logout changed the unrelated custom entry")
	}
	for _, canary := range []string{enteredKey, storedKey, customKey} {
		if strings.Contains(q.raw.String(), canary) || strings.Contains(q2.raw.String(), canary) {
			t.Fatal("synthetic API key appeared in PTY output")
		}
		if err := checkLoginSessions(agentDir, canary); err != nil {
			t.Fatal("synthetic API key appeared in session output")
		}
	}
}

func assertAuthSymlink(t *testing.T, authPath, targetPath, wantLink string, wantMode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(authPath)
	if err != nil {
		t.Fatal("could not inspect auth symlink")
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("auth.json is not a symlink")
	}
	link, err := os.Readlink(authPath)
	if err != nil {
		t.Fatal("could not read auth symlink")
	}
	if link != wantLink {
		t.Fatal("auth symlink text changed")
	}
	target, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal("could not inspect auth target")
	}
	if !target.Mode().IsRegular() {
		t.Fatal("auth target is not a regular file")
	}
	if target.Mode().Perm() != wantMode {
		t.Fatalf("auth target mode = %04o, want %04o", target.Mode().Perm(), wantMode)
	}
}

func TestActualAppSecretNavigationMVP(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("requires -psi-test-binary for the delivered executable")
	}

	// The keycap starts the second navigation row. Up from its 78-cell end
	// must stop before the first row's final character: the following source
	// boundary belongs to the second row, not the first.
	const keycap = "1\u20e3" // DIGIT ONE + COMBINING ENCLOSING KEYCAP
	enteredKey := strings.Repeat("x", 78) + keycap + strings.Repeat("z", 76)
	wantKey := strings.Repeat("x", 77) + "-x" + keycap + strings.Repeat("z", 76)
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	authPath := filepath.Join(agentDir, "auth.json")

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--tools", "read")
	closeLoginPTYWatch(t, q)
	waitLoginReady(t, q, "initial prompt", "")
	q.write("/login deepseek\r")
	waitLoginScreen(t, q, "login key prompt", "API key for DeepSeek", "")
	q.write(enteredKey + "\x1b[A-\r")
	waitLoginOutput(t, q, "edited key saved", "Saved API key for DeepSeek", enteredKey)
	q.write("/quit\r")
	finishLoginPTY(t, q, enteredKey)

	key, source, err := provider.SelectProviderAPIKey(map[string]string{}, authPath, "deepseek", "")
	if err != nil || source != "auth.json:deepseek" || key != wantKey {
		t.Fatal("saved key did not follow the editor's soft-wrap cursor ownership")
	}
	if strings.Contains(q.raw.String(), wantKey) {
		t.Fatal("edited synthetic API key appeared in PTY output")
	}
	for _, canary := range []string{enteredKey, wantKey} {
		if err := checkLoginSessions(agentDir, canary); err != nil {
			t.Fatal("synthetic API key appeared in session output")
		}
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
