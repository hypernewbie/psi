package app

import (
	"context"
	"net"
	"net/http"
	"path/filepath"

	"psi/internal/provider"
	"psi/internal/terminal"
)

// codexLoginRequest carries one OpenAI Codex attempt. It adds no device
// identity: the Codex grant sends neither a device ID nor a nonce.
type codexLoginRequest struct {
	agentDir  string
	transport http.RoundTripper
	bind      func() (net.Listener, error)
	browser   func(context.Context, string) error
	rename    func(string, string) error
}

// runCodexLogin executes one Codex browser attempt: baseline, loopback
// authorization, single exchange, and atomic save. It returns the terminal
// result instead of mutating runtime state; the REPL owns all reporting and
// activation.
func runCodexLogin(ctx context.Context, req codexLoginRequest, updates chan<- oauthLoginUpdate) oauthLoginResult {
	login := &provider.CodexLogin{
		Path:   filepath.Join(req.agentDir, "auth.json"),
		HTTP:   oauthHTTPClient(req.transport),
		Bind:   req.bind,
		Rename: req.rename,
	}
	return runBrowserOAuthLogin(ctx, login, req.browser, updates)
}

// activateCodexLogin installs a usable model after a saved OpenAI Codex
// sign-in. It follows the same retention and offline-default rules as the
// ChatGPT login.
func (r *runtime) activateCodexLogin() (terminal.NoticeKind, string) {
	return r.activateOAuthLogin("openai-codex", "OpenAI Codex", "OpenAI Codex")
}
