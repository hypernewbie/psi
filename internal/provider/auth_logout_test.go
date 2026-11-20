package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// failOnReadBody proves that logout closes a revocation response without
// reading its body.
type failOnReadBody struct {
	closed bool
	read   bool
}

func (b *failOnReadBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("synthetic-response-canary")
}

func (b *failOnReadBody) Close() error {
	b.closed = true
	return nil
}

func revocationResponse(status int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Body: body, Header: make(http.Header)}
}

func readRevokePayload(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	if req.Body != nil {
		defer req.Body.Close() // The test transport owns the request body.
	}
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	if req.URL.String() != "https://auth.openai.com/oauth/revoke" {
		t.Fatalf("unexpected revocation URL: %s", req.URL)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if req.Body == nil || req.ContentLength <= 0 {
		t.Fatalf("ContentLength = %d", req.ContentLength)
	}
	if req.GetBody != nil {
		t.Fatal("revocation body must not be replayable")
	}
	deadline, ok := req.Context().Deadline()
	if !ok || time.Until(deadline) > 10*time.Second {
		t.Fatalf("request deadline = %v, want a limit within 10s", deadline)
	}
	var payload map[string]any
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return payload
}

func TestLogoutCredentialRevocation(t *testing.T) {
	t.Run("Codex refresh uses built-in client ID", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, authPath, `{"openai-codex":{"type":"oauth","access":"synthetic-access","refresh":" synthetic\u002drefresh ","expires":1}}`)
		body := &failOnReadBody{}
		calls := 0
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			payload := readRevokePayload(t, req)
			if payload["token"] != " synthetic-refresh " || payload["token_type_hint"] != "refresh_token" || payload["client_id"] != codexClientID {
				t.Fatalf("Codex payload = %v", payload)
			}
			return revocationResponse(http.StatusOK, body), nil
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
		if err != nil || !result.Removed || result.Revocation != RevocationAcceptedRefresh {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		if calls != 1 || !body.closed || body.read {
			t.Fatalf("calls = %d, response read = %t, closed = %t", calls, body.read, body.closed)
		}
	})

	t.Run("OpenAI refresh uses stored client ID", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, authPath, `{"openai":{"type":"oauth","access":"synthetic-access","refresh":"synthetic-refresh","expires":1,"clientId":"synthetic-client"}}`)
		calls := 0
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			payload := readRevokePayload(t, req)
			if payload["token"] != "synthetic-refresh" || payload["token_type_hint"] != "refresh_token" || payload["client_id"] != "synthetic-client" {
				t.Fatalf("OpenAI refresh payload = %v", payload)
			}
			return revocationResponse(http.StatusNoContent, io.NopCloser(strings.NewReader(""))), nil
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai", Kind: CredentialOAuth}, client)
		if err != nil || !result.Removed || result.Revocation != RevocationAcceptedRefresh || calls != 1 {
			t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls)
		}
	})

	t.Run("OpenAI access without client ID", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, authPath, `{"openai":{"type":"oauth","access":"synthetic-access","refresh":"synthetic-refresh","expires":1}}`)
		calls := 0
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			payload := readRevokePayload(t, req)
			if payload["token"] != "synthetic-access" || payload["token_type_hint"] != "access_token" {
				t.Fatalf("OpenAI access payload = %v", payload)
			}
			if _, ok := payload["client_id"]; ok {
				t.Fatalf("access request carried client_id: %v", payload)
			}
			return revocationResponse(http.StatusOK, io.NopCloser(strings.NewReader(""))), nil
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai", Kind: CredentialOAuth}, client)
		if err != nil || !result.Removed || result.Revocation != RevocationAcceptedAccess || calls != 1 {
			t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls)
		}
	})

	for _, tc := range []struct {
		name    string
		respond func(*http.Request) (*http.Response, error)
	}{
		{name: "redirect", respond: func(req *http.Request) (*http.Response, error) {
			response := revocationResponse(http.StatusFound, io.NopCloser(strings.NewReader("")))
			response.Header.Set("Location", "https://example.invalid/redirected")
			return response, nil
		}},
		{name: "rate limit", respond: func(req *http.Request) (*http.Response, error) {
			response := revocationResponse(http.StatusTooManyRequests, io.NopCloser(strings.NewReader("")))
			response.Header.Set("Retry-After", "1")
			return response, nil
		}},
		{name: "deadline", respond: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("synthetic-transport-canary: %w", context.DeadlineExceeded)
		}},
	} {
		t.Run("unconfirmed "+tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			writeAuthDoc(t, authPath, `{"openai-codex":{"type":"oauth","refresh":"synthetic-refresh-canary"}}`)
			calls := 0
			client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				readRevokePayload(t, req)
				return tc.respond(req)
			})}
			result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
			if err != nil || !result.Removed || result.Revocation != RevocationUnconfirmed {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want exactly one", calls)
			}
			data, readErr := os.ReadFile(authPath)
			if readErr != nil || strings.Contains(string(data), "openai-codex") {
				t.Fatalf("local entry survived a failed revocation: %s error=%v", data, readErr)
			}
		})
	}

	t.Run("API key sends no request", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, authPath, `{"deepseek":{"type":"api_key","key":"synthetic-key"}}`)
		calls := 0
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("API-key logout must not send a request")
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "deepseek", Kind: CredentialAPIKey}, client)
		if err != nil || !result.Removed || result.Revocation != RevocationNotAttempted || calls != 0 {
			t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls)
		}
	})

	t.Run("no usable token sends no request", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, authPath, `{"openai":{"type":"oauth","refresh":7,"access":null,"clientId":"synthetic-client"}}`)
		calls := 0
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("missing token must not send a request")
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai", Kind: CredentialOAuth}, client)
		if err != nil || !result.Removed || result.Revocation != RevocationNoUsableToken || calls != 0 {
			t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls)
		}
	})

	t.Run("stale picker kind sends no request", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, authPath, `{"openai":{"type":"oauth","refresh":"synthetic-refresh"}}`)
		calls := 0
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("stale picker kind must not send a request")
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai", Kind: CredentialAPIKey}, client)
		if err == nil || result.Removed || result.Revocation != RevocationNotAttempted || calls != 0 {
			t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls)
		}
	})
}

