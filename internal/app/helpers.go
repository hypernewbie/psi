package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"psi/internal/provider"
	"psi/internal/session"
)

func modelSelector(r *runtime) *Selector {
	var items []session.SessionInfo
	for _, family := range []string{"anthropic", "openai-codex"} {
		availability := r.credentialAvailability(family)
		for _, m := range r.models.LocalModels(family) {
			items = append(items, session.SessionInfo{ID: family + "/" + m.ID, FirstMessage: fmt.Sprintf("%s; %s, context %d, output %d", availability, m.API, m.ContextWindow, m.MaxTokens)})
		}
	}
	selector := NewSelector(items, r.ov.Provider+"/"+r.ov.Model)
	selector.Models = true
	return selector
}

func modelList(r *runtime) string {
	return fmt.Sprintf("current %s/%s thinking %s\n%s", r.ov.Provider, r.ov.Model, provider.ThinkingDescription(r.cfg.Model, r.ov.Thinking), strings.Join(modelSelector(r).Render(), "\n"))
}

func (r *runtime) credentialAvailability(family string) string {
	path := filepath.Join(r.agentDir, "auth.json")
	switch family {
	case "anthropic":
		if _, _, err := provider.SelectAPIKey(envMap(), path); err == nil {
			return "credential available"
		}
		return "credential unavailable (environment or auth.json)"
	case "openai-codex":
		auth := provider.CodexAuth{Path: path}
		if err := auth.Check(); err == nil {
			return "OAuth login present (access checked on request)"
		}
		return "OAuth login unavailable (run Pi /login)"
	}
	return "credential status unknown"
}

func continuable(proj *session.Projection) string {
	if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
		return "blocked: " + err.Error()
	}
	if len(session.UnresolvedToolCalls(proj.Flat)) > 0 {
		return "blocked: unresolved tool calls (inspect, then new session or copy)"
	}
	return "ok"
}
