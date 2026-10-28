package provider

// Phase B regressions for PLAN3 F01–F03 (shared auth storage).
//
// F01: readers coordinate through the lease and never inspect a partial write.
// F02: abandoned leases recover after the Pi async stale threshold (30s);
//      fresh or replaced leases are never stolen or deleted.
// F03: refresh preserves existing permission bits and unrelated fields.
//
// All fixtures are synthetic; no live request runs (local httptest only).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeAuthDoc(t *testing.T, path, doc string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexDoc(access, refresh string, expires int64) string {
	return fmt.Sprintf(`{"openai-codex":{"type":"oauth","access":%q,"refresh":%q,"expires":%d,"accountId":"acct-test"},"other":{"keep":"me"}}`,
		access, refresh, expires)
}

// TestF01ReaderWaitsForWriterLease pauses a writer after truncation with the
// lease held. Readers must block (never report a torn document) and observe
// the completed credential after release.
func TestF01ReaderWaitsForWriterLease(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	final := codexDoc("final-access", "final-refresh", time.Now().Add(time.Hour).UnixMilli())
	writeAuthDoc(t, authPath, codexDoc("old-access", "old-refresh", time.Now().Add(time.Hour).UnixMilli()))

	unlockW, _, err := acquireAuthLock(context.Background(), authPath+".lock", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Truncate to a partial document while holding the lease.
	if err := os.WriteFile(authPath, []byte(`{"openai-codex":{"type":"oa`), 0o600); err != nil {
		t.Fatal(err)
	}
	truncated := make(chan struct{})
	close(truncated)
	proceed := make(chan struct{})

	// Writer completes only after readers are provably waiting.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer unlockW()
		select {
		case <-proceed:
		case <-time.After(10 * time.Second):
			return
		}
		_ = os.WriteFile(authPath, []byte(final), 0o600)
	}()

	<-truncated
	a := &CodexAuth{Path: authPath}
	type result struct {
		kind string
		err  error
		val  string
	}
	out := make(chan result, 4)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		err := a.Check()
		out <- result{kind: "check", err: err}
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		access, _, err := a.Get(ctx)
		out <- result{kind: "get", err: err, val: access}
	}()
	go func() {
		defer wg.Done()
		// Anthropic reader path shares the same lease protocol.
		apath := filepath.Join(dir, "a-auth.json")
		writeAuthDoc(t, apath, `{"anthropic":{"type":"api_key","key":"sk-test-key"}}`)
		wunlock, _, err := acquireAuthLock(context.Background(), apath+".lock", 0, 0)
		if err != nil {
			out <- result{kind: "select", err: err}
			return
		}
		if err := os.WriteFile(apath, []byte(`{"anth`), 0o600); err != nil {
			out <- result{kind: "select", err: err}
			wunlock()
			return
		}
		wrote := make(chan struct{})
		go func() {
			defer close(wrote)
			defer wunlock()
			// Hold the torn document long enough to prove blocking,
			// then publish the complete credential and release.
			time.Sleep(500 * time.Millisecond)
			_ = os.WriteFile(apath, []byte(`{"anthropic":{"type":"api_key","key":"sk-test-key"}}`), 0o600)
		}()
		k, src, err := SelectAPIKey(map[string]string{}, apath)
		<-wrote
		if err != nil {
			out <- result{kind: "select", err: err}
			return
		}
		out <- result{kind: "select", val: k + "|" + src}
	}()

	// Give readers a chance to (incorrectly) observe the torn file.
	time.Sleep(300 * time.Millisecond)
	select {
	case r := <-out:
		t.Fatalf("reader returned during torn write: %+v", r)
	default:
	}
	close(proceed)
	wg.Wait()
	close(out)
	for r := range out {
		if r.err != nil {
			t.Fatalf("%s: %v", r.kind, r.err)
		}
	}
	<-writerDone
	if _, _, err := a.loadLocked(); err != nil {
		t.Fatalf("final read: %v", err)
	}
}

// TestF01CancelledWaitReleasesPromptly cancels a waiting Get. The waiter
// returns the context error, the holder lease survives, and the holder can
// still complete and release cleanly (no stolen deletion, no leak).
func TestF01CancelledWaitReleasesPromptly(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeAuthDoc(t, authPath, codexDoc("a", "r", time.Now().Add(time.Hour).UnixMilli()))
	unlockW, validW, err := acquireAuthLock(context.Background(), authPath+".lock", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockW()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := (&CodexAuth{Path: authPath}).Get(ctx)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected context error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled Get did not return")
	}
	if !validW() {
		t.Fatal("holder lease compromised by cancelled waiter")
	}
}

// TestF02StaleLeaseRecovered reclaims an hour-old unowned lease quickly.
func TestF02StaleLeaseRecovered(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "auth.json.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	unlock, valid, err := acquireAuthLock(context.Background(), lock, 0, 0)
	if err != nil {
		t.Fatalf("stale lease not reclaimed: %v", err)
	}
	defer unlock()
	if time.Since(start) > 4*time.Second {
		t.Fatalf("stale reclaim too slow: %v", time.Since(start))
	}
	if !valid() {
		t.Fatal("reclaimed lease not valid")
	}
}

