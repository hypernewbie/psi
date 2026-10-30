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
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancelled"}[cancelled], func(t *testing.T) {
			path := writeCodexAuth(t, true)
			before, _ := os.ReadFile(path)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if cancelled {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"dummy-refresh must not appear in diagnostics"}`)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			a := &CodexAuth{Path: path, HTTP: server.Client(), TokenURL: server.URL}
			_, _, err := a.Get(ctx)
			if err == nil || strings.Contains(err.Error(), "dummy-refresh") {
				t.Fatal("refresh failure was accepted or leaked a token")
			}
			if cancelled && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("refresh ignored cancellation")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed/cancelled refresh rewrote credentials")
			}
			if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
				t.Fatal("failed refresh leaked a lock")
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".psi-auth-*"))
			if len(leftovers) > 0 {
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

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			auth := &CodexAuth{
				Path:              path,
				HTTP:              server.Client(),
				TokenURL:          server.URL,
				StaleAfter:        5 * time.Second,
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
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("goroutine error: %v", err)
	}

	if refreshCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 refresh call across distinct instances, got %d", refreshCalls.Load())
	}

	// Lock should not be leaked
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("auth.json.lock was leaked")
	}
}
