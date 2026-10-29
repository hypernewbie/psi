# PLAN6 acceptance ledger

## Scope and references

- Plan: `temp/PLAN6.md`; it remains an untracked planning input.
- Pi source: `temp/pi-mono` revision `d5b3de22d0e24a3e9c38a9410d3329b099717283`.
- Public catalog package: installed `@earendil-works/pi-ai` 1.4.0; catalog manifest generated `2026-10-21T22:33:24.994Z`.
- Pi dynamic model cache: read the provider/model metadata schema and summarized the five requested providers from `~/.pi/agent/models-store.json`. The real cache was not copied into the repository or tests. Tests use synthetic cache entries. The sanitized endpoint profile included OpenCode paths `/inference/go/anthropic`, `/inference/go/google/v1beta`, and `/inference/go/openai/v1` for the applicable APIs.
- The checked-in public catalog source hashes and exact model counts are in `internal/config/data/catalog-source.json`; generated model data contains no credentials or headers.
- No live provider request has run.

## Checkpoints

| Slice | Status | Evidence |
|---|---|---|
| 1. Registry, catalog, and auth | Complete locally | P6-001–012: explicit registry, seven public catalogs, model API/endpoint and compat metadata, Pi cache policy, truthful unavailable model options, and provider-specific API-key precedence. Evidence: `go test ./internal/modelmeta ./internal/providerregistry ./internal/config ./internal/provider ./internal/app -count=1`; generator check below. |
| 2. MiniMax and OpenCode Anthropic | Complete locally | P6-013–018: provider-specific keys/endpoints, `/anthropic/v1/messages` path, adaptive-thinking and `allowEmptySignature`, case-insensitive OpenCode session-header precedence, tool/image translation, text/thinking streams, partial-stream settlement, and restarted-session replay. Evidence below. |
| 3. OpenAI Chat Completions | Complete locally | P6-019–030: dedicated bounded client, Pi-compatible history/tool/image translation and call-ID linking, model-specific compatibility fields, DeepSeek/Z.AI controls, OpenCode reasoning normalization, usage/finish parsing, retry/cancel/malformed-stream settlement. Unsupported mid-conversation system/tool-addition modes remain visible as unavailable. Evidence below. |
| 4. Google Generative AI | Complete locally | P6-031–040: separate REST/SSE client, SDK-shaped `generationConfig` wire body, API-key header and route, system/user/image/tool conversion, model-specific IDs and grouped function responses, signature replay, thinking budgets/maps/clamping/off, usage and finish parsing, retries/cancellation, safe partial settlement, and OpenCode Go Google routing/session affinity. Evidence below. Pi pins SDK 2.21.0; an executable version-exact SDK comparison is not claimed. No live provider request ran. |
| 5. OpenCode Responses | Not started | — |
| 6. Integration, safety, and release | Not started | — |

### Slice 1 evidence

- `node --check scripts/snapshot-models.mjs` passed.
- Regenerated `catalog.json` and `catalog-source.json` from installed public `@earendil-works/pi-ai` 1.4.0; both matched the checked-in files byte-for-byte.
- A synthetic package with a non-public name was rejected by the generator.
- Registry tests assert exactly seven provider IDs and the allowed API pairs. Cache tests cover missing, malformed, stale, oversized, valid multi-provider, unsupported-API, malicious metadata, and OpenCode endpoint cases.
- Credential tests cover stored key → `models.json` → exact provider environment variable for each new API-key provider, and fail-closed unsupported stored auth and command credentials.
- `go test ./... -count=1` passed after the slice 1 changes.

### Slice 2 evidence

