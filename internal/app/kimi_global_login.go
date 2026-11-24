package app

import (
	"context"
	"path/filepath"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/providerregistry"
	"github.com/hypernewbie/psi/internal/terminal"
)

// runKimiGlobalLogin runs one Global Kimi Code device attempt. It never binds
// a listener; only an explicit Enter in the login panel can open the validated
// verification link.
func runKimiGlobalLogin(ctx context.Context, req oauthLoginRequest, updates chan<- oauthLoginUpdate) oauthLoginResult {
	login := &provider.KimiGlobalLogin{
		Path:      filepath.Join(req.agentDir, "auth.json"),
		HTTP:      oauthHTTPClient(req.transport),
		Rename:    req.rename,
		Wait:      req.wait,
		UserAgent: req.userAgent,
	}
	return runDeviceOAuthLogin(ctx, login, req.browser, updates)
}

// activateKimiGlobalLogin installs a usable model after a saved Global Kimi
// sign-in. It follows the same retention and offline-default rules as the
// other OAuth logins.
func (r *runtime) activateKimiGlobalLogin() (terminal.NoticeKind, string) {
	return r.activateOAuthLogin(providerregistry.KimiGlobalProviderID, "Kimi Code (Global)", "Kimi Code (Global)")
}
