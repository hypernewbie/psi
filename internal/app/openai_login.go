package app

import (
	"context"
	"path/filepath"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/terminal"
)

// runOpenAILogin executes one attempt: device identity, baseline, browser
// authorization, single exchange, and atomic save. It returns the terminal
// result instead of mutating runtime state; the REPL owns all reporting and
// activation.
func runOpenAILogin(ctx context.Context, req oauthLoginRequest, updates chan<- oauthLoginUpdate) oauthLoginResult {
	deviceID, err := config.GetOrCreateUserDeviceID(ctx, filepath.Join(req.agentDir, "settings.json"))
	if err != nil {
		return oauthLoginResult{err: err}
	}
	login := &provider.OpenAILogin{
		Path:     filepath.Join(req.agentDir, "auth.json"),
		DeviceID: deviceID,
		HTTP:     oauthHTTPClient(req.transport),
		Bind:     req.bind,
		Rename:   req.rename,
	}
	return runBrowserOAuthLogin(ctx, login, req.browser, updates)
}

// activateOpenAILogin installs a usable model after a saved ChatGPT sign-in.
func (r *runtime) activateOpenAILogin() (terminal.NoticeKind, string) {
	return r.activateOAuthLogin("openai", "ChatGPT", "OpenAI")
}
