package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requireAuthWriteMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode = %o, want %o", info.Mode().Perm(), want)
	}
}

func TestSaveAPIKeyCreatesPiCompatibleAuthAndEscapesLiteral(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "private", "auth.json")
	key := "!sk-test-$-!"
	if err := SaveAPIKey(context.Background(), authPath, "deepseek", key); err != nil {
		t.Fatal(err)
	}
	requireAuthWriteMode(t, filepath.Dir(authPath), 0o700)
	requireAuthWriteMode(t, authPath, 0o600)
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"key": "$!sk-test-$$-!"`) {
		t.Fatalf("auth.json does not contain escaped literal: %s", data)
	}
	var entries map[string]map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	if entries["deepseek"]["type"] != "api_key" {
		t.Fatalf("credential type = %v", entries["deepseek"]["type"])
	}
	resolved, _, err := SelectProviderAPIKey(nil, authPath, "deepseek", "")
	if err != nil || resolved != key {
		t.Fatalf("resolved key = %q, error = %v", resolved, err)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(authPath), ".psi-auth-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files remain: %v, error=%v", temps, err)
	}
}

func TestSaveAPIKeyPreservesOtherProvidersAndUnknownFields(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	before := `{"anthropic":{"type":"api_key","key":"anthropic-old"},"deepseek":{"type":"api_key","key":"old","unknown":"keep"},"openai-codex":{"type":"oauth","access":"codex-access"},"other":{"type":"api_key","key":"other"}}`
	if err := os.WriteFile(authPath, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(authPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SaveAPIKey(context.Background(), authPath, "deepseek", "replacement"); err != nil {
		t.Fatal(err)
	}
	requireAuthWriteMode(t, authPath, 0o640)
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"anthropic", "openai-codex", "other"} {
		if len(entries[id]) == 0 {
			t.Fatalf("provider %s was not preserved", id)
		}
	}
	if string(entries["deepseek"]["unknown"]) != `"keep"` || string(entries["deepseek"]["key"]) != `"replacement"` {
		t.Fatalf("selected entry fields not preserved/updated: %s", entries["deepseek"])
	}
}

func TestSaveAPIKeyRejectsInvalidInputBeforeFilesystemEffects(t *testing.T) {
	cases := []struct {
		name       string
		providerID string
		key        string
	}{
		{name: "empty", providerID: "deepseek"},
		{name: "spaces", providerID: "deepseek", key: " \t "},
		{name: "leading newline", providerID: "deepseek", key: "\nsecret"},
		{name: "trailing newline", providerID: "deepseek", key: "secret\r"},
		{name: "invalid utf8", providerID: "deepseek", key: string([]byte{0xff})},
		{name: "unknown provider", providerID: "not-a-provider", key: "secret"},
		{name: "codex", providerID: "openai-codex", key: "secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "missing", "auth.json")
			err := SaveAPIKey(context.Background(), authPath, tc.providerID, tc.key)
			if err == nil {
				t.Fatal("invalid input was accepted")
			}
			if strings.Contains(err.Error(), tc.key) && tc.key != "" {
				t.Fatal("error included credential input")
			}
			if _, err := os.Stat(filepath.Dir(authPath)); !os.IsNotExist(err) {
				t.Fatalf("filesystem effect occurred before validation: %v", err)
			}
		})
	}
}

func TestSaveAPIKeyRejectsMalformedAuthWithoutChangingBytes(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{name: "malformed", data: `{"deepseek":`},
		{name: "null", data: `null`},
		{name: "array", data: `[]`},
		{name: "scalar", data: `1`},
		{name: "null selected entry", data: `{"deepseek":null}`},
		{name: "array selected entry", data: `{"deepseek":[]}`},
		{name: "scalar selected entry", data: `{"deepseek":"key"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(authPath, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := SaveAPIKey(context.Background(), authPath, "deepseek", "new-secret"); err == nil {
				t.Fatal("malformed document was accepted")
			}
			after, err := os.ReadFile(authPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != tc.data {
				t.Fatalf("auth.json changed: %q", after)
			}
		})
	}
}

func TestSaveAPIKeyRejectsOversizeInputAndOutput(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		data := []byte(`{"unrelated":"` + strings.Repeat("x", maxAuthJSONSize) + `"}`)
		if err := os.WriteFile(authPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SaveAPIKey(context.Background(), authPath, "deepseek", "new-secret"); err == nil {
			t.Fatal("oversize auth.json was accepted")
		}
		after, err := os.ReadFile(authPath)
		if err != nil || string(after) != string(data) {
			t.Fatalf("oversize auth.json changed: error=%v", err)
		}
	})
	t.Run("encoded output", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		data := []byte(`{"unrelated":"` + strings.Repeat("x", maxAuthJSONSize-40) + `"}`)
		if len(data) > maxAuthJSONSize {
			t.Fatal("test input exceeds reader limit")
		}
		if err := os.WriteFile(authPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SaveAPIKey(context.Background(), authPath, "deepseek", "new-secret"); err == nil {
			t.Fatal("oversize encoded auth.json was accepted")
		}
		after, err := os.ReadFile(authPath)
		if err != nil || string(after) != string(data) {
			t.Fatalf("auth.json changed: error=%v", err)
		}
	})
}

