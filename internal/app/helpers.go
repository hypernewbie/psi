package app

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/providerregistry"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/terminal"
)

func modelSelector(r *runtime) *Selector {
	selector := NewSelector(modelSelectorItems(r), r.ov.Provider+"/"+r.ov.Model)
	selector.Models = true
	return selector
}

func modelSelectorItems(r *runtime) []session.SessionInfo {
	var items []session.SessionInfo
	for _, model := range r.usableModels() {
		m := model.option.Model
		items = append(items, session.SessionInfo{ID: model.provider + "/" + m.ID, FirstMessage: fmt.Sprintf("%s; %s, context %d, output %d", model.credential, m.API, m.ContextWindow, m.MaxTokens)})
	}
	return items
}

// usableModel is one model the session can use, with the credential detail
// shown for the selected row.
type usableModel struct {
	provider   string
	option     config.ModelOption
	credential string
}

// usableModels lists the models the session can use. Each model must pass
// activation and request construction, and its credential policy decides
// whether a resolved key is required. The credentials come from one auth.json
// read, so a busy login file cannot stall the list once per provider. Models in
// one provider can use different policies, so each distinct provider/policy
// pair resolves once.
func (r *runtime) usableModels() []usableModel {
	type keyMode struct {
		provider string
		strict   bool
	}
	type candidate struct {
		provider     string
		option       config.ModelOption
		mode         keyMode
		allowMissing bool
	}
	var requests []provider.CredentialRequest
	requestIndex := map[keyMode]int{}
	var candidates []candidate
	for _, family := range r.providerIDs() {
		configured := r.models.Providers[family]
		for _, option := range r.models.ModelOptions(family) {
			if !option.Available {
				continue
			}
			input, allowMissing := apiKeyPolicy(family, option.Model, configured)
			mode := keyMode{provider: family, strict: input.Strict}
			if _, seen := requestIndex[mode]; !seen {
				requestIndex[mode] = len(requests)
				requests = append(requests, provider.CredentialRequest{ProviderID: family, Key: input})
			}
			candidates = append(candidates, candidate{provider: family, option: option, mode: mode, allowMissing: allowMissing})
		}
	}
	states := provider.CredentialsBatch(envMap(), filepath.Join(r.agentDir, "auth.json"), requests)
	var models []usableModel
	for _, candidate := range candidates {
		state := states[requestIndex[candidate.mode]]
		detail, usable := credentialDetail(candidate.provider, candidate.option.Model, state, candidate.allowMissing)
		if !usable {
			continue
		}
		if _, err := provider.BuildRequest(provider.Request{Model: providerModel(candidate.provider, candidate.option.Model), Thinking: "off"}); err != nil {
			continue
		}
		models = append(models, usableModel{provider: candidate.provider, option: candidate.option, credential: detail})
	}
	return models
}

// apiKeyPolicy returns the models.json key input for one model and whether
// genuine credential absence can run it. Custom providers may omit a key for
// the three generic APIs that can legitimately need none. A built-in openai or
// anthropic model may omit a key only when a user baseUrl selects a valid,
// non-default route. authHeader always requires a key.
func apiKeyPolicy(providerID string, meta config.ModelMeta, configured config.ProviderMeta) (provider.APIKeyInput, bool) {
	strict := false
	switch {
	case meta.Custom:
		switch meta.API {
		case "openai-completions", "openai-responses", "anthropic-messages":
			strict = true
		}
	case meta.BaseURLOverride && providerID == "openai" && meta.API == "openai-responses",
		meta.BaseURLOverride && providerID == "anthropic" && meta.API == "anthropic-messages":
		strict = provider.IsNonDefaultEndpoint(providerID, meta.API, meta.BaseURL)
	}
	input := provider.APIKeyInput{
		Value:   configured.APIKey,
		Present: configured.APIKey != "" || (strict && configured.APIKeyPresent),
		Strict:  strict,
	}
	return input, strict && !meta.AuthHeader && !configured.AuthHeader
}

