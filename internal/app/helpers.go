package app

import (
	"fmt"
	"math"
	"path/filepath"
	"time"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/providerregistry"
	"psi/internal/session"
)

func modelSelector(r *runtime) *Selector {
	var items []session.SessionInfo
	for _, family := range providerregistry.IDs() {
		credential, usable := r.credentialAvailability(family)
		if !usable {
			// Like Pi, the picker offers only providers the session can use:
			// a missing credential would make every selection fail at request
			// time, so those models do not appear at all.
			continue
		}
		for _, option := range r.models.ModelOptions(family) {
			if !option.Available {
				continue
			}
			m := option.Model
			if _, err := provider.BuildRequest(provider.Request{Model: providerModel(family, m), Thinking: "off"}); err != nil {
				continue
			}
			items = append(items, session.SessionInfo{ID: family + "/" + m.ID, FirstMessage: fmt.Sprintf("%s; %s, context %d, output %d", credential, m.API, m.ContextWindow, m.MaxTokens)})
		}
	}
	selector := NewSelector(items, r.ov.Provider+"/"+r.ov.Model)
	selector.Models = true
	return selector
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