func TestLogoutCredentialCommitGuards(t *testing.T) {
	t.Run("distinct leases stay held through the request", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink lease identity test is POSIX-only")
		}
		agentDir, targetDir := t.TempDir(), t.TempDir()
		authPath := filepath.Join(agentDir, "auth.json")
		targetPath := filepath.Join(targetDir, "auth.json")
		writeAuthDoc(t, targetPath, `{"deepseek":{"type":"api_key","key":"keep"},"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`)
		if err := os.Symlink(targetPath, authPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			readRevokePayload(t, req)
			for _, lock := range []string{authPath + ".lock", targetPath + ".lock"} {
				if err := os.Mkdir(lock, 0o700); !os.IsExist(err) {
					t.Fatalf("logout did not hold lease %s: %v", lock, err)
				}
			}
			return revocationResponse(http.StatusOK, io.NopCloser(strings.NewReader(""))), nil
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
		if err != nil || !result.Removed || result.Revocation != RevocationAcceptedRefresh {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	})

	t.Run("unrelated entry change refuses the write", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		const original = `{"deepseek":{"type":"api_key","key":"original"},"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`
		const changed = `{"deepseek":{"type":"api_key","key":"concurrent"},"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`
		writeAuthDoc(t, authPath, original)
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			readRevokePayload(t, req)
			if err := os.WriteFile(authPath, []byte(changed), 0o600); err != nil {
				t.Fatal(err)
			}
			return revocationResponse(http.StatusOK, io.NopCloser(strings.NewReader(""))), nil
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
		if err == nil || result.Removed || result.Revocation != RevocationAcceptedRefresh {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		data, readErr := os.ReadFile(authPath)
		if readErr != nil || string(data) != changed {
			t.Fatalf("concurrent entry change did not survive: %s error=%v", data, readErr)
		}
	})

	t.Run("in-place change during staging survives", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		const original = `{"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`
		const changed = `{"openai-codex":{"type":"oauth","refresh":"rotated-synthetic"}}`
		writeAuthDoc(t, authPath, original)
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			readRevokePayload(t, req)
			return revocationResponse(http.StatusOK, http.NoBody), nil
		})}
		hooks := authFileHooks{Chmod: func(tempPath string, mode os.FileMode) error {
			if err := os.WriteFile(authPath, []byte(changed), 0o600); err != nil {
				t.Fatal(err)
			}
			return os.Chmod(tempPath, mode)
		}}
		result, err := logoutCredential(t.Context(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client, hooks)
		if err == nil || !strings.Contains(err.Error(), "changed during logout") || result.Removed || result.Revocation != RevocationAcceptedRefresh {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		data, readErr := os.ReadFile(authPath)
		if readErr != nil || string(data) != changed {
			t.Fatalf("logout overwrote a change made during staging: error=%v", readErr)
		}
	})

	t.Run("retargeted symlink refuses the write", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink retarget test is POSIX-only")
		}
		dir := t.TempDir()
		authPath := filepath.Join(dir, "auth.json")
		targetA := filepath.Join(dir, "target-a.json")
		targetB := filepath.Join(dir, "target-b.json")
		const document = `{"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`
		for _, path := range []string{targetA, targetB} {
			writeAuthDoc(t, path, document)
		}
		if err := os.Symlink(filepath.Base(targetA), authPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			readRevokePayload(t, req)
			if err := os.Remove(authPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(targetB), authPath); err != nil {
				t.Fatal(err)
			}
			return revocationResponse(http.StatusOK, io.NopCloser(strings.NewReader(""))), nil
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
		if err == nil || result.Removed || result.Revocation != RevocationAcceptedRefresh {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		for _, path := range []string{targetA, targetB} {
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != document {
				t.Fatalf("retarget refusal changed %s: %s error=%v", filepath.Base(path), data, readErr)
			}
		}
		link, linkErr := os.Readlink(authPath)
		if linkErr != nil || link != filepath.Base(targetB) {
			t.Fatalf("logout changed the retargeted link: %q error=%v", link, linkErr)
		}
	})

	t.Run("permission change refuses the write", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permission bits are not portable to Windows")
		}
		authPath := filepath.Join(t.TempDir(), "auth.json")
		const document = `{"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`
		writeAuthDoc(t, authPath, document)
		if err := os.Chmod(authPath, 0o600); err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			readRevokePayload(t, req)
			if err := os.Chmod(authPath, 0o644); err != nil {
				t.Fatal(err)
			}
			return revocationResponse(http.StatusOK, io.NopCloser(strings.NewReader(""))), nil
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
		if err == nil || result.Removed || result.Revocation != RevocationAcceptedRefresh {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		data, readErr := os.ReadFile(authPath)
		if readErr != nil || string(data) != document {
			t.Fatalf("permission refusal changed auth.json: %s error=%v", data, readErr)
		}
		requireAuthWriteMode(t, authPath, 0o644)
	})

	t.Run("outer cancellation refuses the write", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		const document = `{"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`
		writeAuthDoc(t, authPath, document)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			readRevokePayload(t, req)
			cancel()
			return revocationResponse(http.StatusOK, io.NopCloser(strings.NewReader(""))), nil
		})}
		result, err := LogoutCredential(ctx, authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
		if !errors.Is(err, context.Canceled) || result.Removed {
			t.Fatalf("result = %+v, error = %v, want caller cancellation", result, err)
		}
		data, readErr := os.ReadFile(authPath)
		if readErr != nil || string(data) != document {
			t.Fatalf("cancelled logout changed auth.json: %s error=%v", data, readErr)
		}
	})

	t.Run("replaced lease refuses the write", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("replacement under a live holder is impossible on Windows")
		}
		authPath := filepath.Join(t.TempDir(), "auth.json")
		const document = `{"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`
		writeAuthDoc(t, authPath, document)
		lock := authPath + ".lock"
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			readRevokePayload(t, req)
			if err := os.RemoveAll(lock); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(lock, 0o700); err != nil {
				t.Fatal(err)
			}
			return revocationResponse(http.StatusOK, io.NopCloser(strings.NewReader(""))), nil
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
		if err == nil || result.Removed {
			t.Fatalf("result = %+v, error = %v, want refused local write", result, err)
		}
		if info, statErr := os.Lstat(lock); statErr != nil || !info.IsDir() {
			t.Fatalf("logout removed or changed the replacement lease: %v", statErr)
		}
		data, readErr := os.ReadFile(authPath)
		if readErr != nil || string(data) != document {
			t.Fatalf("lost lease changed auth.json: %s error=%v", data, readErr)
		}
	})

	t.Run("symlinked parent shares one physical lease", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinked parent test is POSIX-only")
		}
		realDir, linkRoot := t.TempDir(), t.TempDir()
		linkDir := filepath.Join(linkRoot, "agent")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		authPath := filepath.Join(linkDir, "auth.json")
		targetPath := filepath.Join(realDir, "auth.json")
		writeAuthDoc(t, targetPath, `{"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`)
		client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
			readRevokePayload(t, req)
			logicalInfo, logicalErr := os.Lstat(authPath + ".lock")
			resolvedInfo, resolvedErr := os.Lstat(targetPath + ".lock")
			if logicalErr != nil || resolvedErr != nil || !os.SameFile(logicalInfo, resolvedInfo) {
				t.Fatal("logout did not use one physical lease for the symlinked parent")
			}
			return revocationResponse(http.StatusOK, io.NopCloser(strings.NewReader(""))), nil
		})}
		result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
		if err != nil || !result.Removed {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	})
}