func missingAPIKey(err error) bool {
	var missing *provider.MissingAPIKeyError
	return errors.As(err, &missing)
}

// providerIDs returns the effective provider set: the built-in providers plus
// the providers models.json declares.
func (r *runtime) providerIDs() []string {
	if r.models == nil {
		return providerregistry.IDs()
	}
	return r.models.ProviderIDs()
}

// refreshModelsForPicker re-reads models.json before the model picker opens,
// the way Pi reloads the model configuration when the picker appears. A
// malformed file keeps the current models and reports the error.
func (r *runtime) refreshModelsForPicker(notice func(terminal.NoticeKind, string)) {
	if notice == nil {
		return
	}
	if err := r.reloadModels(); err != nil {
		notice(terminal.NoticeError, "models.json error: "+err.Error())
	}
}

// credentialDetail describes one provider's credential state for the model
// picker. allowMissing is true only for a model whose policy accepts genuine
// absence; an invalid credential still describes its error. A ChatGPT OAuth
// login is usable only on the exact official OpenAI route, the same check the
// request time resolver applies.
func credentialDetail(family string, meta config.ModelMeta, state provider.CredentialState, allowMissing bool) (string, bool) {
	spec, builtin := providerregistry.Lookup(family)
	if !builtin {
		return apiKeyCredentialDetail(state, allowMissing)
	}
	switch spec.Auth {
	case providerregistry.AuthCodexOAuth:
		if state.Usable {
			return "OAuth login present (access checked on request)", true
		}
		return "OAuth login unavailable (run Pi /login)", false
	case providerregistry.AuthAPIKeyOrOAuth:
		if state.OAuth && !provider.IsOfficialOpenAIEndpoint(meta.BaseURL) {
			return "OAuth login requires the official OpenAI endpoint", false
		}
		return apiKeyCredentialDetail(state, allowMissing)
	default:
		return apiKeyCredentialDetail(state, allowMissing)
	}
}

func apiKeyCredentialDetail(state provider.CredentialState, allowMissing bool) (string, bool) {
	if state.Usable {
		return "credential available (" + state.Source + ")", true
	}
	if allowMissing && missingAPIKey(state.Err) {
		return "no API key configured", true
	}
	reason := "credential unavailable"
	if state.Err != nil {
		reason += ": " + state.Err.Error()
	}
	return reason, false
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

// formatRetryActivity returns Pi's RetryStatusIndicator text for the start
// of a retry: the backoff as a countdown in seconds with the default
// interrupt binding.
func formatRetryActivity(attempt, maximum int, delay time.Duration) string {
	return formatRetryCountdown(attempt, maximum, int(math.Ceil(delay.Seconds())))
}

// formatRetryCountdown is Pi's retry line with seconds left in the wait. Pi's
// CountdownTimer shows zero once the wait is over.
func formatRetryCountdown(attempt, maximum, seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("Retrying (%d/%d) in %ds... (escape to cancel)", attempt, maximum, seconds)
}

// retryIndicator is the state of Pi's RetryStatusIndicator while a backoff
// wait runs.
type retryIndicator struct {
	attempt, maximum int
	delay            time.Duration
	started          time.Time
}

// text is the retry line at now. Pi's CountdownTimer starts at the rounded-up
// wait and drops one second at each full second, then stays at zero until the
// retried response replaces the indicator.
func (c *retryIndicator) text(now time.Time) string {
	seconds := int(math.Ceil(c.delay.Seconds())) - int(now.Sub(c.started)/time.Second)
	return formatRetryCountdown(c.attempt, c.maximum, seconds)
}

// compactionActivity returns Pi's compaction indicator text. Pi distinguishes
// a manual compaction from an automatic one and names the overflow reason.
func compactionActivity(state string) string {
	switch state {
	case event.CompactionThreshold, event.CompactionAdaptive, event.CompactionEndTurn:
		return "Auto-compacting... (escape to cancel)"
	case event.CompactionOverflow:
		return "Context overflow detected, Auto-compacting... (escape to cancel)"
	default:
		return "Compacting context... (escape to cancel)"
	}
}