func TestSaveAPIKeyRejectsOAuthAndUnsupportedTypesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "oauth", data: `{"deepseek":{"type":"oauth","access":"do-not-overwrite"}}`},
		{name: "oauth token", data: `{"deepseek":{"type":"oauth_token","access":"do-not-overwrite"}}`},
		{name: "unsupported", data: `{"deepseek":{"type":"custom","key":"keep"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(authPath, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			err := SaveAPIKey(context.Background(), authPath, "deepseek", "new-secret")
			if err == nil {
				t.Fatal("credential type was overwritten")
			}
			if tc.name == "oauth" && err.Error() != "refusing to overwrite the OAuth credential for deepseek" {
				t.Fatalf("OAuth error = %v", err)
			}
			after, readErr := os.ReadFile(authPath)
			if readErr != nil || string(after) != tc.data {
				t.Fatalf("auth.json changed: error=%v", readErr)
			}
		})
	}
}

func TestSaveAPIKeyRefusesSymlinkAndPreservesTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink auth path test is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	authPath := filepath.Join(dir, "auth.json")
	original := `{"deepseek":{"type":"api_key","key":"original"}}`
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := SaveAPIKey(context.Background(), authPath, "deepseek", "new-secret"); err == nil {
		t.Fatal("symlinked auth.json was accepted")
	}
	after, err := os.ReadFile(target)
	if err != nil || string(after) != original {
		t.Fatalf("symlink target changed: error=%v", err)
	}
}

func TestSaveAPIKeyCancellationAndLeaseRefusalLeaveNoChanges(t *testing.T) {
	t.Run("pre-cancelled missing file", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "missing", "auth.json")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := SaveAPIKey(ctx, authPath, "deepseek", "secret"); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
		if _, err := os.Stat(filepath.Dir(authPath)); !os.IsNotExist(err) {
			t.Fatalf("directory created after cancellation: %v", err)
		}
	})
	t.Run("cancelled lease wait", func(t *testing.T) {
		dir := t.TempDir()
		authPath := filepath.Join(dir, "auth.json")
		original := `{"deepseek":{"type":"api_key","key":"old"}}`
		if err := os.WriteFile(authPath, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		lockPath := authPath + ".lock"
		if err := os.Mkdir(lockPath, 0o700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := SaveAPIKey(ctx, authPath, "deepseek", "secret"); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
		if _, err := os.Stat(lockPath); err != nil {
			t.Fatalf("holder lock changed: %v", err)
		}
		after, err := os.ReadFile(authPath)
		if err != nil || string(after) != original {
			t.Fatalf("auth.json changed: error=%v", err)
		}
		temps, _ := filepath.Glob(filepath.Join(dir, ".psi-auth-*"))
		if len(temps) != 0 {
			t.Fatalf("temporary files remain: %v", temps)
		}
	})
}

func TestWriteAuthDocumentLostLeaseLeavesOriginalAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	original := `{"deepseek":{"type":"api_key","key":"old"}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAuthDocument(path, map[string]json.RawMessage{"deepseek": json.RawMessage(`{"type":"api_key","key":"new"}`)}, 0o600, func() bool { return false }); err == nil {
		t.Fatal("lost lease did not refuse commit")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != original {
		t.Fatalf("auth.json changed after lease loss: error=%v", err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".psi-auth-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files remain: %v, error=%v", temps, err)
	}
}

func TestAuthEntriesAndSelectionAcceptBOM(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte("\xef\xbb\xbf{\"deepseek\":{\"type\":\"api_key\",\"key\":\"stored\"}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := AuthEntries(authPath)
	if err != nil || len(entries) != 1 {
		t.Fatalf("AuthEntries = %v, error = %v", entries, err)
	}
	key, _, err := SelectProviderAPIKey(nil, authPath, "deepseek", "")
	if err != nil || key != "stored" {
		t.Fatalf("selected key = %q, error = %v", key, err)
	}
}

func TestAuthEntriesRejectsNonRegularFilesAndFollowsRegularSymlinks(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		if err := os.Mkdir(authPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthEntries(authPath); err == nil || !strings.Contains(err.Error(), "auth.json must be a regular file") {
			t.Fatalf("directory auth path error = %v", err)
		}
	})

	if runtime.GOOS == "windows" {
		t.Skip("regular-file symlink behavior is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(target, []byte(`{"deepseek":{"type":"api_key","key":"stored"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	entries, err := AuthEntries(authPath)
	if err != nil || string(entries["deepseek"]) != `{"type":"api_key","key":"stored"}` {
		t.Fatalf("regular-file symlink entries = %v, error = %v", entries, err)
	}
}

func TestInvalidAuthDocumentRejectsCredentialFallbackAndLogoutListing(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{name: "null", data: []byte("null")},
		{name: "oversize", data: []byte(`{"unrelated":"` + strings.Repeat("x", maxAuthJSONSize) + `"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(authPath, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}

			key, source, err := SelectProviderAPIKey(map[string]string{"DEEPSEEK_API_KEY": "environment-key"}, authPath, "deepseek", "")
			if err == nil || key != "" || source != "" {
				t.Fatalf("invalid auth.json fell back to credentials: key=%q source=%q err=%v", key, source, err)
			}
			providers, err := StoredAPIKeyProviders(authPath)
			if err == nil || providers != nil {
				t.Fatalf("invalid auth.json was reported as an empty logout list: providers=%v err=%v", providers, err)
			}
		})
	}
}
