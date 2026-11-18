package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexOAuthRefreshPreservesOtherCredentialsAndUnknownFields(t *testing.T) {
	path := writeCodexAuth(t, true)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.ParseForm() != nil || r.Form.Get("refresh_token") != "dummy-refresh" || r.Form.Get("client_id") != codexClientID || r.Form.Get("grant_type") != "refresh_token" {
			t.Error("incorrect refresh protocol")
		}
		// A writer outside the lock protocol modifies another provider. Refresh
		// must reread and merge, not overwrite a startup snapshot.
		data, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
			return
		}
		var all map[string]json.RawMessage
		json.Unmarshal(data, &all)
		all["added-while-refreshing"] = json.RawMessage(`{"type":"api_key","key":"dummy-concurrent-key"}`)
		data, _ = json.Marshal(all)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Error(err)
			return
		}
		io.WriteString(w, `{"access_token":"dummy-new-access","refresh_token":"dummy-new-refresh","expires_in":3600}`)
	}))
	defer server.Close()
	a := &CodexAuth{Path: path, HTTP: server.Client(), TokenURL: server.URL}
	if err := a.Check(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("startup validation performed a refresh")
	}
	access, account, err := a.Get(context.Background())
	if err != nil || access != "dummy-new-access" || account != "test-account" {
		t.Fatal("refresh did not return the synthetic credential")
	}
	data, _ := os.ReadFile(path)
	var all map[string]json.RawMessage
	if json.Unmarshal(data, &all) != nil {
		t.Fatal("auth.json was corrupted")
	}
	if !bytes.Contains(all["unrelated"], []byte("dummy-other-key")) || !bytes.Contains(all["added-while-refreshing"], []byte("dummy-concurrent-key")) || !bytes.Contains(all["openai-codex"], []byte(`"unknown"`)) {
		t.Fatal("refresh discarded unrelated or unknown credential data")
	}
	var updated codexCredential
	json.Unmarshal(all["openai-codex"], &updated)
	if updated.Refresh != "dummy-new-refresh" || updated.Expires < time.Now().Add(50*time.Minute).UnixMilli() {
		t.Fatal("refresh rotation or expiry was not persisted")
	}
	// Windows has no Unix mode bits; a private file stats as 0666.
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0600 {
			t.Fatal("refreshed auth is not private")
		}
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("auth lock leaked")
	}
	if _, _, err := a.Get(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatal("fresh credentials were refreshed again")
	}
}

func TestCodexConcurrentRefreshUsesPiCompatibleDirectoryLock(t *testing.T) {
	path := writeCodexAuth(t, true)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if st, err := os.Stat(path + ".lock"); err != nil || !st.IsDir() {
			t.Error("not using Pi's directory lock")
		}
		time.Sleep(40 * time.Millisecond)
		io.WriteString(w, `{"access_token":"dummy-new-access","refresh_token":"dummy-new-refresh","expires_in":3600}`)
	}))
	defer server.Close()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := &CodexAuth{Path: path, HTTP: server.Client(), TokenURL: server.URL}
			if _, _, err := a.Get(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("the same refresh token was used concurrently")
	}
}

