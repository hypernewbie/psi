package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---- Fixtures ------------------------------------------------------------

type mdOption func(*modelsDevModel)

func intPointer(value int) *int { return &value }

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

func withInputLimit(value string) mdOption {
	return func(row *modelsDevModel) { row.Limit.Input = json.RawMessage(value) }
}

func withAPIs(apis map[string]modelsDevAPI) mdOption {
	return func(row *modelsDevModel) { row.API = apis }
}

func withCost(input, output, cacheRead, cacheWrite float64) mdOption {
	return func(row *modelsDevModel) {
		row.Cost = json.RawMessage(fmt.Sprintf(`{"text":{"input":{"price":%g,"cache":{"read":%g,"write":%g}},"output":{"price":%g}}}`, input, cacheRead, cacheWrite, output))
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
	rows, err := buildCatalog(catalog, openRouterListing{}, defs, false, nil)
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
		mdRow("gpt-5-pro", withLimits(400000, 272000), withAPIs(responsesAPI)),
		mdRow("gpt-4o-audio", withAPIs(map[string]modelsDevAPI{"chat-completions": {BaseURL: "https://api.openai.com/v1"}})),
	)
	rows := buildRows(t, openAI, []*providerDef{openAIProvider()})

	direct := rowByID(rows["openai"], "gpt-6.1-sol")
	if direct == nil {
		t.Fatal("direct gpt-6.1-sol is missing")
	}
	if direct.ContextWindow != 922000 || direct.MaxTokens != 128000 {
		t.Fatalf("direct gpt-6.1-sol limits = %d/%d, want 922000/128000", direct.ContextWindow, direct.MaxTokens)
	}
	if value, present := direct.ThinkingLevelMap["off"]; !present || value != nil {
		t.Fatalf("direct gpt-6.1-sol off = %v present=%v, want explicit null", value, present)
	}
	if enabled, present := compatBool(direct, "supportsExplicitPromptCacheMode"); !present || !enabled {
		t.Fatal("direct gpt-6.1-sol lacks explicit prompt-cache support")
	}
	gpt62 := catalogWith("openai", mdRow("gpt-6.2-sol", withReasoning("low", "medium", "high", "xhigh", "max"), withLimits(1050000, 128000), withAPIs(responsesAPI)))
	gpt62Rows := buildRows(t, gpt62, []*providerDef{openAIProvider()})
	direct62 := rowByID(gpt62Rows["openai"], "gpt-6.2-sol")
	if direct62 == nil || direct62.ContextWindow != 922000 || direct62.MaxTokens != 128000 {
		t.Fatalf("direct gpt-6.2-sol = %+v", direct62)
	}
	if value, present := direct62.ThinkingLevelMap["off"]; !present || value != nil {
		t.Fatalf("direct gpt-6.2-sol off = %v present=%v, want explicit null", value, present)
	}
	for _, field := range []string{"supportsMidConvoEffort", "supportsMidConvoSystemMessages", "supportsToolSearch"} {
		if enabled, present := compatBool(direct62, field); !present || !enabled {
			t.Fatalf("direct gpt-6.2-sol lacks %s", field)
		}
	}
	if rowByID(rows["openai"], "gpt-5.6") != nil || rowByID(rows["openai"], "gpt-5.6-sol") == nil {
		t.Fatal("direct Responses must exclude gpt-5.6 but keep gpt-5.6-sol")
	}
	if rowByID(rows["openai"], "gpt-4o-audio") != nil {
		t.Fatal("Chat Completions-only model was admitted to the Responses provider")
	}
	if pro := rowByID(rows["openai"], "gpt-5-pro"); pro == nil || pro.ContextWindow != 400000 || pro.MaxTokens != 272000 {
		t.Fatalf("direct gpt-5-pro limits = %+v, want 400000/272000", pro)
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
	codex62 := rowByID(codexRows["openai-codex"], "gpt-6.2-astra")
	if codex62 == nil || codex62.ContextWindow != 272000 || codex62.MaxTokens != 128000 {
		t.Fatalf("Codex gpt-6.2-astra = %+v", codex62)
	}
	if enabled, present := compatBool(codex62, "supportsMidConvoEffort"); !present || !enabled {
		t.Fatal("Codex gpt-6.2-astra lacks mid-conversation effort support")
	}
	codexSol := rowByID(codexRows["openai-codex"], "gpt-6.2-sol")
	if codexSol == nil || codexSol.ContextWindow != 500000 || codexSol.MaxTokens != 128000 {
		t.Fatalf("Codex gpt-6.2-sol = %+v", codexSol)
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

func TestClaudeMidConversationEffortPreservesHaikuOff(t *testing.T) {
	for _, id := range []string{"claude-haiku-5-5", "claude-haiku-5.5-20261001"} {
		t.Run(id, func(t *testing.T) {
			rows := buildRows(t, catalogWith("anthropic", mdRow(id, withReasoningNoEffort())), []*providerDef{anthropicProvider()})
			row := rowByID(rows["anthropic"], id)
			if enabled, _ := compatBool(row, "supportsMidConvoEffort"); !enabled {
				t.Fatal("Haiku lost mid-conversation effort support")
			}
			if value, present := row.ThinkingLevelMap["off"]; present && value == nil {
				t.Fatal("the catalog disables Haiku's off level")
			}
		})
	}
}

func TestModelsDevCostOnlyAffectsConsumers(t *testing.T) {
	const correct = `{"text":{"input":{"price":2,"cache":{"write":{"5m":0,"1h":5}}},"output":{"price":10}}}`
	const explicit = `{"text":{"input":{"price":2,"cache":{"write":null,"explicit":{"write":2.5}}},"output":{"price":10}}}`
	const malformed = `{"text":{"input":{"price":2,"cache":{"write":{"5m":"invalid","1h":5}}},"output":{"price":10}}}`
	for _, tc := range []struct {
		name, provider, cost string
		def                  *providerDef
		wantCache, wantError bool
	}{
		{"first TTL price wins even if zero", "openai", correct, openAIProvider(), false, false},
		{"null write falls back to explicit price", "openai", explicit, openAIProvider(), true, false},
		{"invalid consumed price fails", "openai", malformed, openAIProvider(), false, true},
		{"unused cost shape is ignored", "groq", `[]`, groqProvider(), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var model modelsDevModel
			if err := json.Unmarshal([]byte(`{"id":"fixture","type":"chat","capabilities":{"tools":{"supported":true}},"api":{"responses":{}},"cost":`+tc.cost+`}`), &model); err != nil {
				t.Fatalf("decode catalog row: %v", err)
			}
			rows, err := buildCatalog(catalogWith(tc.provider, model), openRouterListing{}, []*providerDef{tc.def}, false, nil)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), tc.provider+"/fixture") || !strings.Contains(err.Error(), "cost") {
					t.Fatalf("invalid cost error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			row := rowByID(rows[tc.provider], "fixture")
			if row == nil {
				t.Fatal("fixture row missing")
			}
			if got, _ := compatBool(row, "supportsExplicitPromptCacheMode"); got != tc.wantCache {
				t.Fatalf("explicit cache support = %v, want %v", got, tc.wantCache)
			}
		})
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
		mdRow("hy3", withReasoning("low", "high"), withLimits(256000, 128000), withAPIs(map[string]modelsDevAPI{
			"chat-completions": {BaseURL: "https://opencode.ai/inference/go/openai/v1"},
		})),
	)
	rows := buildRows(t, catalog, []*providerDef{opencodeGoProvider()})

	native := rowByID(rows["opencode-go"], "gpt-5.6-luna")
	if native.API != "openai-responses" || native.BaseURL != "https://opencode.ai/inference/go/openai/v1" {
		t.Fatalf("native precedence = %s %s", native.API, native.BaseURL)
	}
	if native.ContextWindow != 922000 {
		t.Fatalf("gpt-5.6-luna context = %d, want 922000", native.ContextWindow)
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
	hy3 := rowByID(rows["opencode-go"], "hy3")
	if hy3.ContextWindow != 192000 {
		t.Fatalf("hy3 context = %d, want 192000 (max input)", hy3.ContextWindow)
	}

	unapproved := catalogWith("opencode-go", mdRow("bad-route", withAPIs(map[string]modelsDevAPI{
		"messages": {BaseURL: "https://attacker.example/v1"},
	})))
	if _, err := buildCatalog(unapproved, openRouterListing{}, []*providerDef{opencodeGoProvider()}, false, nil); err == nil ||
		!strings.Contains(err.Error(), "outside the reviewed OpenCode allowlist") {
		t.Fatalf("unapproved endpoint error = %v", err)
	}

	legacyMessages := catalogWith("opencode-go", mdRow("qwen3.6-plus", withAPIs(map[string]modelsDevAPI{
		"messages": {BaseURL: "https://opencode.ai/inference/go/v1"},
	})))
	if _, err := buildCatalog(legacyMessages, openRouterListing{}, []*providerDef{opencodeGoProvider()}, false, nil); err == nil ||
		!strings.Contains(err.Error(), "outside the reviewed OpenCode allowlist") {
		t.Fatalf("reroute accepted a Messages path without the /anthropic suffix: %v", err)
	}

	badCompat := catalogWith("opencode-go", mdRow("kimi-k2.6", withReasoningNoEffort(), withAPIs(map[string]modelsDevAPI{
		"chat-completions": {BaseURL: "https://opencode.ai/inference/go/openai/v1"},
	})))
	if _, err := buildCatalog(badCompat, openRouterListing{}, []*providerDef{opencodeGoProvider()}, false, nil); err == nil ||
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
	rows, err := buildCatalog(modelsDevCatalog{}, listing, []*providerDef{openRouterProvider()}, false, nil)
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
	if _, err := buildCatalog(partial, openRouterListing{}, providerDefinitions(), true, nil); err == nil ||
		!strings.Contains(err.Error(), `models.dev source "openai"`) {
		t.Fatalf("missing bucket error = %v", err)
	}

	// A bucket can exist but contain no eligible source rows. OpenAI's authored
	// alias must not turn that into a successful full bake.
	for _, bucket := range requiredModelsDevSources {
		partial[bucket] = catalogWith(bucket, mdRow("row"))[bucket]
	}
	listing := openRouterListing{Data: []openRouterModel{orRow("example/model", true)}}
	if _, err := buildCatalog(partial, listing, providerDefinitions(), true, nil); err == nil ||
		!strings.Contains(err.Error(), `models.dev source "openai" has no eligible models`) {
		t.Fatalf("empty eligible OpenAI source error = %v", err)
	}

	duplicates := modelsDevCatalog{"groq": {Models: map[string]modelsDevModel{
		"first":  mdRow("same-id", withName("First")),
		"second": mdRow("same-id", withName("Second")),
	}}}
	if _, err := buildCatalog(duplicates, openRouterListing{}, []*providerDef{groqProvider()}, false, nil); err == nil ||
		!strings.Contains(err.Error(), "conflicting duplicate") {
		t.Fatalf("conflicting duplicate error = %v", err)
	}

	openAIResponses := map[string]modelsDevAPI{"responses": {BaseURL: "https://api.openai.com/v1"}}
	openAIDuplicates := modelsDevCatalog{"openai": {Models: map[string]modelsDevModel{
		"first":  mdRow("gpt-6.1-sol", withName("First"), withLimits(1050000, 64000), withAPIs(openAIResponses)),
		"second": mdRow("gpt-6.1-sol", withName("Second"), withLimits(1050000, 64000), withAPIs(openAIResponses)),
	}}}
	if _, err := buildCatalog(openAIDuplicates, openRouterListing{}, []*providerDef{openAIProvider()}, false, nil); err == nil ||
		!strings.Contains(err.Error(), "conflicting duplicate") {
		t.Fatalf("reviewed OpenAI duplicate with differing names error = %v", err)
	}

	duplicates["groq"].Models["first"] = mdRow("same-id", withReasoning("low"))
	duplicates["groq"].Models["second"] = mdRow("same-id", withReasoning("high"))
	if _, err := buildCatalog(duplicates, openRouterListing{}, []*providerDef{groqProvider()}, false, nil); err == nil ||
		!strings.Contains(err.Error(), "conflicting duplicate") {
		t.Fatalf("conflicting duplicate effort error = %v", err)
	}

	identical := modelsDevCatalog{"groq": {Models: map[string]modelsDevModel{
		"first":  mdRow("same-id", withReasoning("low", "high")),
		"second": mdRow("same-id", withReasoning("high", "low")),
	}}}
	rows, err := buildCatalog(identical, openRouterListing{}, []*providerDef{groqProvider()}, false, nil)
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
	if _, err := buildCatalog(negative, openRouterListing{}, []*providerDef{groqProvider()}, false, nil); err == nil ||
		!strings.Contains(err.Error(), "negative context limit") {
		t.Fatalf("negative limit error = %v", err)
	}
}

func reviewedProjectionModel(t *testing.T, providerID, modelID, contextLimit, inputValue string, includeInput bool) *catalogModel {
	t.Helper()
	var limitFields []string
	if contextLimit != "" {
		limitFields = append(limitFields, `"context":`+contextLimit)
	}
	if includeInput {
		limitFields = append(limitFields, `"input":`+inputValue)
	}
	limitJSON := "{" + strings.Join(limitFields, ",") + "}"
	body := []byte(fmt.Sprintf(
		`{"%s":{"models":{"fixture":{"id":%q,"type":"chat","limit":%s,"capabilities":{"tools":{"supported":true}}}}}}`,
		providerID, modelID, limitJSON,
	))
	if err := validateModelsDevJSON(body); err != nil {
		t.Fatalf("validate source JSON: %v", err)
	}
	var catalog modelsDevCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatalf("decode source JSON: %v", err)
	}
	sources, err := modelsDevChatModels(catalog, providerID)
	if err != nil {
		t.Fatalf("normalize source row: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("normalized source rows = %d, want 1", len(sources))
	}
	var definition *providerDef
	switch providerID {
	case "openai":
		definition = openAIProvider()
	case "opencode-go":
		definition = opencodeGoProvider()
	default:
		t.Fatalf("test provider %q has no projection definition", providerID)
	}
	return newCatalogModel(definition, sources[0])
}

func TestReviewedSourceInputProjection(t *testing.T) {
	for _, tc := range []struct {
		name         string
		input        string
		includeInput bool
		want         int
	}{
		{name: "valid smaller input", input: "900000", includeInput: true, want: 900000},
		{name: "absent", want: 1050000},
		{name: "null", input: "null", includeInput: true, want: 1050000},
		{name: "zero", input: "0", includeInput: true, want: 1050000},
		{name: "negative", input: "-1", includeInput: true, want: 1050000},
		{name: "wrong type", input: `"large"`, includeInput: true, want: 1050000},
		{name: "fractional", input: "900000.5", includeInput: true, want: 1050000},
		{name: "overflowing", input: "18446744073709551616", includeInput: true, want: 1050000},
		{name: "equal to context", input: "1050000", includeInput: true, want: 1050000},
		{name: "greater than context", input: "1050001", includeInput: true, want: 1050000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := reviewedProjectionModel(t, "openai", "gpt-6.1-sol", "1050000", tc.input, tc.includeInput)
			if model.ContextWindow != tc.want {
				t.Fatalf("context = %d, want %d", model.ContextWindow, tc.want)
			}
		})
	}

	withoutContext := reviewedProjectionModel(t, "openai", "gpt-6.1-sol", "", "2048", true)
	if withoutContext.ContextWindow != 4096 {
		t.Fatalf("context without a source limit = %d, want existing default 4096", withoutContext.ContextWindow)
	}

	for _, tc := range []struct {
		provider, model string
		context         string
		want            int
	}{
		{"openai", "gpt-5-pro", "400000", 400000},
		{"openai", "gpt-5.3-codex-spark", "400000", 400000},
		{"opencode-go", "hy4", "300000", 300000},
		{"opencode-go", "hy4-flash-free", "300000", 300000},
		{"opencode-go", "gpt-6.1-sol", "300000", 300000},
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			model := reviewedProjectionModel(t, tc.provider, tc.model, tc.context, "100000", true)
			if model.ContextWindow != tc.want {
				t.Fatalf("context = %d, want source context %d", model.ContextWindow, tc.want)
			}
		})
	}

	wrongTypeOnExcludedRow := reviewedProjectionModel(t, "openai", "gpt-5-pro", "400000", `"large"`, true)
	if wrongTypeOnExcludedRow.ContextWindow != 400000 {
		t.Fatalf("excluded row context = %d, want source context 400000", wrongTypeOnExcludedRow.ContextWindow)
	}
}