- `internal/provider/anthropic_compat_test.go` uses loopback HTTP only. It checks MiniMax and OpenCode path/key/API version, streamed text and signed thinking, OpenCode session header default and case-insensitive explicit override, images, grouped multiple tool results, and that incomplete tool streams are not retried or dispatched.
- `internal/config/catalog_test.go` checks the pinned MiniMax adaptive-thinking and OpenCode `allowEmptySignature` fields, including an absent-field case. `TestAnthropicAdaptiveThinkingUsesModelEffortMap` checks effort mapping and `off: null` behavior.
- `internal/app/provider_config_test.go` checks selected provider credentials and a synthetic newer Pi-cache OpenCode endpoint through runtime configuration.
- `internal/agent/anthropic_replay_test.go` reopens synthetic v3 sessions for both providers, continues through the request builder, and checks thinking signatures and tool/result linkage.
- `go test ./... -count=1` passed after slice 2. No live provider requests ran.

### Slice 3 evidence

- `internal/provider/completions_request_test.go` covers DeepSeek `max_tokens`, tool schema, images, thinking enable/off and effort, assistant `reasoning_content`, Z.AI `clear_thinking`/effort/`tool_stream`, OpenCode `reasoning` normalization, compatibility flags, and rejection of unknown or unsupported compatibility fields.
- `internal/provider/completions_test.go` and `completions_edge_test.go` use loopback servers. They cover the OpenCode route and session-header precedence, interleaved tool indexes/IDs, fragmented arguments, response/model IDs, usage-only chunks, `stop`/`length`/`tool_calls` finish reasons, malformed/truncated streams, no-finish compatibility, retry-before-stream, no retry after stream start, cancellation, missing-key refusal, and secret-safe HTTP errors.
- `internal/agent/completions_cycle_test.go` completes a synthetic DeepSeek reasoning/tool/read/result/final-answer cycle and checks that reasoning and tool results are replayed on the following request. `completions_replay_test.go` reopens sessions for DeepSeek and OpenCode and checks cross-API tool-ID normalization and linked results.
- `internal/app/provider_config_test.go` verifies selected DeepSeek key/API/client construction. `internal/app/completions_test.go` checks that catalog models have a request path or a clear picker-unavailable reason, and that an imported unsupported model leaves history readable.
- `go test ./... -count=1` passed after slice 3. No live provider requests ran.

G6-01, G6-02, and G6-03 are met by local synthetic evidence. Remaining gates stay open until their listed A6 cases have evidence. Local protocol mocks will not be described as live-provider acceptance.

### Slice 4 evidence

- `internal/provider/google_request_test.go` checks the REST serialization against Pi's adapter and SDK transformation: `contents`, top-level `systemInstruction` and `tools`, and `generationConfig` (not a nested `config` field). It covers system/user text, inline images, `parametersJsonSchema`, `const` to `enum`, call/response grouping, IDs, errors, and image routing.
- `internal/provider/google_test.go` uses loopback HTTP to check `:streamGenerateContent?alt=sse`, `x-goog-api-key`, SDK-compatible `Accept: */*`, OpenCode Go's catalog route/session header, and explicit case-insensitive header precedence. Stream tests cover text/thought/signatures, usage, finish mapping, invalid and incomplete calls, secret-safe failures, retry-before-stream, no retry after stream start, and cancellation.
- `internal/agent/google_replay_test.go` performs a Google tool/read/result round trip, reopens the JSONL v3 session, and checks that the call ID, valid thought signature, function response, response ID, and raw finish reason survive replay.
- `internal/app/provider_config_test.go` exercises runtime model/API dispatch with `GEMINI_API_KEY`; `internal/app/completions_test.go` checks all bundled Google and OpenCode Go Google models have a valid request path or a clear unavailable reason.
- A local capture with installed `@google/genai` 1.30.0 showed the matching REST body and route. Pi's lockfile pins 2.21.0; that package was not available offline for an executable version-exact comparison. No live provider request ran.
- Focused and full package tests passed: `go test ./internal/provider ./internal/agent ./internal/app -count=1`.

G6-04 is locally supported by synthetic evidence and pinned-source review. This does not claim live service acceptance or executable parity with the separately pinned SDK package.
