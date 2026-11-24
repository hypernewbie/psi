package app

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/terminal"
)

// waitAppOutputAfter waits for want in output written after start. The
// offset keeps repeated dialogs from matching an earlier frame.
func waitAppOutputAfter(t *testing.T, out *notifyingBuffer, start int, want string) int {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for {
		text := out.String()
		if start < 0 || start > len(text) {
			start = len(text)
		}
		if index := strings.Index(text[start:], want); index >= 0 {
			return start + index + len(want)
		}
		select {
		case <-out.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %q after offset %d", want, start)
		}
	}
}

// TestLogoutRequiresConfirmation proves that only a fresh y or Y confirms a
// logout. Ignored keys, paste, and cancellation must not remove the credential
// or start a revocation request.
func TestLogoutRequiresConfirmation(t *testing.T) {
	// The fake transport below is the only intended request path. If a future
	// change bypasses it, these guards keep the synthetic tokens local.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:0")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:0")
	r := testRuntime(t)
	authPath := filepath.Join(r.agentDir, "auth.json")
	writeAppFile(t, authPath, `{"deepseek":{"type":"api_key","key":"synthetic-deepseek-key"},"openai":{"type":"oauth","access":"synthetic-oauth-access"}}`)
	baseline, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var revocations atomic.Int32
	r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		revocations.Add(1)
		_ = req.Body.Close()
		return nil, io.ErrUnexpectedEOF
	})
	_, out, send, finish := startBumpREPL(t, r)
	offset := len(out.String())
	wait := func(want string) {
		t.Helper()
		offset = waitAppOutputAfter(t, out, offset, want)
	}
	assertUnchanged := func(stage string) {
		t.Helper()
		data, readErr := os.ReadFile(authPath)
		if readErr != nil || !bytes.Equal(data, baseline) {
			t.Fatalf("%s changed auth.json: err=%v", stage, readErr)
		}
		if got := revocations.Load(); got != 0 {
			t.Fatalf("%s attempted %d revocation request(s)", stage, got)
		}
	}

	send("/logout\r")
	wait("Stored credentials")
	send("openai\r")
	wait("Log out of OpenAI?")
	wait("Y — Yes    N — No")
	assertUnchanged("selecting a provider")

	// Cancel variants: N, Esc, then Ctrl+C. The hotkeys output is the barrier
	// that proves those keys were processed.
	send("N")
	wait("Stored credentials")
	send("\r")
	wait("Log out of OpenAI?")
	send("\x1b")
	wait("Stored credentials")
	send("\r")
	wait("Log out of OpenAI?")
	send("\x03")
	wait("Stored credentials")
	send("\x03")
	send("/hotkeys\r")
	wait("Keys (fixed, no Kitty required):")
	assertUnchanged("cancel")

	// The ignored inputs must not confirm. N then cancels the dialog, and the
	// hotkeys output proves the whole sequence ran before the file is checked.
	send("/logout\r")
	wait("Stored credentials")
	send("openai\r")
	wait("Log out of OpenAI?")
	send("\r")
	send("x")
	send("\x04")
	send("\x1b[200~y\x1b[201~")
	send("N")
	wait("Stored credentials")
	send("\x03")
	send("/hotkeys\r")
	wait("Keys (fixed, no Kitty required):")
	assertUnchanged("ignored input")

	// The ignored inputs must also not cancel: the later y still confirms.
	send("/logout\r")
	wait("Stored credentials")
	send("openai\r")
	wait("Log out of OpenAI?")
	send("\r")
	send("x")
	send("\x04")
	send("\x1b[200~y\x1b[201~")
	send("y")
	wait("Removed stored OAuth credential for OpenAI")
	wait("Revocation unconfirmed.")
	if got := revocations.Load(); got != 1 {
		t.Fatalf("revocation requests = %d, want 1", got)
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"openai"`) || !strings.Contains(string(data), "synthetic-deepseek-key") {
		t.Fatalf("logout removed the wrong entries: %s", data)
	}
	send("/quit\r")
	finish()
}

// TestLogoutDialogClearedWhenPickerReopens pins the invariant that a pending
// logout dialog never outlives the picker that opened it. A stale dialog over
// a different selector would render the wrong rows and answer the new
// picker's keys as dialog keys.
func TestLogoutDialogClearedWhenPickerReopens(t *testing.T) {
	r := testRuntime(t)
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"deepseek":{"type":"api_key","key":"synthetic-deepseek-key"}}`)
	console := authCommandConsole(t)
	r.lifecycle = &terminalLifecycle{console: console}
	r.state = StateIdle
	if err := r.logoutCommand("", console, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.selector == nil || r.selector.AuthMode != AuthLogout {
		t.Fatalf("/logout did not open its picker: %+v", r.selector)
	}
	r.startLogoutConfirm(provider.StoredCredential{ProviderID: "deepseek", Kind: provider.CredentialAPIKey}, console)
	if r.logoutConfirm == nil {
		t.Fatal("selected provider did not open the confirmation dialog")
	}
	r.openAuthSelector(loginSelector(r), console)
	if r.logoutConfirm != nil {
		t.Fatal("replacing the picker kept the stale logout dialog")
	}
}

func TestLogoutConfirmDialogFitsTerminal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		provider  string
		id        string
		width     int
		rowBudget int
		rowCount  int
		ok        bool
		want      []string
	}{
		{"wide", "OpenAI", "openai", 80, 18, 2, true, []string{"Log out of OpenAI?", "Y — Yes", "N — No"}},
		{"narrow two rows", "OpenAI", "openai", 20, 18, 2, true, []string{"Log out of OpenAI?", "Y — Yes", "N — No"}},
		{"short terminal", "OpenAI", "openai", 80, 1, 1, true, []string{"Log out openai? y/n"}},
		{"too short", "OpenAI", "openai", 80, 0, 1, false, []string{"Widen terminal"}},
		{"long provider id", "Google Generative AI", "google", 20, 2, 1, true, []string{"Log out google? y/n"}},
		{"too narrow", "Google Generative AI", "google", 17, 18, 1, false, []string{"Widen terminal"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, ok := logoutConfirmDialog(tc.provider, tc.id, tc.width, tc.rowBudget)
			joined := strings.Join(rows, "\n")
			if ok != tc.ok || len(rows) != tc.rowCount {
				t.Fatalf("dialog at %dx%d = %q, ok=%v; want %d rows, ok=%v", tc.width, tc.rowBudget, rows, ok, tc.rowCount, tc.ok)
			}
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("dialog at %dx%d = %q; missing %q", tc.width, tc.rowBudget, rows, want)
				}
			}
			if !ok {
				return
			}
			if len(rows) > tc.rowBudget {
				t.Fatalf("confirmable dialog needs %d rows but has budget %d", len(rows), tc.rowBudget)
			}
			for _, row := range rows {
				if terminal.CellWidth(row) > tc.width-1 {
					t.Fatalf("row %q exceeds width %d", row, tc.width)
				}
			}
		})
	}
}