func TestBakeLimitOverrides(t *testing.T) {
	modelsDev := make(modelsDevCatalog, len(requiredModelsDevSources))
	for _, source := range requiredModelsDevSources {
		modelsDev[source] = catalogWith(source, mdRow("row"))[source]
	}

	responsesAPI := map[string]modelsDevAPI{"responses": {BaseURL: "https://api.openai.com/v1"}}
	modelsDev["openai"] = modelsDevProvider{Models: map[string]modelsDevModel{
		"first":  mdRow("gpt-6.1-sol", withLimits(1050000, 64000), withInputLimit("900000"), withAPIs(responsesAPI)),
		"second": mdRow("gpt-6.1-sol", withLimits(1000000, 96000), withInputLimit("880000"), withAPIs(responsesAPI)),
	}}
	openCodeAPI := map[string]modelsDevAPI{"chat-completions": {BaseURL: "https://opencode.ai/inference/go/openai/v1"}}
	modelsDev["opencode-go"] = catalogWith("opencode-go",
		mdRow("hy3", withLimits(256000, 64000), withAPIs(openCodeAPI)),
		mdRow("gpt-6-luna", withStatus("deprecated"), withAPIs(map[string]modelsDevAPI{
			"responses": {BaseURL: "https://opencode.ai/inference/go/openai/v1"},
		})),
	)["opencode-go"]
	openRouter := openRouterListing{Data: []openRouterModel{orRow("example/model", true)}}

	modelsDevBody, err := json.Marshal(modelsDev)
	if err != nil {
		t.Fatalf("marshal models.dev fixture: %v", err)
	}
	openRouterBody, err := json.Marshal(openRouter)
	if err != nil {
		t.Fatalf("marshal OpenRouter fixture: %v", err)
	}
	fetchedAt := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var warnings bytes.Buffer
	out := filepath.Join(t.TempDir(), "candidate")
	err = bake(context.Background(), out, bakeDeps{
		fetch: func(context.Context) (fetchedInputs, error) {
			return fetchedInputs{
				modelsDevBody:       modelsDevBody,
				modelsDevFetchedAt:  fetchedAt,
				openRouterBody:      openRouterBody,
				openRouterFetchedAt: fetchedAt,
			}, nil
		},
		now:      func() time.Time { return fetchedAt.Add(time.Minute) },
		warnings: &warnings,
	})
	if err != nil {
		t.Fatalf("bake failed: %v", err)
	}

	catalogBytes, err := os.ReadFile(filepath.Join(out, "catalog.json"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	var rows map[string][]*catalogModel
	if err := json.Unmarshal(catalogBytes, &rows); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	var openAIModel *catalogModel
	openAICount := 0
	for _, model := range rows["openai"] {
		if model.ID == "gpt-6.1-sol" {
			openAIModel = model
			openAICount++
		}
	}
	if openAICount != 1 || openAIModel == nil || openAIModel.ContextWindow != 922000 || openAIModel.MaxTokens != 128000 {
		t.Fatalf("openai/gpt-6.1-sol rows=%d model=%+v, want one row at 922000/128000", openAICount, openAIModel)
	}
	hy3 := rowByID(rows["opencode-go"], "hy3")
	if hy3 == nil || hy3.ContextWindow != 192000 || hy3.MaxTokens != 128000 {
		t.Fatalf("opencode-go/hy3 = %+v, want 192000/128000", hy3)
	}
	if rowByID(rows["opencode-go"], "gpt-6-luna") != nil {
		t.Fatal("deprecated opencode-go/gpt-6-luna was admitted")
	}

	sourceBytes, err := os.ReadFile(filepath.Join(out, "catalog-source.json"))
	if err != nil {
		t.Fatalf("read catalog provenance: %v", err)
	}
	var provenance catalogProvenance
	if err := json.Unmarshal(sourceBytes, &provenance); err != nil {
		t.Fatalf("decode catalog provenance: %v", err)
	}
	if provenance.CatalogSHA256 != sha256Hex(catalogBytes) {
		t.Fatalf("catalog digest = %s, want %s", provenance.CatalogSHA256, sha256Hex(catalogBytes))
	}

	present := map[string]bool{"openai/gpt-6.1-sol": true, "opencode-go/hy3": true}
	var expectedKeys []string
	for providerID, models := range reviewedLimitOverrides {
		for modelID := range models {
			key := providerID + "/" + modelID
			if !present[key] {
				expectedKeys = append(expectedKeys, key)
			}
		}
	}
	sort.Strings(expectedKeys)
	warningLines := strings.Split(strings.TrimSuffix(warnings.String(), "\n"), "\n")
	if len(warningLines) != len(expectedKeys) {
		t.Fatalf("warning lines=%d, expected %d missing-target warnings: %q", len(warningLines), len(expectedKeys), warnings.String())
	}
	for index, key := range expectedKeys {
		want := "catalog-bake: warning: limit override skipped for " + key + ": model not in candidate catalog"
		if warningLines[index] != want {
			t.Errorf("warning[%d] = %q, want %q", index, warningLines[index], want)
		}
	}
}

func TestLimitOverrideAfterFinalizer(t *testing.T) {
	definition := openAIProvider()
	definition.finalizeAll = func(models []*catalogModel) {
		for _, model := range models {
			if model.ID == "gpt-6.1-sol" {
				model.ContextWindow = 0
				model.MaxTokens = 0
			}
		}
	}
	var warnings bytes.Buffer
	rows, err := buildCatalog(catalogWith("openai", mdRow("gpt-6.1-sol", withAPIs(map[string]modelsDevAPI{
		"responses": {BaseURL: "https://api.openai.com/v1"},
	}))), openRouterListing{}, []*providerDef{definition}, false, &warnings)
	if err != nil {
		t.Fatalf("partial build failed: %v", err)
	}
	model := rowByID(rows["openai"], "gpt-6.1-sol")
	if model == nil || model.ContextWindow != 922000 || model.MaxTokens != 128000 {
		t.Fatalf("finalized openai/gpt-6.1-sol = %+v, want 922000/128000", model)
	}
	if warnings.Len() != 0 {
		t.Fatalf("partial build emitted missing-target warnings: %q", warnings.String())
	}
}

func TestCatalogLimitOverridesMatchBundle(t *testing.T) {
	data, err := os.ReadFile("../../internal/config/data/catalog.json")
	if err != nil {
		t.Fatalf("read bundled catalog: %v", err)
	}
	var bundle map[string][]*catalogModel
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatalf("decode bundled catalog: %v", err)
	}
	compared, tableSize := 0, 0
	for providerID, overrides := range reviewedLimitOverrides {
		tableSize += len(overrides)
		for modelID, want := range overrides {
			model := rowByID(bundle[providerID], modelID)
			if model == nil {
				continue
			}
			compared++
			if model.ContextWindow != want.contextWindow || model.MaxTokens != want.maxTokens {
				t.Errorf("bundled %s/%s limits = %d/%d, want %d/%d", providerID, modelID, model.ContextWindow, model.MaxTokens, want.contextWindow, want.maxTokens)
			}
		}
	}
	t.Logf("reviewed limit overrides compared: %d of %d", compared, tableSize)
}

func TestOpenRouterRejectsNegativeLimits(t *testing.T) {
	for field, corrupt := range map[string]func(*openRouterModel){
		"context_length":                     func(m *openRouterModel) { m.ContextLength = -1 },
		"top_provider.context_length":        func(m *openRouterModel) { m.TopProvider.ContextLength = -1 },
		"top_provider.max_completion_tokens": func(m *openRouterModel) { m.TopProvider.MaxCompletionTokens = -1 },
	} {
		t.Run(field, func(t *testing.T) {
			listing := openRouterListing{Data: []openRouterModel{orRow("moonshotai/kimi-k2.5", true, corrupt)}}
			_, err := buildCatalog(nil, listing, []*providerDef{openRouterProvider()}, false, nil)
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
	if _, _, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, func() time.Time { return time.Unix(1_700_000_100, 0) }, nil); err == nil {
		t.Fatal("wrong limit type was accepted")
	}
	inputs.modelsDevBody = []byte(`{"groq":{"models":{}}} {}`)
	if _, _, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, func() time.Time { return time.Unix(1_700_000_100, 0) }, nil); err == nil {
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
	firstCatalog, firstSource, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, fixedNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondCatalog, secondSource, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, fixedNow, nil)
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
	if _, _, err := assembleArtifacts(inputs, []*providerDef{groqProvider()}, false, func() time.Time { return backward }, nil); err == nil {
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
