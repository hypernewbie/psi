package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/providerregistry"
	"psi/internal/secretmem"
	"psi/internal/session"
	"psi/internal/terminal"
)

type loginPrompt struct {
	providerID   string
	providerName string
	wasOffline   bool
}

func loginProviderIDs() []string {
	ids := make([]string, 0, len(providerregistry.IDs()))
	for _, id := range providerregistry.IDs() {
		spec, ok := providerregistry.Lookup(id)
		if !ok {
			continue
		}
		if id != "openai" && id != "openai-codex" && (spec.Auth != providerregistry.AuthAPIKey || id == "anthropic") {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func loginSelector(r *runtime) *Selector {
	ids := loginProviderIDs()
	items := make([]session.SessionInfo, 0, len(ids))
	for _, id := range ids {
		spec, ok := providerregistry.Lookup(id)
		if !ok {
			continue
		}
		method := spec.Environment
		switch id {
		case "openai":
			method = "ChatGPT browser sign-in"
		case "openai-codex":
			method = "browser or device code sign-in"
		}
		parts := []string{spec.Name, method}
		if r.models != nil {
			if model, err := r.models.SelectModel(id, ""); err == nil {
				parts = append(parts, model.ID)
			}
		}
		items = append(items, session.SessionInfo{ID: id, FirstMessage: strings.Join(parts, " · ")})
	}
	selector := NewSelector(items, r.ov.Provider)
	selector.AuthMode = AuthLogin
	return selector
}

// codexLoginMethodSelector offers browser sign-in first. Choosing a row starts
// that method immediately; Escape has no network or credential effect.
func codexLoginMethodSelector() *Selector {
	items := []session.SessionInfo{
		{ID: "browser", Name: "Browser login (default)"},
		{ID: "device", Name: "Device code login (headless)"},
	}
	selector := NewSelector(items, "")
	selector.AuthMode = AuthCodexMethods
	return selector
}

// openCodexLoginMethodSelector rechecks admission before showing the method
// picker. The chosen method is admitted again after the picker closes.
func (r *runtime) openCodexLoginMethodSelector(console *terminal.Console) error {
	if err := r.authCommandAvailable(console, "login"); err != nil {
		return err
	}
	r.openAuthSelector(codexLoginMethodSelector(), console)
	return nil
}

func logoutSelector(credentials []provider.StoredCredential, activeID string) *Selector {
	items := make([]session.SessionInfo, 0, len(credentials))
	kinds := make(map[string]provider.CredentialKind, len(credentials))
	for _, credential := range credentials {
		spec, ok := providerregistry.Lookup(credential.ProviderID)
		if !ok {
			continue
		}
		detail := "API key"
		if credential.Kind == provider.CredentialOAuth {
			detail = "OAuth credential"
		}
		items = append(items, session.SessionInfo{ID: credential.ProviderID, FirstMessage: spec.Name + " · " + detail})
		kinds[credential.ProviderID] = credential.Kind
	}
	selector := NewSelector(items, activeID)
	selector.AuthMode = AuthLogout
	selector.CredentialKinds = kinds
	return selector
}

func (r *runtime) authCommandAvailable(console *terminal.Console, name string) error {
	if r.lifecycle == nil || r.lifecycle.console == nil || console == nil {
		return fmt.Errorf("%s requires an interactive terminal", name)
	}
	if r.store == nil {
		return fmt.Errorf("%s requires an active session", name)
	}
	if err := r.store.WriteError(); err != nil {
		return fmt.Errorf("storage is blocked: %w", err)
	}
	if !r.state.CanSubmit() || r.selector != nil || r.settingsPicker != nil || r.login != nil ||
		(r.bridgeWork != nil && r.bridgeWork()) || r.compactActive || r.compactPending || r.reloadPending {
		return fmt.Errorf("%s is unavailable while a run or picker is active", name)
	}
	return nil
}

func (r *runtime) openAuthSelector(selector *Selector, console *terminal.Console) {
	r.selector = selector
	r.selectorDraft, r.selectorCursor, r.selectorRestoreOnSuccess = "", 0, false
	r.settingsPicker = nil
	console.ClearPickerRows()
	console.Editor.Clear()
	r.state = StateSelecting
	r.draw(console)
}

func (r *runtime) loginCommand(arg string, console *terminal.Console) error {
	if err := r.authCommandAvailable(console, "login"); err != nil {
		return err
	}
	parts := strings.Fields(arg)
	if len(parts) == 0 {
		r.openAuthSelector(loginSelector(r), console)
		return nil
	}
	if len(parts) != 1 {
		return fmt.Errorf("use /login [provider]")
	}
	providerID := strings.ToLower(parts[0])
	authPath := filepath.Join(r.agentDir, "auth.json")
	switch providerID {
	case "anthropic":
		return fmt.Errorf("Anthropic login is not available; set ANTHROPIC_API_KEY or add an api_key entry to %s.", authPath)
	case "openai-codex":
		return r.openCodexLoginMethodSelector(console)
	}
	spec, ok := providerregistry.Lookup(providerID)
	if !ok {
		return fmt.Errorf("unknown provider %q; supported: %s", providerID, strings.Join(loginProviderIDs(), ", "))
	}
	if spec.Auth != providerregistry.AuthAPIKey {
		return fmt.Errorf("unknown provider %q; supported: %s", providerID, strings.Join(loginProviderIDs(), ", "))
	}
	r.startLogin(providerID, spec.Name, console)
	return nil
}

func (r *runtime) startLogin(providerID, providerName string, console *terminal.Console) {
	r.selector = nil
	r.selectorDraft, r.selectorCursor, r.selectorRestoreOnSuccess = "", 0, false
	r.settingsPicker = nil
	console.ClearPickerRows()
	console.Editor.Clear()
	console.BeginSecret()
	console.Status("API key for " + providerName + " · Enter save · Esc cancel")
	r.login = &loginPrompt{providerID: providerID, providerName: providerName, wasOffline: r.authErr != nil}
	r.state = StateSelecting
	r.draw(console)
}

func (r *runtime) restoreAuthState() {
	if r.store != nil && r.store.WriteError() != nil {
		r.state = StateStorageBlocked
		return
	}
	if r.authErr != nil {
		r.state = StateOffline
		return
	}
	r.state = StateIdle
}

func (r *runtime) cancelLogin(console *terminal.Console) {
	if r.login == nil {
		return
	}
	console.DiscardSecret()
	console.Status("")
	r.login = nil
	r.restoreAuthState()
	r.draw(console)
}

func saveLoginKey(ctx context.Context, authPath, providerID string, key []byte) error {
	defer secretmem.Wipe(key[:cap(key)])
	return provider.SaveAPIKey(ctx, authPath, providerID, key)
}

func (r *runtime) finishLogin(console *terminal.Console, out io.Writer) {
	prompt := r.login
	if prompt == nil {
		return
	}
	key := console.EndSecret()
	console.Status("")
	r.login = nil
	authPath := filepath.Join(r.agentDir, "auth.json")
	if err := saveLoginKey(context.Background(), authPath, prompt.providerID, key); err != nil {
		printStatusText(out, fmt.Sprintf("Failed to save API key for %s: %v", prompt.providerName, err))
		r.restoreAuthState()
		r.draw(console)
		return
	}

	status := ""
	if prompt.wasOffline {
		meta, err := r.models.SelectModel(prompt.providerID, "")
		if err != nil {
			status = fmt.Sprintf("Saved API key for %s, but no default model is configured. Use /model.", prompt.providerName)
		} else {
			next := r.ov
			next.Provider, next.Model = prompt.providerID, meta.ID
			next.ThinkingExplicit = false
			candidate := *r
			candidate.prov = nil
			selectionErr := candidate.configure(next)
			if selectionErr == nil {
				selectionErr = candidate.authErr
			}
			if selectionErr == nil {
				selectionErr = r.selectModel(&candidate)
			}
			if selectionErr != nil {
				if candidate.prov != nil {
					candidate.prov.CloseIdle()
				}
				status = fmt.Sprintf("Saved API key for %s, but could not select the default model: %v", prompt.providerName, selectionErr)
			} else {
				if r.ac != nil {
					r.ac.ResetGrowth()
				}
				r.settings.DefaultProvider, r.settings.DefaultModel = candidate.ov.Provider, candidate.ov.Model
				providerValue, _ := json.Marshal(candidate.ov.Provider)
				modelValue, _ := json.Marshal(candidate.ov.Model)
				if err := config.UpdateUserSettings(filepath.Join(r.agentDir, "settings.json"), []config.SettingsUpdate{
					{Path: []string{"defaultProvider"}, Value: providerValue},
					{Path: []string{"defaultModel"}, Value: modelValue},
				}); err != nil {
					status = fmt.Sprintf("Saved API key for %s and selected %s, but could not save defaults: %v", prompt.providerName, candidate.ov.Model, err)
				} else {
					status = fmt.Sprintf("Saved API key for %s. Selected %s. Credentials saved to %s.", prompt.providerName, candidate.ov.Model, authPath)
				}
			}
		}
	} else if prompt.providerID == r.ov.Provider {
		candidate := *r
		candidate.prov = nil
		refreshErr := candidate.configure(r.ov)
		if refreshErr == nil {
			refreshErr = candidate.authErr
		}
		if refreshErr != nil {
			if candidate.prov != nil {
				candidate.prov.CloseIdle()
			}
			if r.prov != nil {
				r.prov.CloseIdle()
			}
			r.prov = nil
			r.authErr = refreshErr
			status = fmt.Sprintf("Saved API key for %s, but could not refresh the active provider: %v", prompt.providerName, refreshErr)
		} else {
			r.adoptConfiguration(&candidate)
			status = fmt.Sprintf("Saved API key for %s. Credentials saved to %s.", prompt.providerName, authPath)
		}
		r.publishLive()
	} else {
		status = fmt.Sprintf("Saved API key for %s. Credentials saved to %s.", prompt.providerName, authPath)
	}

	if status != "" {
		printStatusText(out, status)
	}
	r.restoreAuthState()
	r.draw(console)
}

func (r *runtime) logoutCommand(arg string, console *terminal.Console, out io.Writer) error {
	if err := r.authCommandAvailable(console, "logout"); err != nil {
		return err
	}
	if strings.TrimSpace(arg) != "" {
		return fmt.Errorf("use /logout")
	}
	authPath := filepath.Join(r.agentDir, "auth.json")
	credentials, err := provider.StoredCredentials(authPath)
	if err != nil {
		return err
	}
	if len(credentials) == 0 {
		printStatusText(out, "No stored credentials to remove. /logout only removes entries from auth.json; environment variables and models.json config are unchanged.")
		r.draw(console)
		return nil
	}
	r.openAuthSelector(logoutSelector(credentials, r.ov.Provider), console)
	return nil
}

func (r *runtime) finishLogout(selected provider.StoredCredential, console *terminal.Console, out io.Writer) {
	r.selector = nil
	console.ClearPickerRows()
	r.selectorDraft, r.selectorCursor, r.selectorRestoreOnSuccess = "", 0, false
	console.Editor.Clear()
	r.restoreAuthState()

	spec, ok := providerregistry.Lookup(selected.ProviderID)
	if !ok {
		printStatusText(out, fmt.Sprintf("Failed to remove stored credential for %s: unsupported provider", selected.ProviderID))
		r.draw(console)
		return
	}
	if selected.Kind == provider.CredentialOAuth {
		console.Status("Removing stored OAuth credential…")
	}
	result, err := provider.LogoutCredential(context.Background(), filepath.Join(r.agentDir, "auth.json"), selected, r.logoutHTTPClient(selected))
	if selected.Kind == provider.CredentialOAuth {
		console.Status("")
	}
	if err != nil {
		status := fmt.Sprintf("Failed to remove stored %s for %s: %v", logoutKindName(selected.Kind), spec.Name, err)
		if selected.Kind == provider.CredentialOAuth {
			status += "\n" + logoutRevocationText(result.Revocation)
		}
		printLogoutNotice(out, terminal.NoticeError, status)
		r.restoreAuthState()
		r.draw(console)
		return
	}
	if result.Removed && selected.ProviderID == r.ov.Provider {
		candidate := *r
		candidate.prov = nil
		refreshErr := candidate.configure(r.ov)
		if refreshErr == nil {
			refreshErr = candidate.authErr
		}
		if refreshErr != nil {
			if candidate.prov != nil {
				candidate.prov.CloseIdle()
			}
			if r.prov != nil {
				r.prov.CloseIdle()
			}
			r.prov = nil
			r.authErr = refreshErr
		} else {
			r.adoptConfiguration(&candidate)
		}
		r.publishLive()
	}
	if !result.Removed {
		if selected.Kind == provider.CredentialOAuth {
			printStatusText(out, fmt.Sprintf("No stored OAuth credential for %s was found.", spec.Name))
		} else {
			printStatusText(out, fmt.Sprintf("No stored API key for %s was found.", spec.Name))
		}
		r.restoreAuthState()
		r.draw(console)
		return
	}
	if selected.Kind == provider.CredentialOAuth {
		printLogoutNotice(out, terminal.NoticeInfo, fmt.Sprintf("Removed stored OAuth credential for %s.\n%s", spec.Name, logoutRevocationText(result.Revocation)))
	} else {
		printLogoutNotice(out, terminal.NoticeInfo, fmt.Sprintf("Removed stored API key for %s.\nEnvironment variables and models.json config are unchanged.", spec.Name))
	}
	r.restoreAuthState()
	r.draw(console)
}

// The usual one-line status clips a second outcome on narrow terminals.
func printLogoutNotice(out io.Writer, kind terminal.NoticeKind, text string) {
	if sink, ok := out.(interface {
		AppendDocumentBlock(terminal.DocumentBlock) error
	}); ok {
		_ = sink.AppendDocumentBlock(terminal.DocumentBlock{Kind: terminal.DocumentNotice, NoticeKind: kind, Text: text})
		return
	}
	fmt.Fprintln(out, terminal.Sanitize(text))
}

func (r *runtime) logoutHTTPClient(selected provider.StoredCredential) *http.Client {
	if selected.Kind != provider.CredentialOAuth {
		return nil
	}
	switch selected.ProviderID {
	case "openai-codex":
		return oauthHTTPClient(r.codexTransport)
	case "openai":
		return oauthHTTPClient(r.openAITransport)
	default:
		return nil
	}
}

func logoutKindName(kind provider.CredentialKind) string {
	if kind == provider.CredentialOAuth {
		return "OAuth credential"
	}
	return "API key"
}

func logoutRevocationText(outcome provider.RevocationOutcome) string {
	switch outcome {
	case provider.RevocationAcceptedRefresh:
		return "OpenAI accepted the refresh-token revocation request."
	case provider.RevocationAcceptedAccess:
		return "OpenAI accepted the access-token revocation request."
	case provider.RevocationNoUsableToken:
		return "No usable token; no revocation request was sent."
	case provider.RevocationNotAttempted:
		return "Revocation not attempted."
	default:
		return "Revocation unconfirmed."
	}
}
