package app

import (
	"context"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"time"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/terminal"
)

// openAILoginBrowserTimeout bounds one browser-launcher wait. The launcher
// is only a short-lived shim: on timeout or login cancellation the owned
// launcher is reaped, never the browser it may have started.
const openAILoginBrowserTimeout = 5 * time.Second

// openAILoginSession owns one interactive sign-in from the REPL goroutine.
// Only that goroutine touches the struct; the worker talks through the
// login context and the two channels, and sends exactly one terminal
// result to the buffered done channel.
type openAILoginSession struct {
	cancel context.CancelFunc
	done   chan openAILoginResult
	// updates carries authorization-link notifications. Every send selects
	// on cancellation, so the worker never blocks when the REPL stops
	// consuming them during shutdown.
	updates chan openAILoginUpdate
	// store is the admitting session. Activation is fenced to it: a save
	// that settles after a session change is reported but not installed.
	store *session.Store
	// cancelled is set by the REPL when it cancels first. A save already
	// admitted then still settles, but it never activates a model.
	cancelled bool
}

// openAILoginUpdate is one worker-to-REPL notification. link presents the
// authorization URL once; manualOpen asks the user to open that displayed
// link by hand after the launcher failed.
type openAILoginUpdate struct {
	link       string
	manualOpen bool
}

// openAILoginResult is the terminal outcome. A nil error means the
// credential was saved. A context error means the caller cancelled before
// commit and nothing was written.
type openAILoginResult struct {
	err error
}

// openAILoginRequest carries one attempt's dependencies. transport, bind,
// browser, and rename are test seams and stay nil in production: the
// default HTTP client, the fixed loopback listener, the platform browser
// launcher, and the real filesystem rename.
type openAILoginRequest struct {
	agentDir  string
	transport http.RoundTripper
	bind      func() (net.Listener, error)
	browser   func(context.Context, string) error
	rename    func(string, string) error
}

// runOpenAILogin executes one attempt: device identity, baseline, browser
// authorization, single exchange, and atomic save. It returns the terminal
// result instead of mutating runtime state; the REPL owns all reporting
// and activation.
func runOpenAILogin(ctx context.Context, req openAILoginRequest, updates chan<- openAILoginUpdate) openAILoginResult {
	deviceID, err := config.GetOrCreateUserDeviceID(ctx, filepath.Join(req.agentDir, "settings.json"))
	if err != nil {
		return openAILoginResult{err: err}
	}
	var httpClient *http.Client
	if req.transport != nil {
		httpClient = &http.Client{Transport: req.transport}
	}
	login := &provider.OpenAILogin{
		Path:     filepath.Join(req.agentDir, "auth.json"),
		DeviceID: deviceID,
		HTTP:     httpClient,
		Bind:     req.bind,
		Rename:   req.rename,
	}
	launch := req.browser
	if launch == nil {
		launch = openOpenAILoginBrowser
	}
	err = login.Run(ctx, func(authURL string) {
		select {
		case updates <- openAILoginUpdate{link: authURL}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			return
		}
		launchCtx, stop := context.WithTimeout(ctx, openAILoginBrowserTimeout)
		defer stop()
		if err := launch(launchCtx, authURL); err != nil {
			select {
			case updates <- openAILoginUpdate{manualOpen: true}:
			case <-ctx.Done():
			}
		}
	})
	return openAILoginResult{err: err}
}

// openOpenAILoginBrowser opens the authorization URL without a shell, the
// way Pi launches its browser. Output is discarded so a raw launcher error
// can never leak the URL; the caller reports a static instruction instead.
// exec.CommandContext reaps only the owned launcher shim on timeout or
// cancellation, never the browser, its profile, or unrelated children.
func openOpenAILoginBrowser(ctx context.Context, authURL string) error {
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

// activateOpenAILogin installs a usable model after a saved sign-in. A
// working non-OpenAI selection is retained; otherwise the current
// supported OpenAI model is rechecked, or the official OpenAI default is
// selected for an offline or missing selection. Only a candidate that
// passes both configure and its own authErr can install: configure
// returns nil while recording unavailable credentials in authErr. The
// model-change entry is appended only when the selection moves.
func (r *runtime) activateOpenAILogin() (terminal.NoticeKind, string) {
	if r.authErr == nil && r.ov.Provider != "openai" {
		return terminal.NoticeInfo, "Signed in to ChatGPT. Model unchanged: " + r.ov.Provider + "/" + r.ov.Model
	}
	next := config.StartupOverrides{Provider: "openai", Thinking: r.ov.Thinking, ThinkingExplicit: true}
	if r.ov.Provider == "openai" {
		if meta, err := r.models.SelectModel(r.ov.Provider, r.ov.Model); err == nil {
			next.Model = meta.ID
		}
	}
	if next.Model == "" {
		next.ThinkingExplicit = false
		meta, err := r.models.SelectModel("openai", "")
		if err != nil {
			return terminal.NoticeError, "Signed in to ChatGPT, but no OpenAI model is available: " + err.Error()
		}
		next.Model = meta.ID
	}
	candidate := *r
	candidate.prov = nil
	if err := candidate.configure(next); err != nil {
		if candidate.prov != nil {
			candidate.prov.CloseIdle()
		}
		return terminal.NoticeError, "Signed in to ChatGPT, but model activation failed: " + err.Error() + ". Use /model to select a model."
	}
	if candidate.authErr != nil {
		if candidate.prov != nil {
			candidate.prov.CloseIdle()
		}
		return terminal.NoticeError, "Signed in to ChatGPT, but model activation failed: " + candidate.authErr.Error() + ". Use /model to select a model."
	}
	if candidate.ov.Provider != r.ov.Provider || candidate.ov.Model != r.ov.Model {
		if err := r.selectModel(&candidate); err != nil {
			if candidate.prov != nil {
				candidate.prov.CloseIdle()
			}
			return terminal.NoticeError, "Signed in to ChatGPT, but the session update failed: " + err.Error()
		}
	} else {
		r.adoptConfiguration(&candidate)
		r.publishLive()
	}
	if r.ac != nil {
		r.ac.ResetGrowth()
	}
	return terminal.NoticeInfo, "Signed in to ChatGPT. Model: " + candidate.ov.Provider + "/" + candidate.ov.Model
}
