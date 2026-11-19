package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- Fixtures ------------------------------------------------------------

type mdOption func(*modelsDevModel)

func intPointer(value int) *int           { return &value }
func floatPointer(value float64) *float64 { return &value }

func mdRow(id string, options ...mdOption) modelsDevModel {
	row := modelsDevModel{
		ID:         id,
		Type:       "chat",
		Name:       id,
		Modalities: modelsDevModalities{Input: []string{"text"}},
		Capabilities: modelsDevCapabilities{
			Tools: modelsDevToolCapability{Supported: true},
		},
		Limit: modelsDevLimits{Context: intPointer(128000), Output: intPointer(16384)},
		API:   map[string]modelsDevAPI{"chat-completions": {BaseURL: "https://example.invalid/v1"}},
	}
	for _, option := range options {
		option(&row)
	}
	return row
}

func withName(name string) mdOption {
	return func(row *modelsDevModel) { row.Name = name }
}

func withStatus(status string) mdOption {
	return func(row *modelsDevModel) { row.Status = status }
}

func withReasoning(efforts ...string) mdOption {
	return func(row *modelsDevModel) {
		row.Capabilities.Reasoning.Supported = true
		values := make([]*string, len(efforts))
		for index, effort := range efforts {
			effort := effort
			values[index] = &effort
		}
		row.Capabilities.Reasoning.Effort = values
	}
}

func withReasoningNoEffort() mdOption {
	return func(row *modelsDevModel) { row.Capabilities.Reasoning.Supported = true }
}

func withLimits(contextWindow, maxTokens int) mdOption {
	return func(row *modelsDevModel) {
		row.Limit = modelsDevLimits{Context: intPointer(contextWindow), Output: intPointer(maxTokens)}
	}
}

func withAPIs(apis map[string]modelsDevAPI) mdOption {
	return func(row *modelsDevModel) { row.API = apis }
}

func withCost(input, output, cacheRead, cacheWrite float64) mdOption {
	return func(row *modelsDevModel) {
		row.Cost = &modelsDevCost{Text: &struct {
			Input  *modelsDevModalityCost `json:"input"`
			Output *modelsDevModalityCost `json:"output"`
		}{
			Input:  &modelsDevModalityCost{Price: floatPointer(input), Cache: &modelsDevCachePrices{Read: floatPointer(cacheRead), Write: orderedPrice{Value: cacheWrite, Set: true}}},
			Output: &modelsDevModalityCost{Price: floatPointer(output)},
		}}
	}
}

func catalogWith(provider string, rows ...modelsDevModel) modelsDevCatalog {
	models := make(map[string]modelsDevModel, len(rows))
	for _, row := range rows {
		models[row.ID] = row
	}
	return modelsDevCatalog{provider: modelsDevProvider{Models: models}}
}