func TestCodexRefreshFailureAndCancellationDoNotRewriteAuth(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		cancelHeaders bool
		cancelBody    bool
	}{
		{name: "HTTP failure", status: http.StatusBadRequest, body: `{"error":"dummy-refresh must not appear in diagnostics"}`},
		{name: "cancel before headers", cancelHeaders: true},
		{name: "cancel partial body", status: http.StatusOK, body: `{"access_token":"dummy-refresh`, cancelBody: true},
		{name: "malformed body", status: http.StatusOK, body: `{"access_token":"dummy-refresh`},
		{name: "oversized body", status: http.StatusOK, body: strings.Repeat(" ", 64*1024) + `{"access_token":"dummy-refresh","expires_in":3600}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeCodexAuth(t, true)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &http.Client{Transport: tokenTransportFunc(func(r *http.Request) (*http.Response, error) {
				if tc.cancelHeaders {
					cancel()
					return nil, r.Context().Err()
				}
				body := io.NopCloser(strings.NewReader(tc.body))
				if tc.cancelBody {
					body = cancelAtEOF{Reader: strings.NewReader(tc.body), cancel: cancel}
				}
				return &http.Response{StatusCode: tc.status, Body: body, Header: make(http.Header)}, nil
			})}
			a := &CodexAuth{Path: path, HTTP: client, TokenURL: "http://synthetic-token-endpoint.invalid"}
			_, _, err = a.Get(ctx)
			if err == nil || strings.Contains(err.Error(), "dummy-refresh") {
				t.Fatal("refresh failure was accepted or leaked a token")
			}
			if (tc.cancelHeaders || tc.cancelBody) && !errors.Is(err, context.Canceled) {
				t.Fatalf("refresh lost cancellation: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed/cancelled refresh rewrote credentials")
			}
			if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
				t.Fatal("failed refresh leaked a lock")
			}
			leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".psi-auth-*"))
			if err != nil || len(leftovers) > 0 {
				t.Fatal("refresh left a credential temporary file")
			}
		})
	}
}

type tokenTransportFunc func(*http.Request) (*http.Response, error)

func (f tokenTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cancelAtEOF struct {
	io.Reader
	cancel context.CancelFunc
}

func (r cancelAtEOF) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.cancel()
	}
	return n, err
}
func (r cancelAtEOF) Close() error { return nil }

func TestCodexCancellationDoesNotDiscardReturnedRotatedTokens(t *testing.T) {
	path := writeCodexAuth(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: tokenTransportFunc(func(r *http.Request) (*http.Response, error) {
		body := cancelAtEOF{Reader: strings.NewReader(`{"access_token":"dummy-rotated-access","refresh_token":"dummy-rotated-refresh","expires_in":3600}`), cancel: cancel}
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})}
	a := &CodexAuth{Path: path, HTTP: client, TokenURL: "http://synthetic-token-endpoint.invalid"}
	if _, _, err := a.Get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled caller was not stopped")
	}
	_, entry, err := a.loadLocked()
	if err != nil || entry.Refresh != "dummy-rotated-refresh" || entry.Access != "dummy-rotated-access" {
		t.Fatal("cancellation discarded a successfully returned rotated credential")
	}
}

func TestCodexAccountExtractionUsesURLSafeJWT(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"test-jwt-account"}}`))
	account, err := codexAccount(codexCredential{Access: "test-header." + payload + ".test-signature"})
	if err != nil || account != "test-jwt-account" {
		t.Fatal("account claim was not decoded")
	}
}

func TestCodexMultipleGoroutinesConcurrentRefreshSharedInstance(t *testing.T) {
	path := writeCodexAuth(t, true)
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		time.Sleep(30 * time.Millisecond)
		io.WriteString(w, `{"access_token":"shared-concurrent-access","refresh_token":"shared-concurrent-refresh","expires_in":3600}`)
	}))
	defer server.Close()

	auth := &CodexAuth{
		Path:              path,
		HTTP:              server.Client(),
		TokenURL:          server.URL,
		StaleAfter:        5 * time.Second,
		HeartbeatInterval: 50 * time.Millisecond,
	}

	const goroutines = 10
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			access, account, err := auth.Get(context.Background())
			if err != nil {
				errCh <- err
				return
			}
			if access != "shared-concurrent-access" || account != "test-account" {
				errCh <- fmt.Errorf("unexpected access=%q account=%q", access, account)
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("goroutine error: %v", err)
	}

	if refreshCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 refresh call, got %d", refreshCalls.Load())
	}
}

func TestCodexMultipleGoroutinesConcurrentRefreshDistinctInstances(t *testing.T) {
	for trial := 0; trial < 16; trial++ {
		t.Run(fmt.Sprintf("handoff-%d", trial), func(t *testing.T) {
			path := writeCodexAuth(t, true)
			var refreshCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				refreshCalls.Add(1)
				time.Sleep(30 * time.Millisecond)
				io.WriteString(w, `{"access_token":"distinct-concurrent-access","refresh_token":"distinct-concurrent-refresh","expires_in":3600}`)
			}))
			defer server.Close()

			const goroutines = 8
			var wg sync.WaitGroup
			errCh := make(chan error, goroutines)
			start := make(chan struct{})

			for i := 0; i < goroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					auth := &CodexAuth{
						Path:              path,
						HTTP:              server.Client(),
						TokenURL:          server.URL,
						StaleAfter:        30 * time.Second,
						HeartbeatInterval: 50 * time.Millisecond,
					}
					access, account, err := auth.Get(context.Background())
					if err != nil {
						errCh <- err
						return
					}
					if access != "distinct-concurrent-access" || account != "test-account" {
						errCh <- fmt.Errorf("unexpected access=%q account=%q", access, account)
					}
				}()
			}
			close(start)
			wg.Wait()
			close(errCh)

			for err := range errCh {
				t.Errorf("goroutine error: %v", err)
			}
			if refreshCalls.Load() != 1 {
				t.Fatalf("expected exactly 1 refresh call across distinct instances, got %d", refreshCalls.Load())
			}
			if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
				t.Fatal("auth.json.lock was leaked")
			}
		})
	}
}