func TestLogoutLockPreservesFilesystemError(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), apiKeyAuthLockWait)
	defer cancel()
	path := filepath.Join(t.TempDir(), "missing", "auth.json.lock")
	unlock, _, err := acquireLogoutLock(t.Context(), ctx, path)
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock error = %v, want missing directory rather than contention", err)
	}
}

func TestLogoutCredentialBusyResolvedLease(t *testing.T) {
	if os.Getenv("PSI_TEST_LOGOUT_BUSY_RESOLVED") != "1" {
		t.Skip("set PSI_TEST_LOGOUT_BUSY_RESOLVED=1 to run the 5-second busy-lease fixture")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink lease test is POSIX-only")
	}
	agentDir, targetDir := t.TempDir(), t.TempDir()
	authPath := filepath.Join(agentDir, "auth.json")
	targetPath := filepath.Join(targetDir, "auth.json")
	const document = `{"openai-codex":{"type":"oauth","refresh":"synthetic-refresh"}}`
	writeAuthDoc(t, targetPath, document)
	if err := os.Symlink(targetPath, authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	unlockHolder, holderValid, err := acquireAuthLock(context.Background(), targetPath+".lock", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockHolder()
	calls := 0
	client := &http.Client{Transport: tokenTransportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("busy logout must not send a request")
	})}
	result, err := LogoutCredential(context.Background(), authPath, StoredCredential{ProviderID: "openai-codex", Kind: CredentialOAuth}, client)
	if err == nil || !strings.Contains(err.Error(), errAuthLogoutBusy.Error()) {
		t.Fatalf("busy logout result = %+v, error = %v", result, err)
	}
	if result.Removed || calls != 0 {
		t.Fatalf("busy logout removed=%t calls=%d", result.Removed, calls)
	}
	data, readErr := os.ReadFile(targetPath)
	if readErr != nil || string(data) != document {
		t.Fatalf("busy logout changed auth.json: %s error=%v", data, readErr)
	}
	if !holderValid() {
		t.Fatal("busy logout compromised the resolved lease holder")
	}
	if _, statErr := os.Lstat(authPath + ".lock"); !os.IsNotExist(statErr) {
		t.Fatalf("busy logout leaked the logical lease: %v", statErr)
	}
}
