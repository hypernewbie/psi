package app

import (
	"context"
	"net"
	"net/http"
	"os/exec"
	goruntime "runtime"
	"time"

	"psi/internal/config"
	"psi/internal/session"
	"psi/internal/terminal"
)

// oauthLoginBrowserTimeout bounds one browser-launcher wait. The launcher is
// only a short-lived shim: on timeout or login cancellation the owned
// launcher is reaped, never the browser it may have started.
const oauthLoginBrowserTimeout = 5 * time.Second

// oauthLoginSession owns one interactive OAuth sign-in from the REPL
// goroutine. Only that goroutine touches the struct; the worker talks through
// the login context and channels, and sends exactly one terminal result to
// the buffered done channel.
type oauthLoginSession struct {
	cancel context.CancelFunc
	done   chan oauthLoginResult
	// store is the admitting session. Activation is fenced to it: a save
	// that settles after a session change is reported but not installed.
	store *session.Store
	// label names the provider in user-facing settlement text, for example
	// ChatGPT or OpenAI Codex.
	label string
	// activate installs a usable model after a successful save. It runs on
	// the REPL goroutine and returns the notice kind and text.
	activate func() (terminal.NoticeKind, string)
	// cancelled is set by the REPL when it cancels first. A save already
	// admitted then still settles, but it never activates a model.
	cancelled bool
}

// oauthLoginRequest carries one browser sign-in attempt's shared
// dependencies. transport, bind, browser, and rename are test seams and stay
// nil in production: the default HTTP client, the fixed loopback listener,
// the platform browser launcher, and the real filesystem rename.
type oauthLoginRequest struct {
	agentDir  string
	transport http.RoundTripper
	bind      func() (net.Listener, error)
	browser   func(context.Context, string) error
	rename    func(string, string) error
}

// oauthLoginUpdate is one worker-to-REPL notification. link presents the
// authorization URL once; manualOpen asks the user to open that displayed
// link by hand after the launcher failed.
type oauthLoginUpdate struct {
	link       string
	manualOpen bool
}

// oauthLoginResult is the terminal outcome. A nil error means the credential
// was saved. A context error means the caller cancelled before commit and
// nothing was written.
type oauthLoginResult struct {
	err error
}

// browserOAuthLogin is the one provider contract the shared worker needs.
// Both OpenAI implementations satisfy it without grants being shared.
type browserOAuthLogin interface {
	Run(context.Context, func(string)) error
}

// runBrowserOAuthLogin runs one browser attempt: it presents the
// authorization URL, launches the browser with a bounded wait, and returns
// the provider's terminal result. Browser launch happens after the update is
// queued and never delays the callback server.
func runBrowserOAuthLogin(ctx context.Context, login browserOAuthLogin, browser func(context.Context, string) error, updates chan<- oauthLoginUpdate) oauthLoginResult {
	launch := browser
	if launch == nil {
		launch = openOAuthLoginBrowser
	}
	err := login.Run(ctx, func(authURL string) {
		select {
		case updates <- oauthLoginUpdate{link: authURL}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			return
		}
		launchCtx, stop := context.WithTimeout(ctx, oauthLoginBrowserTimeout)
		defer stop()
		if err := launch(launchCtx, authURL); err != nil {
			select {
			case updates <- oauthLoginUpdate{manualOpen: true}:
			case <-ctx.Done():
			}
		}
	})
	return oauthLoginResult{err: err}
}

// oauthHTTPClient returns the transport override as a client, or nil so the
// provider selects its default client.
func oauthHTTPClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		return nil
	}
	return &http.Client{Transport: transport}
}

// openOAuthLoginBrowser opens the authorization URL without a shell, the way
// Pi launches its browser. Output is discarded so a raw launcher error can
// never leak the URL; the caller reports a static instruction instead.
// exec.CommandContext reaps only the owned launcher shim on timeout or
// cancellation, never the browser, its profile, or unrelated children.
func openOAuthLoginBrowser(ctx context.Context, authURL string) error {
	var name string
	var args []string
	switch goruntime.GOOS {
	case "darwin":
		name, args = "open", []string{authURL}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", authURL}
	default:
		name, args = "xdg-open", []string{authURL}
	}
	// Leave stdio nil so os/exec uses the null device, not output pipes
	// that a browser child could inherit and keep open after the shim exits.
	return exec.CommandContext(ctx, name, args...).Run()
}

// activateOAuthLogin installs a usable model after a saved OAuth sign-in. A
// working selection from another provider is retained; otherwise the current
// provider model is rechecked, or that provider's default is selected for an
// offline or missing selection. Only a candidate that passes both configure
// and its own authErr can install: configure returns nil while recording
// unavailable credentials in authErr. The model-change entry is appended only
// when the selection moves. label names the signed-in subscription; provider
// names the model family in the no-model error.
func (r *runtime) activateOAuthLogin(providerID, label, provider string) (terminal.NoticeKind, string) {
	if r.authErr == nil && r.ov.Provider != providerID {
		return terminal.NoticeInfo, "Signed in to " + label + ". Model unchanged: " + r.ov.Provider + "/" + r.ov.Model
	}
	next := config.StartupOverrides{Provider: providerID, Thinking: r.ov.Thinking, ThinkingExplicit: true}
	if r.ov.Provider == providerID {
		if meta, err := r.models.SelectModel(r.ov.Provider, r.ov.Model); err == nil {
			next.Model = meta.ID
		}
	}
	if next.Model == "" {
		next.ThinkingExplicit = false
		meta, err := r.models.SelectModel(providerID, "")
		if err != nil {
			return terminal.NoticeError, "Signed in to " + label + ", but no " + provider + " model is available: " + err.Error()
		}
		next.Model = meta.ID
	}
	candidate := *r
	candidate.prov = nil
	if err := candidate.configure(next); err != nil {
		if candidate.prov != nil {
			candidate.prov.CloseIdle()
		}
		return terminal.NoticeError, "Signed in to " + label + ", but model activation failed: " + err.Error() + ". Use /model to select a model."
	}
	if candidate.authErr != nil {
		if candidate.prov != nil {
			candidate.prov.CloseIdle()
		}
		return terminal.NoticeError, "Signed in to " + label + ", but model activation failed: " + candidate.authErr.Error() + ". Use /model to select a model."
	}
	if candidate.ov.Provider != r.ov.Provider || candidate.ov.Model != r.ov.Model {
		if err := r.selectModel(&candidate); err != nil {
			if candidate.prov != nil {
				candidate.prov.CloseIdle()
			}
			return terminal.NoticeError, "Signed in to " + label + ", but the session update failed: " + err.Error()
		}
	} else {
		r.adoptConfiguration(&candidate)
		r.publishLive()
	}
	if r.ac != nil {
		r.ac.ResetGrowth()
	}
	return terminal.NoticeInfo, "Signed in to " + label + ". Model: " + candidate.ov.Provider + "/" + candidate.ov.Model
}