// TestF02FreshLeaseNeverStolen holds a lease while a contender waits with a
// short context. The contender fails; the holder lease survives intact.
func TestF02FreshLeaseNeverStolen(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "auth.json.lock")
	unlock, valid, err := acquireAuthLock(context.Background(), lock, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	before, _ := os.Stat(lock)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, err = acquireAuthLock(ctx, lock, 0, 0)
	if err == nil {
		t.Fatal("contender stole a fresh lease")
	}
	after, statErr := os.Stat(lock)
	if statErr != nil || !os.SameFile(before, after) {
		t.Fatal("holder lease disturbed by contender")
	}
	if !valid() {
		t.Fatal("holder lease compromised by contender")
	}
}

// TestF02ReplacedLeaseNotDeleted simulates a replacement lease appearing
// under a former owner. valid() reports false and unlock() leaves it alone.
func TestF02ReplacedLeaseNotDeleted(t *testing.T) {
	// The holder keeps the lock directory open, which blocks removal
	// and replacement on Windows. The scenario cannot occur there.
	if runtime.GOOS == "windows" {
		t.Skip("replacement under a live holder is impossible on Windows")
	}
	dir := t.TempDir()
	lock := filepath.Join(dir, "auth.json.lock")
	unlock, valid, err := acquireAuthLock(context.Background(), lock, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(lock); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	if valid() {
		t.Fatal("replaced lease still reports valid")
	}
	unlock()
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("unlock deleted a replacement lease: %v", err)
	}
}

// TestF02HeartbeatKeepsLeaseFresh uses a fast test heartbeat to prove mtime
// updates without a long sleep.
func TestF02HeartbeatKeepsLeaseFresh(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "auth.json.lock")
	unlock, valid, err := acquireAuthLock(context.Background(), lock, time.Hour, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, err := os.Stat(lock)
		if err != nil {
			t.Fatal(err)
		}
		if st.ModTime().After(before.ModTime()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("heartbeat never advanced the lease timestamp")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !valid() {
		t.Fatal("held lease reports invalid")
	}
}

// TestF03RefreshPreservesExistingModes covers 0600, 0640, and 0660 on a
// supported local filesystem with a local token fixture.
func TestF03RefreshPreservesExistingModes(t *testing.T) {
	// Unix mode bits do not exist on Windows; preservation there means
	// the file stays put, which other tests cover.
	if runtime.GOOS == "windows" {
		t.Skip("mode preservation needs Unix permission bits")
	}
	for _, mode := range []os.FileMode{0o600, 0o640, 0o660} {
		t.Run(fmt.Sprintf("mode-%o", mode), func(t *testing.T) {
			dir := t.TempDir()
			authPath := filepath.Join(dir, "auth.json")
			writeAuthDoc(t, authPath, codexDoc("expired", "refresh-me", time.Now().Add(-time.Hour).UnixMilli()))
			if err := os.Chmod(authPath, mode); err != nil {
				t.Skipf("filesystem ignores mode bits: %v", err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
			}))
			defer server.Close()
			a := &CodexAuth{Path: authPath, TokenURL: server.URL,
				HTTP: server.Client()}
			access, _, err := a.Get(context.Background())
			if err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if access != "new-access" {
				t.Fatalf("unexpected access %q", access)
			}
			st, err := os.Stat(authPath)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != mode {
				t.Fatalf("mode changed: got %o want %o", st.Mode().Perm(), mode)
			}
			data, _ := os.ReadFile(authPath)
			var all map[string]json.RawMessage
			if err := json.Unmarshal(data, &all); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(all["other"]), "me") {
				t.Fatal("unrelated provider lost")
			}
			if leftovers, _ := filepath.Glob(filepath.Join(dir, ".psi-auth-*")); len(leftovers) != 0 {
				t.Fatalf("temp files remain: %v", leftovers)
			}
		})
	}
}

// TestF03CommitFailures injects chmod and rename faults. The surviving
// document stays the old credential and no temp files remain.
func TestF03CommitFailures(t *testing.T) {
	mkFixture := func(t *testing.T) (string, *httptest.Server) {
		dir := t.TempDir()
		authPath := filepath.Join(dir, "auth.json")
		doc := codexDoc("expired", "refresh-me", time.Now().Add(-time.Hour).UnixMilli())
		writeAuthDoc(t, authPath, doc)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
		}))
		t.Cleanup(server.Close)
		return authPath, server
	}
	t.Run("chmod", func(t *testing.T) {
		authPath, server := mkFixture(t)
		before, _ := os.ReadFile(authPath)
		a := &CodexAuth{Path: authPath, TokenURL: server.URL, HTTP: server.Client(),
			Chmod: func(string, os.FileMode) error { return fmt.Errorf("injected chmod failure") }}
		if _, _, err := a.Get(context.Background()); err == nil {
			t.Fatal("expected chmod failure")
		}
		after, _ := os.ReadFile(authPath)
		if string(before) != string(after) {
			t.Fatal("surviving document changed on chmod failure")
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(authPath), ".psi-auth-*")); len(leftovers) != 0 {
			t.Fatalf("temp files remain: %v", leftovers)
		}
	})
	t.Run("rename", func(t *testing.T) {
		authPath, server := mkFixture(t)
		before, _ := os.ReadFile(authPath)
		a := &CodexAuth{Path: authPath, TokenURL: server.URL, HTTP: server.Client(),
			Rename: func(string, string) error { return fmt.Errorf("injected rename failure") }}
		if _, _, err := a.Get(context.Background()); err == nil {
			t.Fatal("expected rename failure")
		}
		after, _ := os.ReadFile(authPath)
		if string(before) != string(after) {
			t.Fatal("surviving document changed on rename failure")
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(authPath), ".psi-auth-*")); len(leftovers) != 0 {
			t.Fatalf("temp files remain: %v", leftovers)
		}
	})
}
