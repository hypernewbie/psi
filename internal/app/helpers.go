package app

import (
	"fmt"
	"math"
	"path/filepath"
	"time"

	"psi/internal/config"
	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/providerregistry"
	"psi/internal/session"
)

func modelSelector(r *runtime) *Selector {
	var items []session.SessionInfo
	for _, model := range r.usableModels() {
		m := model.option.Model
		items = append(items, session.SessionInfo{ID: model.provider + "/" + m.ID, FirstMessage: fmt.Sprintf("%s; %s, context %d, output %d", model.credential, m.API, m.ContextWindow, m.MaxTokens)})
	}
	selector := NewSelector(items, r.ov.Provider+"/"+r.ov.Model)
	selector.Models = true
	return selector
}

// usableModel is one model the session can use, with the credential detail
// shown for the selected row.
type usableModel struct {
	provider   string
	option     config.ModelOption
	credential string
}

// usableModels lists the models the session can use. A provider needs a
// usable credential, and each model must pass activation and request
// construction. The credentials come from one auth.json read, so a busy login
// file cannot stall the list once per provider.
func (r *runtime) usableModels() []usableModel {
	states := r.credentialStates()
	var models []usableModel
	for _, family := range providerregistry.IDs() {
		state := states[family]
		if !state.usable {
			continue
		}
		for _, option := range r.models.ModelOptions(family) {
			if !option.Available {
				continue
			}
			if _, err := provider.BuildRequest(provider.Request{Model: providerModel(family, option.Model), Thinking: "off"}); err != nil {
				continue
			}
			models = append(models, usableModel{provider: family, option: option, credential: state.detail})
		}
	}
	return models
}

// credentialState is one provider's credential as the UI describes it.
type credentialState struct {
	detail string
	usable bool
}

// credentialStates resolves every provider's credential from one auth.json
// snapshot.
func (r *runtime) credentialStates() map[string]credentialState {
	configured := make(map[string]string, len(r.models.Providers))
	for family, meta := range r.models.Providers {
		configured[family] = meta.APIKey
	}
	states := provider.Credentials(envMap(), filepath.Join(r.agentDir, "auth.json"), configured)
	out := make(map[string]credentialState, len(states))
	for family, state := range states {
		spec, _ := providerregistry.Lookup(family)
		switch spec.Auth {
		case providerregistry.AuthCodexOAuth:
			if state.Usable {
				out[family] = credentialState{detail: "OAuth login present (access checked on request)", usable: true}
				continue
			}
			out[family] = credentialState{detail: "OAuth login unavailable (run Pi /login)"}
		default:
			if state.Usable {
				out[family] = credentialState{detail: "credential available (" + state.Source + ")", usable: true}
				continue
			}
			reason := "credential unavailable"
			if state.Err != nil {
				reason += ": " + state.Err.Error()
			}
			out[family] = credentialState{detail: reason}
		}
	}
	return out
}

func thinkingSelector(r *runtime) *Selector {
	levels := provider.SupportedThinking(r.cfg.Model)
	items := make([]session.SessionInfo, 0, len(levels))
	for _, level := range levels {
		items = append(items, session.SessionInfo{ID: level, FirstMessage: provider.ThinkingDescription(r.cfg.Model, level)})
	}
	selector := NewSelector(items, r.ov.Thinking)
	selector.Thinking = true
	return selector
}

func continuable(proj *session.Projection) string {
	if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
		return "blocked: " + err.Error()
	}
	return "ok"
}

// compactionActivity returns Pi's compaction indicator text for a compaction
// phase. Pi distinguishes a manual compaction from an automatic one and names
// the overflow reason.
func formatRetryActivity(attempt, maximum int, delay time.Duration) string {
	duration := "unknown delay"
	if delay > 0 {
		if delay >= time.Second {
			duration = fmt.Sprintf("%.0fs", math.Ceil(delay.Seconds()))
		} else {
			duration = fmt.Sprintf("%dms", max(1, delay.Milliseconds()))
		}
	}
	return fmt.Sprintf("Retrying %d/%d · %s (Esc/Ctrl+C cancel)", attempt, maximum, duration)
}

func compactionActivity(state string) string {
	switch state {
	case event.CompactionThreshold, event.CompactionAdaptive, event.CompactionEndTurn:
		return "Auto-compacting… (Esc/Ctrl+C cancel)"
	case event.CompactionOverflow:
		return "Context overflow detected, Auto-compacting… (Esc/Ctrl+C cancel)"
	default:
		return "Compacting context… (Esc/Ctrl+C cancel)"
	}
}
