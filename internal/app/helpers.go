package app

import (
	"fmt"
	"os"
	"strings"

	"psi/internal/config"
	"psi/internal/session"
)

func modelList(r *runtime) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("current %s/%s thinking %s\n", r.ov.Provider, r.ov.Model, r.ov.Thinking))
	b.WriteString("supported (local, no catalog refresh):\n")
	b.WriteString("  anthropic (api anthropic-messages) [key: " + credAvail("anthropic") + "]\n")
	for _, id := range []string{"claude-sonnet-4-5", "claude-haiku-4-5", "claude-opus-4-1"} {
		mark := " "
		if r.ov.Provider == "anthropic" && r.ov.Model == id {
			mark = "*"
		}
		b.WriteString(fmt.Sprintf("  %s anthropic/%s\n", mark, id))
	}
	b.WriteString("  openai-codex (api openai-codex-responses) [oauth: " + credAvail("openai-codex") + "]\n")
	return strings.TrimSpace(b.String())
}

func credAvail(provider string) string {
	switch provider {
	case "anthropic":
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			return "env"
		}
		return "missing (ANTHROPIC_API_KEY or auth.json)"
	case "openai-codex":
		return "auth.json oauth (login remains Pi operation)"
	}
	return "unknown"
}

func continuable(proj *session.Projection) string {
	if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
		return "blocked: " + err.Error()
	}
	if len(session.UnresolvedToolCalls(proj.Flat)) > 0 {
		return "blocked: unresolved tool calls (inspect, then new session or copy)"
	}
	_ = config.ValidThinkingLevels
	return "ok"
}