func TestCodexRefreshThroughSymlinkPreservesLinkAndTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink auth path test is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	authPath := filepath.Join(dir, "auth.json")
	doc := codexDoc("old-access", "old-refresh", time.Now().Add(-time.Hour).UnixMilli())
	writeAuthDoc(t, target, doc)
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(target), authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"access_token":"linked-access","refresh_token":"linked-refresh","expires_in":3600}`)
	}))
	defer server.Close()
	a := &CodexAuth{Path: authPath, HTTP: server.Client(), TokenURL: server.URL}
	if err := a.Check(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("Check refreshed an expired token")
	}
	access, account, err := a.Get(context.Background())
	if err != nil || access != "linked-access" || account != "acct-test" || calls.Load() != 1 {
		t.Fatalf("linked refresh = %q, %q, calls=%d, err=%v", access, account, calls.Load(), err)
	}
	linkInfo, err := os.Lstat(authPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("refresh replaced the auth symlink")
	}
	link, err := os.Readlink(authPath)
	if err != nil || link != filepath.Base(target) {
		t.Fatalf("link text changed: %q, %v", link, err)
	}
	st, err := os.Stat(target)
	if err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("target mode changed: %v, %v", st.Mode().Perm(), err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	var updated codexCredential
	if err := json.Unmarshal(all["openai-codex"]["refresh"], &updated.Refresh); err != nil || updated.Refresh != "linked-refresh" {
		t.Fatalf("rotated refresh = %q, err=%v", updated.Refresh, err)
	}
	if !bytes.Contains(all["other"]["keep"], []byte(`"me"`)) {
		t.Fatal("unrelated provider changed")
	}
	if _, _, err := a.Get(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatalf("fresh linked read refreshed again: calls=%d err=%v", calls.Load(), err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".psi-auth-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
}

func TestCodexRefreshRefusesRetargetedLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink auth path test is POSIX-only")
	}
	dir := t.TempDir()
	targetA := filepath.Join(dir, "a.json")
	targetB := filepath.Join(dir, "b.json")
	authPath := filepath.Join(dir, "auth.json")
	writeAuthDoc(t, targetA, codexDoc("a-access", "a-refresh", time.Now().Add(-time.Hour).UnixMilli()))
	writeAuthDoc(t, targetB, codexDoc("b-access", "b-refresh", time.Now().Add(-time.Hour).UnixMilli()))
	beforeA, _ := os.ReadFile(targetA)
	beforeB, _ := os.ReadFile(targetB)
	if err := os.Symlink(filepath.Base(targetA), authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.Remove(authPath); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(filepath.Base(targetB), authPath); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"access_token":"rotated","refresh_token":"rotated-refresh","expires_in":3600}`)
	}))
	defer server.Close()
	a := &CodexAuth{Path: authPath, HTTP: server.Client(), TokenURL: server.URL}
	if _, _, err := a.Get(context.Background()); err == nil {
		t.Fatal("retargeted link was overwritten")
	}
	afterA, _ := os.ReadFile(targetA)
	afterB, _ := os.ReadFile(targetB)
	if !bytes.Equal(beforeA, afterA) || !bytes.Equal(beforeB, afterB) {
		t.Fatal("retargeted commit changed a target")
	}
	if leftover, _ := filepath.Glob(filepath.Join(dir, ".psi-auth-*")); len(leftover) != 0 {
		t.Fatalf("temporary files remain: %v", leftover)
	}
}

