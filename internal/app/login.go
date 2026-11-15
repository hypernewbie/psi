package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/providerregistry"
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
		if ok && spec.Auth == providerregistry.AuthAPIKey && id != "anthropic" {
			ids = append(ids, id)
		}
	}
	return ids
}

func loginSelector(r *runtime) *Selector {
	ids := loginProviderIDs()
	items := make([]session.SessionInfo, 0, len(ids))
	for _, id := range ids {
		spec, _ := providerregistry.Lookup(id)
		parts := []string{spec.Name, spec.Environment}
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

func logoutSelector(providers []string, activeID string) *Selector {
	items := make([]session.SessionInfo, 0, len(providers))
	for _, id := range providers {
		spec, ok := providerregistry.Lookup(id)
		if !ok {
			continue
		}
		items = append(items, session.SessionInfo{ID: id, FirstMessage: spec.Name + " · " + spec.Environment})
	}
	selector := NewSelector(items, activeID)
	selector.AuthMode = AuthLogout
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
		return fmt.Errorf("OpenAI Codex login is not available in psi; log in through Pi with /login openai-codex.")
	}
	spec, ok := providerregistry.Lookup(providerID)
	if !ok || spec.Auth != providerregistry.AuthAPIKey || providerID == "anthropic" {
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
	} else if r.authErr != nil {
		r.state = StateOffline
	} else {
		r.state = StateIdle
	}
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

func (r *runtime) finishLogin(console *terminal.Console, out io.Writer) {
	prompt := r.login
	if prompt == nil {
		return
	}
	key := strings.TrimSpace(console.EndSecret())
	console.Status("")
	r.login = nil
	authPath := filepath.Join(r.agentDir, "auth.json")
	if err := provider.SaveAPIKey(context.Background(), authPath, prompt.providerID, key); err != nil {
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
				if _, err := r.store.AppendModelChange(candidate.ov.Provider, candidate.ov.Model); err != nil {
					selectionErr = err
				}
			}
			if selectionErr != nil {
				if candidate.prov != nil {
					candidate.prov.CloseIdle()
				}
				status = fmt.Sprintf("Saved API key for %s, but could not select the default model: %v", prompt.providerName, selectionErr)
			} else {
				r.installConfiguration(&candidate)
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
			r.installConfiguration(&candidate)
			status = fmt.Sprintf("Saved API key for %s. Credentials saved to %s.", prompt.providerName, authPath)
		}
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
	providers, err := provider.StoredAPIKeyProviders(authPath)
	if err != nil {
		return err
	}
	if len(providers) == 0 {
		printStatusText(out, "No stored API keys to remove. /logout only removes keys from auth.json; environment variables and models.json config are unchanged.")
		r.draw(console)
		return nil
	}
	r.openAuthSelector(logoutSelector(providers, r.ov.Provider), console)
	return nil
}

func (r *runtime) finishLogout(providerID string, console *terminal.Console, out io.Writer) {
	r.selector = nil
	console.ClearPickerRows()
	r.selectorDraft, r.selectorCursor, r.selectorRestoreOnSuccess = "", 0, false
	console.Editor.Clear()
	r.restoreAuthState()

	spec, ok := providerregistry.Lookup(providerID)
	if !ok || spec.Auth != providerregistry.AuthAPIKey {
		printStatusText(out, fmt.Sprintf("Failed to remove API key for %s: unsupported provider", providerID))
		r.draw(console)
		return
	}
	removed, err := provider.RemoveAPIKey(context.Background(), filepath.Join(r.agentDir, "auth.json"), providerID)
	if err != nil {
		printStatusText(out, fmt.Sprintf("Failed to remove API key for %s: %v", spec.Name, err))
		r.restoreAuthState()
		r.draw(console)
		return
	}
	if providerID == r.ov.Provider {
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
			r.installConfiguration(&candidate)
		}
	}
	if removed {
		printStatusText(out, fmt.Sprintf("Removed stored API key for %s. Environment variables and models.json config are unchanged.", spec.Name))
	} else {
		printStatusText(out, fmt.Sprintf("No stored API key for %s was found.", spec.Name))
	}
	r.restoreAuthState()
	r.draw(console)
}