func buildRows(t *testing.T, catalog modelsDevCatalog, defs []*providerDef) map[string][]*catalogModel {
	t.Helper()
	rows, err := buildCatalog(catalog, openRouterListing{}, defs, false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	return rows
}

func rowByID(rows []*catalogModel, id string) *catalogModel {
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	return nil
}

func orRow(id string, tools bool, options ...func(*openRouterModel)) openRouterModel {
	row := openRouterModel{ID: id, Name: id, ContextLength: 100000}
	row.TopProvider.ContextLength = 100000
	row.TopProvider.MaxCompletionTokens = 10000
	if tools {
		row.SupportedParameters = []string{"tools"}
	}
	for _, option := range options {
		option(&row)
	}
	return row
}

func withORReasoning(mandatory bool, efforts ...string) func(*openRouterModel) {
	return func(row *openRouterModel) {
		row.SupportedParameters = append(row.SupportedParameters, "reasoning")
		row.Reasoning = &openRouterReasoning{Mandatory: mandatory}
		for _, effort := range efforts {
			effort := effort
			row.Reasoning.SupportedEfforts = append(row.Reasoning.SupportedEfforts, &effort)
		}
	}
}

// ---- Provider rules ------------------------------------------------------

func TestOpenAIAndCodexRules(t *testing.T) {
	responsesAPI := map[string]modelsDevAPI{"responses": {BaseURL: "https://api.openai.com/v1"}}
	openAI := catalogWith("openai",
		mdRow("gpt-6.1-sol", withReasoning("low", "medium", "high", "xhigh", "max"),
			withLimits(1050000, 128000),
			withAPIs(map[string]modelsDevAPI{
				"responses":        {BaseURL: "https://api.openai.com/v1"},
				"chat-completions": {BaseURL: "https://api.openai.com/v1"},
			})),
		mdRow("gpt-5.6", withReasoning("low", "high"), withAPIs(responsesAPI)),
		mdRow("gpt-5.6-sol", withReasoning("low", "high"), withAPIs(responsesAPI)),
		mdRow("gpt-4o-audio", withAPIs(map[string]modelsDevAPI{"chat-completions": {BaseURL: "https://api.openai.com/v1"}})),
	)
	rows := buildRows(t, openAI, []*providerDef{openAIProvider()})

	direct := rowByID(rows["openai"], "gpt-6.1-sol")
	if direct == nil {
		t.Fatal("direct gpt-6.1-sol is missing")
	}
	if direct.ContextWindow != 272000 || direct.MaxTokens != 128000 {
		t.Fatalf("direct gpt-6.1-sol limits = %d/%d, want 272000/128000", direct.ContextWindow, direct.MaxTokens)
	}
	if value, present := direct.ThinkingLevelMap["off"]; !present || value != nil {
		t.Fatalf("direct gpt-6.1-sol off = %v present=%v, want explicit null", value, present)
	}
	if enabled, present := compatBool(direct, "supportsExplicitPromptCacheMode"); !present || !enabled {
		t.Fatal("direct gpt-6.1-sol lacks explicit prompt-cache support")
	}
	if rowByID(rows["openai"], "gpt-5.6") != nil || rowByID(rows["openai"], "gpt-5.6-sol") == nil {
		t.Fatal("direct Responses must exclude gpt-5.6 but keep gpt-5.6-sol")
	}
	if rowByID(rows["openai"], "gpt-4o-audio") != nil {
		t.Fatal("Chat Completions-only model was admitted to the Responses provider")
	}
	if rowByID(rows["openai"], "gpt-5-chat-latest") == nil {
		t.Fatal("authored gpt-5-chat-latest is missing")
	}

	withPresent := catalogWith("openai", mdRow("gpt-5-chat-latest", withName("Upstream Chat"), withAPIs(map[string]modelsDevAPI{
		"responses": {BaseURL: "https://api.openai.com/v1"},
	})))
	preserved := buildRows(t, withPresent, []*providerDef{openAIProvider()})
	if got := rowByID(preserved["openai"], "gpt-5-chat-latest"); got == nil || got.Name != "Upstream Chat" {
		t.Fatalf("authored addition overwrote a present row: %+v", got)
	}

	codexRows := buildRows(t, modelsDevCatalog{}, []*providerDef{openAICodexProvider()})
	codex := rowByID(codexRows["openai-codex"], "gpt-6.1-sol")
	if codex == nil {
		t.Fatal("Codex gpt-6.1-sol is missing")
	}
	if codex.ContextWindow != 500000 {
		t.Fatalf("Codex gpt-6.1-sol context = %d, want 500000", codex.ContextWindow)
	}
	if value, present := codex.ThinkingLevelMap["off"]; !present || value != nil {
		t.Fatalf("Codex gpt-6.1-sol off = %v present=%v, want explicit null", value, present)
	}
	if value := codex.ThinkingLevelMap["minimal"]; value == nil || *value != "low" {
		t.Fatalf("Codex gpt-6.1-sol minimal = %v, want low", value)
	}
	if _, exists := rowByID(codexRows["openai-codex"], "gpt-5.6-luna").ThinkingLevelMap["off"]; exists {
		t.Fatal("direct Responses off override leaked into Codex GPT-5.6")
	}
}

func TestAnthropicFallbacksAndProviderAdmissions(t *testing.T) {
	anthropic := catalogWith("anthropic",
		mdRow("claude-sonnet-4-5", withStatus("deprecated"), withReasoningNoEffort(), withCost(3, 15, 0.3, 3.75)),
		mdRow("claude-opus-4-8", withReasoningNoEffort(), withCost(5, 25, 0.5, 6.25)),
		mdRow("claude-opus-5", withReasoning("low", "medium", "high"), withCost(5, 25, 0.5, 6.25)),
		mdRow("claude-fable-5-2", withName("Upstream Fable"), withReasoning("low", "medium", "high")),
	)
	rows := buildRows(t, anthropic, []*providerDef{anthropicProvider()})
	if rowByID(rows["anthropic"], "claude-sonnet-4-5") == nil {
		t.Fatal("deprecated direct Anthropic default was filtered out")
	}
	fable := rowByID(rows["anthropic"], "claude-fable-5-2")
	if fable == nil || fable.Name != "Upstream Fable" {
		t.Fatalf("authored Fable overwrote the source row: %+v", fable)
	}
	allowed, ok := fable.Compat["allowedFallbackModels"].([]fallbackModel)
	if !ok || len(allowed) != 1 || allowed[0].Model != "claude-opus-5" {
		t.Fatalf("Fable fallbacks = %+v, want only claude-opus-5", fable.Compat["allowedFallbackModels"])
	}
	if allowed[0].Cost.Input != 5 || allowed[0].Cost.Output != 25 || allowed[0].Cost.CacheRead != 0.5 || allowed[0].Cost.CacheWrite != 6.25 {
		t.Fatalf("fallback nested cost = %+v", allowed[0].Cost)
	}

	missingTarget := catalogWith("anthropic",
		mdRow("claude-fable-5-2", withReasoning("low", "high")),
		mdRow("claude-opus-4-8", withReasoningNoEffort()),
	)
	missingRows := buildRows(t, missingTarget, []*providerDef{anthropicProvider()})
	if _, present := rowByID(missingRows["anthropic"], "claude-fable-5-2").Compat["allowedFallbackModels"]; present {
		t.Fatal("fallback list was emitted without a compatible present target")
	}

	minimax := catalogWith("minimax",
		mdRow("MiniMax-M2.7", withStatus("deprecated"), withReasoningNoEffort()),
		mdRow("MiniMax-M2", withReasoningNoEffort()),
	)
	minimaxRows := buildRows(t, minimax, []*providerDef{minimaxProvider()})
	if rowByID(minimaxRows["minimax"], "MiniMax-M2.7") == nil {
		t.Fatal("deprecated MiniMax default was filtered out")
	}
	if rowByID(minimaxRows["minimax"], "MiniMax-M2") != nil {
		t.Fatal("hidden MiniMax model was admitted")
	}

	xiaomi := catalogWith("xiaomi",
		mdRow("mimo-old", withStatus("deprecated"), withReasoningNoEffort()),
		mdRow("mimo-live", withReasoningNoEffort()),
	)
	xiaomiRows := buildRows(t, xiaomi, []*providerDef{xiaomiProvider("xiaomi")})
	if rowByID(xiaomiRows["xiaomi"], "mimo-old") != nil || rowByID(xiaomiRows["xiaomi"], "mimo-live") == nil {
		t.Fatal("Xiaomi deprecated filter did not apply")
	}

	opencode := catalogWith("opencode-go",
		mdRow("old-model", withStatus("deprecated")),
		mdRow("live-model", withAPIs(map[string]modelsDevAPI{
			"messages": {BaseURL: "https://opencode.ai/inference/go/anthropic"},
		})),
	)
	opencodeRows := buildRows(t, opencode, []*providerDef{opencodeGoProvider()})
	if rowByID(opencodeRows["opencode-go"], "old-model") != nil || rowByID(opencodeRows["opencode-go"], "live-model") == nil {
		t.Fatal("OpenCode Go deprecated filter did not apply")
	}
}

func TestKimiCodingHighspeedPreservesExplicitStates(t *testing.T) {
	catalog := catalogWith("kimi-code-plan-global",
		mdRow("kimi-for-coding-highspeed", withReasoningNoEffort()),
	)
	rows := buildRows(t, catalog, []*providerDef{kimiCodingProvider()})
	row := rowByID(rows["kimi-coding"], "kimi-for-coding-highspeed")
	if row == nil {
		t.Fatal("highspeed row is missing")
	}
	if len(row.ThinkingLevelMap) != 1 {
		t.Fatalf("thinking map = %v, want only off", row.ThinkingLevelMap)
	}
	if value, present := row.ThinkingLevelMap["off"]; !present || value != nil {
		t.Fatal("off must be present and null")
	}
	enabled, present := compatBool(row, "supportsStrictMode")
	if !present || enabled {
		t.Fatal("supportsStrictMode must be present and false")
	}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"off":null`) || !strings.Contains(text, `"supportsStrictMode":false`) {
		t.Fatalf("explicit states were lost: %s", text)
	}
	if strings.Contains(text, `"low"`) {
		t.Fatalf("absent low level became explicit: %s", text)
	}
}

func TestOpenCodeGoRouting(t *testing.T) {
	catalog := catalogWith("opencode-go",
		mdRow("gpt-5.6-luna", withReasoning("low", "high"), withAPIs(map[string]modelsDevAPI{
			"chat-completions": {BaseURL: "https://opencode.ai/inference/go/openai/v1"},
			"messages":         {BaseURL: "https://opencode.ai/inference/go/anthropic"},
			"responses":        {BaseURL: "https://opencode.ai/inference/go/openai/v1"},
		})),
		mdRow("claude-sonnet-4-5", withReasoningNoEffort(), withAPIs(map[string]modelsDevAPI{
			"messages": {BaseURL: "https://opencode.ai/inference/go/anthropic/v1"},
		})),
		mdRow("qwen3.6-plus", withReasoningNoEffort(), withAPIs(map[string]modelsDevAPI{
			"messages": {BaseURL: "https://opencode.ai/inference/go/anthropic"},
		})),
	)
	rows := buildRows(t, catalog, []*providerDef{opencodeGoProvider()})

	native := rowByID(rows["opencode-go"], "gpt-5.6-luna")
	if native.API != "openai-responses" || native.BaseURL != "https://opencode.ai/inference/go/openai/v1" {
		t.Fatalf("native precedence = %s %s", native.API, native.BaseURL)
	}
	if got, _ := compatString(native, "sessionAffinityFormat"); got != "openai-nosession" {
		t.Fatalf("Responses route session affinity = %q", got)
	}
	messages := rowByID(rows["opencode-go"], "claude-sonnet-4-5")
	if messages.API != "anthropic-messages" || messages.BaseURL != "https://opencode.ai/inference/go/anthropic" {
		t.Fatalf("Messages suffix normalization = %s %s", messages.API, messages.BaseURL)
	}
	if messages.ContextWindow != 200000 {
		t.Fatalf("Sonnet 4.5 context = %d, want 200000", messages.ContextWindow)
	}
	qwen := rowByID(rows["opencode-go"], "qwen3.6-plus")
	if qwen.API != "openai-completions" || qwen.BaseURL != "https://opencode.ai/inference/go/openai/v1" {
		t.Fatalf("Qwen reroute = %s %s", qwen.API, qwen.BaseURL)
	}
	if got, _ := compatString(qwen, "thinkingFormat"); got != "qwen" {
		t.Fatalf("Qwen thinking format = %q", got)
	}

	unapproved := catalogWith("opencode-go", mdRow("bad-route", withAPIs(map[string]modelsDevAPI{
		"messages": {BaseURL: "https://attacker.example/v1"},
	})))
	if _, err := buildCatalog(unapproved, openRouterListing{}, []*providerDef{opencodeGoProvider()}, false); err == nil ||
		!strings.Contains(err.Error(), "outside the reviewed OpenCode allowlist") {
		t.Fatalf("unapproved endpoint error = %v", err)
	}

	legacyMessages := catalogWith("opencode-go", mdRow("qwen3.6-plus", withAPIs(map[string]modelsDevAPI{
		"messages": {BaseURL: "https://opencode.ai/inference/go/v1"},
	})))
	if _, err := buildCatalog(legacyMessages, openRouterListing{}, []*providerDef{opencodeGoProvider()}, false); err == nil ||
		!strings.Contains(err.Error(), "outside the reviewed OpenCode allowlist") {
		t.Fatalf("reroute accepted a Messages path without the /anthropic suffix: %v", err)
	}

	badCompat := catalogWith("opencode-go", mdRow("kimi-k2.6", withReasoningNoEffort(), withAPIs(map[string]modelsDevAPI{
		"chat-completions": {BaseURL: "https://opencode.ai/inference/go/openai/v1"},
	})))
	if _, err := buildCatalog(badCompat, openRouterListing{}, []*providerDef{opencodeGoProvider()}, false); err == nil ||
		!strings.Contains(err.Error(), "supportsLongCacheRetention") {
		t.Fatalf("unsupported adapter field error = %v", err)
	}
}

func TestOpenRouterSelection(t *testing.T) {
	listing := openRouterListing{Data: []openRouterModel{
		orRow("anthropic/claude-sonnet-4.5", true, withORReasoning(false, "none", "low", "high")),
		orRow("anthropic/claude-sonnet-4.5:batch", true),
		orRow("google/gemini-test", true, withORReasoning(true, "low", "high")),
		orRow("openai/gpt-4o", true),
		orRow("no-tools/model", false),
	}}
	rows, err := buildCatalog(modelsDevCatalog{}, listing, []*providerDef{openRouterProvider()}, false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	openRouter := rows["openrouter"]
	if len(openRouter) != 5 {
		t.Fatalf("rows = %d, want batch, google, openai, auto, fusion", len(openRouter))
	}
	if rowByID(openRouter, "anthropic/claude-sonnet-4.5") != nil {
		t.Fatal("non-batch Anthropic Messages row was not excluded")
	}
	batch := rowByID(openRouter, "anthropic/claude-sonnet-4.5:batch")
	if batch == nil || batch.API != "openai-completions" {
		t.Fatalf("batch Anthropic row = %+v", batch)
	}
	if got, _ := compatString(batch, "cacheControlFormat"); got != "anthropic" {
		t.Fatalf("batch cache control = %q", got)
	}
	mandatory := rowByID(openRouter, "google/gemini-test")
	if mandatory == nil || !mandatory.Reasoning {
		t.Fatalf("mandatory reasoning capability missing: %+v", mandatory)
	}
	if value, present := mandatory.ThinkingLevelMap["off"]; !present || value != nil {
		t.Fatal("mandatory reasoner off must be explicit null")
	}
	if rowByID(openRouter, "no-tools/model") != nil {
		t.Fatal("tool-less OpenRouter model was admitted")
	}
	if auto := rowByID(openRouter, "auto"); auto == nil || auto.ContextWindow != 2000000 {
		t.Fatalf("authored auto alias = %+v", auto)
	}
	if fusion := rowByID(openRouter, "openrouter/fusion"); fusion == nil || fusion.ContextWindow != 1000000 {
		t.Fatalf("authored fusion alias = %+v", fusion)
	}
	if _, present := mandatory.Compat["supportsDeveloperRole"]; !present {
		t.Fatal("non-OpenAI/Anthropic OpenRouter row must set supportsDeveloperRole false")
	}
	if _, present := rowByID(openRouter, "openai/gpt-4o").Compat["supportsDeveloperRole"]; present {
		t.Fatal("OpenAI OpenRouter row must keep the developer-role default")
	}
}

func TestGoogleAliasAndMistralEffort(t *testing.T) {
	google := catalogWith("google",
		mdRow("gemini-flash-latest", withName("Flash Latest"), withAPIs(map[string]modelsDevAPI{
			"generate-content": {BaseURL: "https://generativelanguage.googleapis.com/v1beta"},
		})),
		mdRow("gemini-3.5-flash", withReasoning("low", "medium", "high"), withLimits(500000, 65536),
			withAPIs(map[string]modelsDevAPI{"generate-content": {BaseURL: "https://generativelanguage.googleapis.com/v1beta"}})),
		mdRow("gemma-4-test", withReasoningNoEffort(),
			withAPIs(map[string]modelsDevAPI{"generate-content": {BaseURL: "https://generativelanguage.googleapis.com/v1beta"}})),
	)
	googleRows := buildRows(t, google, []*providerDef{googleProvider()})
	alias := rowByID(googleRows["google"], "gemini-flash-latest")
	if alias == nil || alias.Name != "Flash Latest" || alias.ContextWindow != 500000 || alias.MaxTokens != 65536 || !alias.Reasoning {
		t.Fatalf("alias metadata = %+v", alias)
	}
	if value := alias.ThinkingLevelMap["low"]; value == nil || *value != "low" {
		t.Fatalf("alias thinking map = %v", alias.ThinkingLevelMap)
	}
	gemma := rowByID(googleRows["google"], "gemma-4-test")
	if value := gemma.ThinkingLevelMap["minimal"]; value == nil || *value != "MINIMAL" {
		t.Fatalf("Gemma minimal = %v", gemma.ThinkingLevelMap["minimal"])
	}
	if value := gemma.ThinkingLevelMap["high"]; value == nil || *value != "HIGH" {
		t.Fatalf("Gemma high = %v", gemma.ThinkingLevelMap["high"])
	}

	mistral := catalogWith("mistral",
		mdRow("mistral-effort", withReasoning("none", "high")),
		mdRow("magistral-test", withReasoningNoEffort()),
	)
	mistralRows := buildRows(t, mistral, []*providerDef{mistralProvider()})
	effort := rowByID(mistralRows["mistral"], "mistral-effort")
	if value := effort.ThinkingLevelMap["off"]; value == nil || *value != "none" {
		t.Fatalf("Mistral off = %v", effort.ThinkingLevelMap["off"])
	}
	if value := effort.ThinkingLevelMap["high"]; value == nil || *value != "high" {
		t.Fatalf("Mistral high = %v", effort.ThinkingLevelMap["high"])
	}
	if rowByID(mistralRows["mistral"], "magistral-test").ThinkingLevelMap != nil {
		t.Fatal("Mistral reasoning model without effort got a generic map")
	}
}

// ---- Failure, replay, and publishing ------------------------------------

func TestBakeRejectsIncompleteOrConflictingInputs(t *testing.T) {
	partial := modelsDevCatalog{
		"anthropic": {Models: map[string]modelsDevModel{"row": mdRow("row")}},
		"openai":    {Models: map[string]modelsDevModel{}},
	}
	if _, err := buildCatalog(partial, openRouterListing{}, providerDefinitions(), true); err == nil ||
		!strings.Contains(err.Error(), `models.dev source "openai"`) {
		t.Fatalf("missing bucket error = %v", err)
	}

	// A bucket can exist but contain no eligible source rows. OpenAI's authored
	// alias must not turn that into a successful full bake.
	for _, bucket := range requiredModelsDevSources {
		partial[bucket] = catalogWith(bucket, mdRow("row"))[bucket]
	}
	listing := openRouterListing{Data: []openRouterModel{orRow("example/model", true)}}
	if _, err := buildCatalog(partial, listing, providerDefinitions(), true); err == nil ||
		!strings.Contains(err.Error(), `models.dev source "openai" has no eligible models`) {
		t.Fatalf("empty eligible OpenAI source error = %v", err)
	}

	duplicates := modelsDevCatalog{"groq": {Models: map[string]modelsDevModel{
		"first":  mdRow("same-id", withName("First")),
		"second": mdRow("same-id", withName("Second")),
	}}}
	if _, err := buildCatalog(duplicates, openRouterListing{}, []*providerDef{groqProvider()}, false); err == nil ||
		!strings.Contains(err.Error(), "conflicting duplicate") {
		t.Fatalf("conflicting duplicate error = %v", err)
	}

	duplicates["groq"].Models["first"] = mdRow("same-id", withReasoning("low"))
	duplicates["groq"].Models["second"] = mdRow("same-id", withReasoning("high"))
	if _, err := buildCatalog(duplicates, openRouterListing{}, []*providerDef{groqProvider()}, false); err == nil ||
		!strings.Contains(err.Error(), "conflicting duplicate") {
		t.Fatalf("conflicting duplicate effort error = %v", err)
	}

	identical := modelsDevCatalog{"groq": {Models: map[string]modelsDevModel{
		"first":  mdRow("same-id", withReasoning("low", "high")),
		"second": mdRow("same-id", withReasoning("high", "low")),
	}}}
	rows, err := buildCatalog(identical, openRouterListing{}, []*providerDef{groqProvider()}, false)
	if err != nil {
		t.Fatalf("identical duplicates failed: %v", err)
	}
	if len(rows["groq"]) != 1 {
		t.Fatalf("identical duplicates produced %d rows", len(rows["groq"]))
	}

	negative := catalogWith("groq", func() modelsDevModel {
		row := mdRow("negative")
		row.Limit.Context = intPointer(-1)
		return row
	}())
	if _, err := buildCatalog(negative, openRouterListing{}, []*providerDef{groqProvider()}, false); err == nil ||
		!strings.Contains(err.Error(), "negative context limit") {
		t.Fatalf("negative limit error = %v", err)
	}
}

func TestOpenRouterRejectsNegativeLimits(t *testing.T) {
	for field, corrupt := range map[string]func(*openRouterModel){
		"context_length":                     func(m *openRouterModel) { m.ContextLength = -1 },
		"top_provider.context_length":        func(m *openRouterModel) { m.TopProvider.ContextLength = -1 },
		"top_provider.max_completion_tokens": func(m *openRouterModel) { m.TopProvider.MaxCompletionTokens = -1 },
	} {
		t.Run(field, func(t *testing.T) {
			listing := openRouterListing{Data: []openRouterModel{orRow("moonshotai/kimi-k2.5", true, corrupt)}}
			_, err := buildCatalog(nil, listing, []*providerDef{openRouterProvider()}, false)
			if err == nil || !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), "moonshotai/kimi-k2.5") {
				t.Fatalf("negative OpenRouter limit error = %v", err)
			}
		})
	}
}

func TestDecodeRejectsWrongShapeAndTrailingData(t *testing.T) {
	inputs := fetchedInputs{
		modelsDevBody:       []byte(`{"groq":{"models":{"m":{"id":"m","type":"chat","limit":{"context":"big"}}}}}`),
		modelsDevFetchedAt:  time.Unix(1_700_000_000, 0),
		openRouterBody:      []byte(`{"data":[]}`),
		openRouterFetchedAt: time.Unix(1_700_000_000, 0),
	}
	if _, _, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, func() time.Time { return time.Unix(1_700_000_100, 0) }); err == nil {
		t.Fatal("wrong limit type was accepted")
	}
	inputs.modelsDevBody = []byte(`{"groq":{"models":{}}} {}`)
	if _, _, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, func() time.Time { return time.Unix(1_700_000_100, 0) }); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func TestOfflineReplayIsByteIdentical(t *testing.T) {
	modelsDevBody, err := json.Marshal(catalogWith("groq",
		mdRow("openai/gpt-oss-20b", withReasoning("low", "medium", "high"), withLimits(131072, 65536)),
	))
	if err != nil {
		t.Fatal(err)
	}
	openRouterBody := []byte(`{"data":[]}`)
	fetchedAt := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	inputs := fetchedInputs{
		modelsDevBody:       modelsDevBody,
		modelsDevFetchedAt:  fetchedAt,
		openRouterBody:      openRouterBody,
		openRouterFetchedAt: fetchedAt,
	}
	generatedAt := fetchedAt.Add(time.Minute)
	fixedNow := func() time.Time { return generatedAt }
	firstCatalog, firstSource, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	secondCatalog, secondSource, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstCatalog, secondCatalog) || !bytes.Equal(firstSource, secondSource) {
		t.Fatal("identical inputs and provenance produced different bytes")
	}
	var provenance catalogProvenance
	if err := json.Unmarshal(firstSource, &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance.CatalogSHA256 != sha256Hex(firstCatalog) {
		t.Fatal("catalog digest does not match the catalog bytes")
	}
	if provenance.Inputs[modelsDevInputName].SHA256 != sha256Hex(modelsDevBody) {
		t.Fatal("models.dev digest does not match the input bytes")
	}
	if provenance.GeneratedAt != generatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("generatedAt = %s", provenance.GeneratedAt)
	}
	backward := fetchedAt.Add(-time.Minute)
	if _, _, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, func() time.Time { return backward }); err == nil {
		t.Fatal("generatedAt earlier than fetchedAt was accepted")
	}
}

func TestDestinationRefusalKeepsSentinel(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(parent, "existing")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(out, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	fetched := false
	err := bake(context.Background(), out, bakeDeps{
		fetch: func(context.Context) (fetchedInputs, error) {
			fetched = true
			return fetchedInputs{}, errors.New("fetch must not run")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want existing destination refusal", err)
	}
	if fetched {
		t.Fatal("fetch ran before the destination check")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "keep" {
		t.Fatalf("sentinel changed: %q %v", data, err)
	}
}

func TestPublishFailureLeavesNoOutput(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(parent, "target")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(out, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := publish(context.Background(), out, map[string][]byte{
		"catalog.json":        []byte("catalog"),
		"catalog-source.json": []byte("source"),
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want existing destination refusal", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "target" {
		t.Fatalf("parent entries = %v, want only the sentinel target", entries)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("sentinel changed: %q %v", data, err)
	}
}

func TestBakeSecondFetchFailurePublishesNothing(t *testing.T) {
	out := filepath.Join(t.TempDir(), "not-created")
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "api.json") {
			return response(http.StatusOK, `{"anthropic":{"models":{}}}`, nil), nil
		}
		return response(http.StatusServiceUnavailable, "", nil), nil
	})}
	var waits []time.Duration
	err := bake(context.Background(), out, bakeDeps{
		fetch: func(ctx context.Context) (fetchedInputs, error) {
			results, err := fetchTargets(ctx, testFetchConfig(client, &waits), []fetchTarget{
				{name: modelsDevInputName, url: modelsDevURL, validate: validateModelsDevJSON},
				{name: openRouterInputName, url: openRouterURL, validate: validateOpenRouterBody},
			})
			if err != nil {
				return fetchedInputs{}, err
			}
			return fetchedInputs{
				modelsDevBody:       results[modelsDevInputName].body,
				modelsDevFetchedAt:  results[modelsDevInputName].fetchedAt,
				openRouterBody:      results[openRouterInputName].body,
				openRouterFetchedAt: results[openRouterInputName].fetchedAt,
			}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "openrouter") || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("error = %v, want source-specific second fetch failure", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output directory exists after failure: %v", statErr)
	}
}