func TestCodexCancellationThroughLinkCommitsRotatedTokens(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink auth path test is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	authPath := filepath.Join(dir, "auth.json")
	writeAuthDoc(t, target, codexDoc("old", "old-refresh", time.Now().Add(-time.Hour).UnixMilli()))
	if err := os.Symlink(filepath.Base(target), authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: tokenTransportFunc(func(r *http.Request) (*http.Response, error) {
		body := cancelAtEOF{Reader: strings.NewReader(`{"access_token":"linked-rotated-access","refresh_token":"linked-rotated-refresh","expires_in":3600}`), cancel: cancel}
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})}
	a := &CodexAuth{Path: authPath, HTTP: client, TokenURL: "http://synthetic-token-endpoint.invalid"}
	if _, _, err := a.Get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled caller was not stopped")
	}
	_, entry, err := a.loadLocked()
	if err != nil || entry.Refresh != "linked-rotated-refresh" || entry.Access != "linked-rotated-access" {
		t.Fatalf("linked cancellation discarded rotated credentials: %+v, %v", entry, err)
	}
	if info, err := os.Lstat(authPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("cancelled refresh replaced the link")
	}
}

func TestCodexReadsFailClosedBeforeRefresh(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "directory", setup: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "invalid utf8", setup: func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte{0xff, 0xfe}, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "broken link", setup: func(t *testing.T, path string) {
			t.Helper()
			if runtime.GOOS == "windows" {
				t.Skip("symlink test is POSIX-only")
			}
			if err := os.Symlink(filepath.Join(filepath.Dir(path), "missing"), path); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}},
		{name: "cyclic link", setup: func(t *testing.T, path string) {
			t.Helper()
			if runtime.GOOS == "windows" {
				t.Skip("symlink test is POSIX-only")
			}
			if err := os.Symlink(filepath.Base(path), path); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			tc.setup(t, path)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.WriteString(w, `{"access_token":"unexpected","refresh_token":"unexpected","expires_in":3600}`)
			}))
			defer server.Close()
			a := &CodexAuth{Path: path, HTTP: server.Client(), TokenURL: server.URL}
			if err := a.Check(); err == nil {
				t.Fatal("Check accepted an unreadable credential")
			}
			if _, _, err := a.Get(context.Background()); err == nil {
				t.Fatal("Get accepted an unreadable credential")
			}
			if calls.Load() != 0 {
				t.Fatal("unreadable credential triggered a refresh")
			}
		})
	}
}

func TestCodexRefreshEnforcesFinalDocumentLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	defer server.Close()

	refresh := func(t *testing.T, path string) ([]byte, error) {
		t.Helper()
		a := &CodexAuth{Path: path, HTTP: server.Client(), TokenURL: server.URL}
		_, _, err := a.Get(context.Background())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return data, err
	}

	basePath := filepath.Join(t.TempDir(), "auth.json")
	writeCodexLimitDoc(t, basePath, "old-access", "old-refresh", 0)
	base, err := refresh(t, basePath)
	if err != nil {
		t.Fatal(err)
	}
	exactFiller := maxAuthJSONSize - len(base)
	if exactFiller < 0 {
		t.Fatalf("base document already exceeds the limit: %d", len(base))
	}
	exactPath := filepath.Join(t.TempDir(), "auth.json")
	writeCodexLimitDoc(t, exactPath, "old-access", "old-refresh", exactFiller)
	exact, err := refresh(t, exactPath)
	if err != nil || len(exact) != maxAuthJSONSize || exact[len(exact)-1] != '\n' {
		t.Fatalf("exact-limit refresh = size %d, error %v", len(exact), err)
	}

	overPath := filepath.Join(t.TempDir(), "auth.json")
	writeCodexLimitDoc(t, overPath, "o", "r", 0)
	overBase, err := refresh(t, overPath)
	if err != nil {
		t.Fatal(err)
	}
	overFiller := maxAuthJSONSize - len(overBase) + 1
	if overFiller < 0 {
		t.Fatalf("over-limit base already exceeds the limit: %d", len(overBase))
	}
	writeCodexLimitDoc(t, overPath, "o", "r", overFiller)
	before, err := os.ReadFile(overPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) > maxAuthJSONSize {
		t.Fatalf("over-limit fixture size = %d", len(before))
	}
	if _, err := refresh(t, overPath); err == nil {
		t.Fatal("over-limit refreshed document was accepted")
	}
	after, err := os.ReadFile(overPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected over-limit refresh changed the target")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(overPath), ".psi-auth-*")); len(leftovers) != 0 {
		t.Fatalf("over-limit refresh left temporary files: %v", leftovers)
	}
}

func writeCodexLimitDoc(t *testing.T, path, access, refresh string, fillerLen int) {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(codexDoc(access, refresh, 1)), &doc); err != nil {
		t.Fatal(err)
	}
	filler, err := json.Marshal(strings.Repeat("x", fillerLen))
	if err != nil {
		t.Fatal(err)
	}
	doc["filler"] = filler
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
