package provider

import (
	"bytes"
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
	if err := SaveAPIKey(context.Background(), authPath, "deepseek", []byte(key)); err != nil {
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
	if err := SaveAPIKey(context.Background(), authPath, "deepseek", []byte("replacement")); err != nil {
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
			err := SaveAPIKey(context.Background(), authPath, tc.providerID, []byte(tc.key))
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
			if err := SaveAPIKey(context.Background(), authPath, "deepseek", []byte("new-secret")); err == nil {
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
		if err := SaveAPIKey(context.Background(), authPath, "deepseek", []byte("new-secret")); err == nil {
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
		if err := SaveAPIKey(context.Background(), authPath, "deepseek", []byte("new-secret")); err == nil {
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
			err := SaveAPIKey(context.Background(), authPath, "deepseek", []byte("new-secret"))
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

func TestSaveAPIKeyPreservesSymlinkAndTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink auth path test is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	authPath := filepath.Join(dir, "auth.json")
	original := `{"deepseek":{"type":"api_key","key":"original","unknown":{"keep":true}},"custom":{"type":"api_key","key":"keep"}}`
	if err := os.WriteFile(target, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(target), authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := SaveAPIKey(context.Background(), authPath, "deepseek", []byte("new-secret")); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(authPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("auth.json link was replaced")
	}
	link, err := os.Readlink(authPath)
	if err != nil || link != filepath.Base(target) {
		t.Fatal("auth.json link text changed")
	}
	requireAuthWriteMode(t, target, 0o640)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	if string(entries["deepseek"]["key"]) != `"new-secret"` {
		t.Fatalf("linked DeepSeek key was not updated: %s", entries["deepseek"])
	}
	var unknown map[string]bool
	if err := json.Unmarshal(entries["deepseek"]["unknown"], &unknown); err != nil || !unknown["keep"] {
		t.Fatalf("linked DeepSeek unknown field changed: %s", entries["deepseek"])
	}
	if string(entries["custom"]["key"]) != `"keep"` {
		t.Fatalf("linked unrelated entry changed: %s", entries["custom"])
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".psi-auth-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files remain: %v, error=%v", temps, err)
	}
}

func TestSaveAPIKeyResolvesChainedAuthLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink auth path test is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"deepseek":{"type":"api_key","key":"original"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "first.json")
	if err := os.Symlink(filepath.Base(target), first); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	authPath := filepath.Join(dir, "auth.json")
	if err := os.Symlink(filepath.Base(first), authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := SaveAPIKey(context.Background(), authPath, "deepseek", []byte("new-secret")); err != nil {
		t.Fatal(err)
	}
	for _, linkPath := range []string{authPath, first} {
		info, err := os.Lstat(linkPath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link %s was replaced", filepath.Base(linkPath))
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(data), `"key": "new-secret"`) {
		t.Fatalf("chained target was not updated: %s, error=%v", data, err)
	}
}

func TestSaveAPIKeyPreCancelledContextHasNoFilesystemEffects(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "missing", "auth.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SaveAPIKey(ctx, authPath, "deepseek", []byte("secret")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if _, err := os.Stat(filepath.Dir(authPath)); !os.IsNotExist(err) {
		t.Fatalf("directory created after cancellation: %v", err)
	}
}

func TestReadAuthDocumentRefusesReplacedFile(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	const document = `{"deepseek":{"type":"api_key","key":"synthetic-key"}}`
	if err := os.WriteFile(authPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := resolveAuthTarget(authPath)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the original alive so a reused file ID cannot hide replacement.
	if err := os.Rename(authPath, filepath.Join(dir, "original.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, _, err := readAuthDocument(target)
	defer releaseRawEntries(entries)
	if err == nil || !strings.Contains(err.Error(), "changed while being read") {
		t.Fatalf("replaced file error = %v", err)
	}
}

func TestWriteAuthDocumentLostLeaseLeavesOriginalAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	original := `{"deepseek":{"type":"api_key","key":"old"}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := resolveAuthTarget(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAuthDocument(target, map[string]json.RawMessage{"deepseek": json.RawMessage(`{"type":"api_key","key":"new"}`)}, 0o600, func() error { return errors.New("lease lost") }); err == nil {
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

func TestWriteAuthDocumentRefusesRetargetedAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink auth path test is POSIX-only")
	}
	dir := t.TempDir()
	targetA := filepath.Join(dir, "target-a.json")
	targetB := filepath.Join(dir, "target-b.json")
	authPath := filepath.Join(dir, "auth.json")
	for _, target := range []string{targetA, targetB} {
		if err := os.WriteFile(target, []byte(`{"deepseek":{"type":"api_key","key":"old"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Base(targetA), authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	target, err := resolveAuthTarget(authPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	valid := func() error {
		calls++
		if calls == 2 {
			if err := os.Remove(authPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(targetB), authPath); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	err = writeAuthDocument(target, map[string]json.RawMessage{"deepseek": json.RawMessage(`{"type":"api_key","key":"new"}`)}, 0o600, valid)
	if err == nil || !strings.Contains(err.Error(), "target changed") {
		t.Fatalf("retargeted auth error = %v", err)
	}
	for _, path := range []string{targetA, targetB} {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), `"key":"old"`) {
			t.Fatalf("failed commit changed %s: %s, error=%v", filepath.Base(path), data, err)
		}
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".psi-auth-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files remain: %v, error=%v", temps, err)
	}
}

func TestWriteAuthDocumentRefusesReplacedAbsentParent(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "agent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(parent, "auth.json")
	target, err := resolveAuthTarget(authPath)
	if err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "agent-old")
	calls := 0
	valid := func() error {
		calls++
		if calls == 1 {
			if err := os.Rename(parent, older); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	err = writeAuthDocument(target, map[string]json.RawMessage{"deepseek": json.RawMessage(`{"type":"api_key","key":"new"}`)}, 0o600, valid)
	if err == nil || !strings.Contains(err.Error(), "target changed") {
		t.Fatalf("replaced parent error = %v", err)
	}
	for _, directory := range []string{parent, older} {
		if _, err := os.Stat(filepath.Join(directory, "auth.json")); !os.IsNotExist(err) {
			t.Fatalf("rejected write created auth.json in %s: %v", filepath.Base(directory), err)
		}
		temps, err := filepath.Glob(filepath.Join(directory, ".psi-auth-*"))
		if err != nil || len(temps) != 0 {
			t.Fatalf("temporary files remain in %s: %v, error=%v", filepath.Base(directory), temps, err)
		}
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

func TestDanglingAuthSymlinkFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dangling symlink behavior is POSIX-only")
	}
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.Symlink(filepath.Join(dir, "missing.json"), authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	entries, err := AuthEntries(authPath)
	if err == nil || entries != nil {
		t.Fatalf("dangling auth symlink treated as missing: entries=%v err=%v", entries, err)
	}
	key, source, err := SelectProviderAPIKey(map[string]string{"DEEPSEEK_API_KEY": "fallback-key"}, authPath, "deepseek", "")
	if err == nil || key != "" || source != "" {
		t.Fatalf("dangling auth symlink allowed fallback: key=%q source=%q err=%v", key, source, err)
	}
	providers, err := StoredAPIKeyProviders(authPath)
	if err == nil || providers != nil {
		t.Fatalf("dangling auth symlink was reported as an empty logout list: providers=%v err=%v", providers, err)
	}
}

func TestInvalidAuthDocumentRejectsCredentialFallbackAndLogoutListing(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{name: "null", data: []byte("null")},
		{name: "invalid UTF-8", data: []byte("{\"deepseek\":{\"type\":\"api_key\",\"key\":\"corrupt-\xff-key\"}}")},
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
				t.Fatalf("invalid auth.json resolved a credential: key=%q source=%q err=%v", key, source, err)
			}
			providers, err := StoredAPIKeyProviders(authPath)
			if err == nil || providers != nil {
				t.Fatalf("invalid auth.json was reported as an empty logout list: providers=%v err=%v", providers, err)
			}
		})
	}
}

func TestSaveAPIKeyLeavesBorrowedInputUnchanged(t *testing.T) {
	t.Run("success and trimming", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		backing := bytes.Repeat([]byte{0x5a}, 64)
		key := backing[8:15]
		copy(key, " !sk-$ ")
		before := append([]byte(nil), backing...)
		if err := SaveAPIKey(context.Background(), authPath, "deepseek", key); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(backing, before) {
			t.Fatalf("borrowed key or sentinels changed: %q", backing)
		}
		data, err := os.ReadFile(authPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"key": "$!sk-$$"`) {
			t.Fatalf("trimmed literal was not encoded: %s", data)
		}
	})

	t.Run("validation rejection", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "missing", "auth.json")
		backing := bytes.Repeat([]byte{0x33}, 32)
		key := backing[4:11]
		copy(key, "bad\rkey")
		before := append([]byte(nil), backing...)
		if err := SaveAPIKey(context.Background(), authPath, "deepseek", key); err == nil {
			t.Fatal("newline key was accepted")
		}
		if !bytes.Equal(backing, before) {
			t.Fatalf("rejected borrowed key changed: %q", backing)
		}
	})

	t.Run("filesystem failure", func(t *testing.T) {
		dir := t.TempDir()
		parentFile := filepath.Join(dir, "not-a-directory")
		if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		backing := bytes.Repeat([]byte{0x44}, 32)
		key := backing[3:10]
		copy(key, "sk-test")
		before := append([]byte(nil), backing...)
		if err := SaveAPIKey(context.Background(), filepath.Join(parentFile, "auth.json"), "deepseek", key); err == nil {
			t.Fatal("invalid parent was accepted")
		}
		if !bytes.Equal(backing, before) {
			t.Fatalf("borrowed key changed after filesystem failure: %q", backing)
		}
	})
}

func TestStoredAPIKeyProvidersInspectsKeyWithoutDecoding(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
		err  bool
	}{
		{name: "absent", body: `{"deepseek":{"type":"api_key"}}`},
		{name: "null", body: `{"deepseek":{"type":"api_key","key":null}}`},
		{name: "empty", body: `{"deepseek":{"type":"api_key","key":""}}`},
		{name: "escaped", body: `{"deepseek":{"type":"api_key","key":"\\u0000"}}`, want: []string{"deepseek"}},
		{name: "spaces", body: `{"deepseek":{"type":"api_key","key":" "}}`, want: []string{"deepseek"}},
		{name: "number", body: `{"deepseek":{"type":"api_key","key":7}}`, err: true},
		{name: "boolean", body: `{"deepseek":{"type":"api_key","key":true}}`, err: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(authPath, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := StoredAPIKeyProviders(authPath)
			if tc.err {
				if err == nil {
					t.Fatalf("malformed key type was accepted: %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("providers = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("providers = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
