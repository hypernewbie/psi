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
	for _, family := range providerregistry.IDs() {
		options, credential := r.usableModelOptions(family)
		for _, option := range options {
			m := option.Model
			items = append(items, session.SessionInfo{ID: family + "/" + m.ID, FirstMessage: fmt.Sprintf("%s; %s, context %d, output %d", credential, m.API, m.ContextWindow, m.MaxTokens)})
		}
	}
	selector := NewSelector(items, r.ov.Provider+"/"+r.ov.Model)
	selector.Models = true
	return selector
}

// usableModelOptions is the model-list rule shared by /model, the bridge, and
// RPC. A provider needs a usable credential, and each model must pass both
// activation and request construction. The checks are local; credential
// validity is still established by the request.
func (r *runtime) usableModelOptions(family string) ([]config.ModelOption, string) {
	credential, usable := r.credentialAvailability(family)
	if !usable {
		return nil, ""
	}
	var options []config.ModelOption
	for _, option := range r.models.ModelOptions(family) {
		if !option.Available {
			continue
		}
		if _, err := provider.BuildRequest(provider.Request{Model: providerModel(family, option.Model), Thinking: "off"}); err != nil {
			continue
		}
		options = append(options, option)
	}
	return options, credential
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

// credentialAvailability reports whether the provider has a usable credential
// and describes it for the selected-model detail row.
func (r *runtime) credentialAvailability(family string) (detail string, usable bool) {
	path := filepath.Join(r.agentDir, "auth.json")
	spec, ok := providerregistry.Lookup(family)
	if !ok {
		return "credential status unavailable: unknown provider", false
	}
	switch spec.Auth {
	case providerregistry.AuthCodexOAuth:
		auth := provider.CodexAuth{Path: path}
		if err := auth.Check(); err == nil {
			return "OAuth login present (access checked on request)", true
		}
		return "OAuth login unavailable (run Pi /login)", false
	case providerregistry.AuthAPIKey:
		configured := r.models.Providers[family].APIKey
		_, source, err := provider.SelectProviderAPIKey(envMap(), path, family, configured)
		if err != nil {
			return "credential unavailable: " + err.Error(), false
		}
		return "credential available (" + source + ")", true
	default:
		return "credential status unavailable: unsupported auth kind", false
	}
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
