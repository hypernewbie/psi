package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
)

// providerSet is one conversation's model clients. A conversation owns its
// clients so request session IDs and idle state never cross conversations.
type providerSet struct {
	main      provider.Provider
	model     provider.ModelMeta
	compactor provider.Provider
	compact   provider.ModelMeta
}

func (p *providerSet) close() {
	if p.main != nil {
		p.main.CloseIdle()
	}
	if p.compactor != nil {
		p.compactor.CloseIdle()
	}
}

// resolveModel selects provider and model metadata through Psi's catalogue.
func resolveModel(models *config.ModelsFile, ref string) (provider.ModelMeta, config.ModelMeta, string, error) {
	providerID, modelID, err := splitModelRef(ref)
	if err != nil {
		return provider.ModelMeta{}, config.ModelMeta{}, "", err
	}
	meta, err := models.SelectModel(providerID, modelID)
	if err != nil {
		return provider.ModelMeta{}, config.ModelMeta{}, "", fmt.Errorf("model metadata is unavailable")
	}
	return providerModel(providerID, meta), meta, providerID, nil
}

// providerModel mirrors the existing Psi conversion from catalogue metadata.
// src: internal/app/app.go:1033 (providerModel). Keep this in sync!
func providerModel(providerID string, meta config.ModelMeta) provider.ModelMeta {
	forceAdaptive, _, _ := meta.Compat.Bool("forceAdaptiveThinking")
	midConvoEffort, _, _ := meta.Compat.Bool("supportsMidConvoEffort")
	allowEmptySignature, _, _ := meta.Compat.Bool("allowEmptySignature")
	return provider.ModelMeta{
		ID: meta.ID, API: meta.API, Provider: providerID, BaseURL: meta.BaseURL,
		MaxTokens: meta.MaxTokens, ContextWindow: meta.ContextWindow, Reasoning: meta.Reasoning,
		InputImage: meta.InputImage, ThinkingLevelMap: meta.ThinkingLevelMap,
		AdaptiveThinking: forceAdaptive, MidConvoEffort: midConvoEffort,
		AllowEmptySignature: allowEmptySignature, Compat: meta.Compat, Custom: meta.Custom,
	}
}

// apiKeyPolicy mirrors Psi's key-presence rule for custom and overridden
// endpoints. A key that is required by the endpoint is never silently omitted.
// src: internal/app/helpers.go:97 (apiKeyPolicy). Keep this in sync!
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

// src: internal/app/helpers.go:117 (missingAPIKey). Keep this in sync!
func missingAPIKey(err error) bool {
	var missing *provider.MissingAPIKeyError
	return errors.As(err, &missing)
}

// buildProvider creates one model client from Psi catalogue metadata and the
// existing credentials. API-key modes are supported; OAuth-only account modes
// stay a later addition. This is a subset of Psi's client-construction block in
// internal/app/app.go:1085 (configure): the API-key cases only, no OAuth or
// subscription clients. Keep this in sync with that block when it changes!
func buildProvider(models *config.ModelsFile, agentDir, providerID string, meta provider.ModelMeta, catalogue config.ModelMeta, maxRetries int, sessionID string) (provider.Provider, error) {
	headers := map[string]string{}
	for name, value := range catalogue.Headers {
		resolved, err := config.ResolveCredential(value)
		if err != nil {
			return nil, fmt.Errorf("provider header credentials are unavailable")
		}
		headers[name] = resolved
	}
	authPath := filepath.Join(agentDir, "auth.json")
	configured := models.Providers[providerID]
	keyInput, allowMissing := apiKeyPolicy(providerID, catalogue, configured)
	resolveAPIKey := func() (string, error) {
		key, _, err := provider.SelectProviderAPIKeyInput(envMap(), authPath, providerID, keyInput)
		if allowMissing && missingAPIKey(err) {
			return "", nil
		}
		return key, err
	}
	key, authErr := resolveAPIKey()
	if authErr != nil && !(allowMissing && missingAPIKey(authErr)) {
		// The credential resolver can include the auth file path in its error.
		// Keep the startup diagnostic to the provider and failure class.
		return nil, fmt.Errorf("provider credentials are unavailable")
	}
	applyAuthHeader := func(headers map[string]string) error {
		if !catalogue.AuthHeader {
			return nil
		}
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("provider requires an API key for its Authorization header")
		}
		headers["Authorization"] = "Bearer " + key
		return nil
	}
	idle := 5 * time.Minute
	var client provider.Provider
	switch meta.API {
	case "anthropic-messages":
		if err := applyAuthHeader(headers); err != nil {
			return nil, err
		}
		c := provider.NewClient(meta.BaseURL, key, maxRetries, idle)
		c.Headers, c.SessionID = headers, sessionID
		client = c
	case "openai-completions":
		if err := applyAuthHeader(headers); err != nil {
			return nil, err
		}
		c := provider.NewCompletionsClient(meta.BaseURL, key, maxRetries, idle)
		c.Headers, c.SessionID = headers, sessionID
		client = c
	case "openai-responses":
		if providerID == "openai" && !meta.Custom {
			return nil, fmt.Errorf("provider requires an API key in models.json; subscription sign-in is not supported")
		}
		if err := applyAuthHeader(headers); err != nil {
			return nil, err
		}
		c := provider.NewResponsesClient(meta.BaseURL, key, maxRetries, idle)
		c.Headers, c.SessionID = headers, sessionID
		client = c
	case "google-generative-ai":
		if err := applyAuthHeader(headers); err != nil {
			return nil, err
		}
		c := provider.NewGoogleClient(meta.BaseURL, key, maxRetries, idle)
		c.Headers, c.SessionID = headers, sessionID
		client = c
	case "mistral-conversations":
		if err := applyAuthHeader(headers); err != nil {
			return nil, err
		}
		c := provider.NewMistralClient(meta.BaseURL, key, maxRetries, idle)
		c.Headers, c.SessionID = headers, sessionID
		client = c
	case "openai-codex-responses":
		return nil, fmt.Errorf("OAuth sign-in is not supported; use API-key mode")
	default:
		return nil, fmt.Errorf("provider API is unsupported")
	}
	return client, nil
}

// src: internal/app/app.go:1246 (envMap). Keep this in sync!
func envMap() map[string]string {
	out := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

// newProviderSet resolves and builds the primary and optional compaction model.
func newProviderSet(models *config.ModelsFile, agentDir, modelRef, compactionRef, sessionID string, maxRetries int) (*providerSet, error) {
	mainModel, mainCatalogue, providerID, err := resolveModel(models, modelRef)
	if err != nil {
		return nil, err
	}
	mainClient, err := buildProvider(models, agentDir, providerID, mainModel, mainCatalogue, maxRetries, sessionID)
	if err != nil {
		return nil, err
	}
	set := &providerSet{main: mainClient, model: mainModel}
	if compactionRef != "" {
		compactionModel, compactionCatalogue, compactorID, err := resolveModel(models, compactionRef)
		if err != nil {
			set.close()
			return nil, fmt.Errorf("compaction model: %w", err)
		}
		compactor, err := buildProvider(models, agentDir, compactorID, compactionModel, compactionCatalogue, maxRetries, sessionID)
		if err != nil {
			set.close()
			return nil, fmt.Errorf("compaction model: %w", err)
		}
		set.compactor, set.compact = compactor, compactionModel
	}
	return set, nil
}
