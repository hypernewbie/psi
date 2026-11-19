package app

import (
	"context"
	"path/filepath"

	"psi/internal/provider"
	"psi/internal/terminal"
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

// runCodexDeviceLogin runs one headless attempt. It never binds a listener or
// launches a browser. The user code is sent to the interactive sign-in view
// before the first poll; no session or bridge data receives it.
func runCodexDeviceLogin(ctx context.Context, req oauthLoginRequest, updates chan<- oauthLoginUpdate) oauthLoginResult {
	login := &provider.CodexDeviceLogin{
		Path:   filepath.Join(req.agentDir, "auth.json"),
		HTTP:   oauthHTTPClient(req.transport),
		Rename: req.rename,
		Wait:   req.wait,
	}
	err := login.Run(ctx, func(userCode, verificationURL string) {
		select {
		case updates <- oauthLoginUpdate{deviceCode: userCode, deviceURL: verificationURL}:
		case <-ctx.Done():
		}
	})
	return oauthLoginResult{err: err}
}

// activateCodexLogin installs a usable model after a saved OpenAI Codex
// sign-in. It follows the same retention and offline-default rules as the
// ChatGPT login.
func (r *runtime) activateCodexLogin() (terminal.NoticeKind, string) {
	return r.activateOAuthLogin("openai-codex", "OpenAI Codex", "OpenAI Codex")
}
