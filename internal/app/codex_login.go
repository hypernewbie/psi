package app

import (
	"context"
	"path/filepath"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/terminal"
)

// runCodexLogin executes one Codex browser attempt: baseline, loopback
// authorization, single exchange, and atomic save. It returns the terminal
// result instead of mutating runtime state; the REPL owns all reporting and
// activation.
func runCodexLogin(ctx context.Context, req oauthLoginRequest, updates chan<- oauthLoginUpdate) oauthLoginResult {
	login := &provider.CodexLogin{
		Path:   filepath.Join(req.agentDir, "auth.json"),
		HTTP:   oauthHTTPClient(req.transport),
		Bind:   req.codexBind,
		Rename: req.rename,
	}
	return runBrowserOAuthLogin(ctx, login, req.browser, updates)
}

// runCodexDeviceLogin runs one headless attempt. It never binds a listener;
// only an explicit Enter in the login panel can open the verification page.
func runCodexDeviceLogin(ctx context.Context, req oauthLoginRequest, updates chan<- oauthLoginUpdate) oauthLoginResult {
	login := &provider.CodexDeviceLogin{
		Path:   filepath.Join(req.agentDir, "auth.json"),
		HTTP:   oauthHTTPClient(req.transport),
		Rename: req.rename,
		Wait:   req.wait,
	}
	return runDeviceOAuthLogin(ctx, login, req.browser, updates)
}

// activateCodexLogin installs a usable model after a saved OpenAI Codex
// sign-in. It follows the same retention and offline-default rules as the
// ChatGPT login.
func (r *runtime) activateCodexLogin() (terminal.NoticeKind, string) {
	return r.activateOAuthLogin("openai-codex", "OpenAI Codex", "OpenAI Codex")
}
