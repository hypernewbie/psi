//go:build linux || darwin

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileTokenProvider(t *testing.T) {
	for _, tc := range []struct {
		name, body, mode, id, want string
	}{
		{"json pointer", `{"nested":{"x/y":["synthetic-token"]}}`, "json", "/nested/x~1y/0", "synthetic-token"},
		{"single value", "\xef\xbb\xbfsynthetic-token\r\n", "singleValue", "value", "synthetic-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{
				secrets: { providers: { local: { source: "file", path: %q, mode: %q } } },
				channels: { discord: { token: { source: "file", provider: "local", id: %q } } }
			}`, path, tc.mode, tc.id)
			settings, err := decodeSettings([]byte(body), func(string) (string, bool) { return "", false })
			if err != nil {
				t.Fatal(err)
			}
			got, err := settings.resolveConfiguredToken(func(string) (string, bool) { return "", false })
			if err != nil || got != tc.want {
				t.Fatalf("token resolution failed: %v", err)
			}
		})
	}
}

func TestFileTokenUsesDefaultProviderAndPathTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("synthetic-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{
		secrets: { defaults: { file: "local" }, providers: { local: { source: "file", path: "${FILE_PATH}", mode: "singleValue" } } },
		channels: { discord: { token: { source: "file", id: "value" } } }
	}`
	lookup := func(name string) (string, bool) { return path, name == "FILE_PATH" }
	settings, err := decodeSettings([]byte(body), lookup)
	if err != nil {
		t.Fatal(err)
	}
	got, err := settings.resolveConfiguredToken(lookup)
	if err != nil || got != "synthetic-token" {
		t.Fatalf("default provider resolution failed: %v", err)
	}
}

func TestCheckResolvesFileTokenWithoutExposingIt(t *testing.T) {
	agentDir := t.TempDir()
	writeTestModels(t, agentDir)
	workspace := t.TempDir()
	secretPath := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(secretPath, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "settings.json5")
	body := fmt.Sprintf(`{
		agents: { defaults: { workspace: %q } },
		secrets: { providers: { local: { source: "file", path: %q, mode: "singleValue" } } },
		channels: { discord: { token: { source: "file", provider: "local", id: "value" } } }
	}`, workspace, secretPath)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--config", configPath, "--agent-dir", agentDir, "--state-dir", stateDir, "--check"}, &stdout, &stderr); err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if strings.Contains(stdout.String(), "synthetic-token") || strings.Contains(stderr.String(), "synthetic-token") {
		t.Fatal("check exposed the token")
	}
	if entries, err := os.ReadDir(stateDir); err != nil || len(entries) != 0 {
		t.Fatalf("check changed state: %v", err)
	}
}

func TestFileTokenSettingsDoNotReadTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	body := fmt.Sprintf(`{
		secrets: { providers: { local: { source: "file", path: %q, mode: "singleValue" } } },
		channels: { discord: { token: { source: "file", provider: "local", id: "value" } } }
	}`, path)
	lookup := func(string) (string, bool) { return "", false }
	settings, err := decodeSettings([]byte(body), lookup)
	if err != nil {
		t.Fatalf("settings validation read the file: %v", err)
	}
	if _, err := settings.resolveConfiguredToken(lookup); err == nil {
		t.Fatal("missing file was accepted during token resolution")
	}
}

func TestFileTokenRejectsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input")
	if err := os.WriteFile(path, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := &fileSettings{
		Channels: &channelsSettings{Discord: &discordSettings{Token: &secretRef{file: "value", provider: "local"}}},
		Secrets:  &secretsSettings{Providers: map[string]*secretProvider{"local": {Source: "file", Path: path, Mode: "singleValue"}}},
	}
	lookup := func(string) (string, bool) { return "", false }
	if _, err := settings.resolveConfiguredToken(lookup); err != nil {
		t.Fatalf("private file: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.resolveConfiguredToken(lookup); err == nil {
		t.Fatal("accepted a file readable by others")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	settings.Secrets.Providers["local"].Path = alias
	if _, err := settings.resolveConfiguredToken(lookup); err == nil {
		t.Fatal("accepted a symlink")
	}
	settings.Secrets.Providers["local"].Path = path
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, alias); err != nil {
		t.Skip("hard links unavailable")
	}
	if _, err := settings.resolveConfiguredToken(lookup); err == nil {
		t.Fatal("accepted a hard-linked file")
	}
}

func TestFileTokenRejectsInvalidSelectorAndOversizeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte(`{"token":"synthetic-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := &fileSettings{
		Channels: &channelsSettings{Discord: &discordSettings{Token: &secretRef{file: "/token~2bad", provider: "local"}}},
		Secrets:  &secretsSettings{Providers: map[string]*secretProvider{"local": {Source: "file", Path: path}}},
	}
	lookup := func(string) (string, bool) { return "", false }
	if _, err := settings.resolveConfiguredToken(lookup); err == nil {
		t.Fatal("accepted an invalid pointer escape")
	}
	settings.Channels.Discord.Token.file = "/token"
	limit := 4
	settings.Secrets.Providers["local"].MaxBytes = &limit
	if _, err := settings.resolveConfiguredToken(lookup); err == nil || strings.Contains(err.Error(), path) {
		t.Fatalf("oversize file diagnostic = %v", err)
	}
	limit = 0
	if _, err := settings.resolveConfiguredToken(lookup); err == nil {
		t.Fatal("accepted a zero byte limit")
	}
	settings.Secrets.Providers["local"].MaxBytes = nil
	zeroTimeout := 0
	settings.Secrets.Providers["local"].TimeoutMs = &zeroTimeout
	if _, err := settings.resolveConfiguredToken(lookup); err == nil {
		t.Fatal("accepted a zero read timeout")
	}
}
