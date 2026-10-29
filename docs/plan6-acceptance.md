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
| 5. OpenCode Responses | Complete locally | P6-041–046: distinct generic Responses API client/request path, OpenCode bearer key and session-affinity headers, Pi-compatible input/tool/system updates, text/reasoning/function-call streaming and usage, safe incomplete/interrupted settlement, and restart replay. Evidence below. The optional session-manager matrix passed all 20 provider/API continuation directions against the installed Pi 1.4.0 package; this does not assert a git-revision-identical executable adapter. |
| 6. Integration, safety, and release | In progress | P6-047–059 local work and validation are recorded below. `./scripts/check.sh` passed, as did Linux/macOS/Windows amd64/arm64 cross-builds. P6-060 awaits the final source commit and delivered-binary PTY pass; live-provider acceptance remains blocked without explicit approval. |

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

G6-01, G6-02, G6-03, and G6-05 are supported by local synthetic evidence and source review. Remaining gates stay open until their listed A6 cases have evidence. Local protocol mocks will not be described as live-provider acceptance.

### Slice 4 evidence

- `internal/provider/google_request_test.go` checks the REST serialization against Pi's adapter and SDK transformation: `contents`, top-level `systemInstruction` and `tools`, and `generationConfig` (not a nested `config` field). It covers system/user text, inline images, `parametersJsonSchema`, `const` to `enum`, call/response grouping, IDs, errors, and image routing.
- `internal/provider/google_test.go` uses loopback HTTP to check `:streamGenerateContent?alt=sse`, `x-goog-api-key`, SDK-compatible `Accept: */*`, OpenCode Go's catalog route/session header, and explicit case-insensitive header precedence. Stream tests cover text/thought/signatures, usage, finish mapping, invalid and incomplete calls, secret-safe failures, retry-before-stream, no retry after stream start, and cancellation.
- `internal/agent/google_replay_test.go` performs a Google tool/read/result round trip, reopens the JSONL v3 session, and checks that the call ID, valid thought signature, function response, response ID, and raw finish reason survive replay.
- `internal/app/provider_config_test.go` exercises runtime model/API dispatch with `GEMINI_API_KEY`; `internal/app/completions_test.go` checks all bundled Google and OpenCode Go Google models have a valid request path or a clear unavailable reason.
- A local capture with installed `@google/genai` 1.30.0 showed the matching REST body and route. Pi's lockfile pins 2.21.0; that package was not available offline for an executable version-exact comparison. No live provider request ran.
- Focused and full package tests passed: `go test ./internal/provider ./internal/agent ./internal/app -count=1`.

G6-04 is locally supported by synthetic evidence and pinned-source review. This does not claim live service acceptance or executable parity with the separately pinned SDK package.

### Slice 5 evidence

- `internal/provider/responses_request.go` builds the generic `/responses` input, `store:false`, optional 16-token-clamped `max_output_tokens`, reasoning levels and signatures, images/tool outputs, and OpenCode `additional_tools`/mid-conversation system changes. Unknown compatibility fields fail closed. It keeps generic bearer authentication separate from Codex OAuth.
- `internal/provider/responses.go` uses bounded HTTP/SSE parsing, pre-stream retries only, `prompt_cache_key`, `openai-nosession` affinity, and the case-insensitive OpenCode session header. Loopback tests cover request route/headers/body, text/reasoning/function calls, IDs/phases, usage, explicit header precedence, malformed/interrupted/incomplete streams, and safe tool-call settlement.
- `internal/agent/responses_cycle_test.go` runs a synthetic OpenCode Responses read-tool round trip, reopens the JSONL v3 session, and checks same-model reasoning item, function call/result IDs, provider/model/response identity, usage, and final answer replay.
- `internal/app/responses_test.go` exercises runtime API dispatch and provider-key selection. `internal/app/completions_test.go` checks request-builder/thinking capability across all bundled OpenCode Responses models.
- `PSI_PI_RESPONSES_MODULE=/home/hypernewbie/.nvm/versions/node/v24.11.1/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-ai/dist/api/openai-responses-shared.js go test ./internal/provider -run TestResponsesRequestMatchesOptionalPiReference -count=1 -v` passed. It differentially compares normalized Responses input and tool/system changes with the installed Pi 1.4.0 adapter.
- `PSI_PI_SESSION_MODULE=file:///tmp/psi-pi-1.4-session-adapter.mjs go test ./internal/session -run TestReferencePiContinuationMatrix -count=1 -v` passed all 20 directions (ten provider/API cases in both Pi→psi and psi→Pi order) using the installed Pi 1.4.0 session manager. Both optional references use installed package artifacts, not verified builds from the pinned source revision; exact executable Pi parity remains unclaimed.
- `go test ./internal/provider ./internal/agent ./internal/app ./internal/session -count=1` passed after this slice. No live provider request ran.

### Slice 6 progress

- P6-047–048: provider API support is checked by `BuildRequest` for model selection/configuration; candidate config installs only after validation and durable session change. Existing transactional model/startup tests and `internal/app/completions_test.go` cover available thinking levels for bundled Responses models.
- P6-049–051: provider tests cover Responses context-limit HTTP classification, usage parsing/persistence, partial/incomplete tool settlement, and session replay. `internal/agent` covers bounded overflow/compaction recovery without duplicate tool execution; synthetic replay fixtures cover all four new protocol APIs and cross-provider call-ID translation.
- P6-052: the optional Responses request differential and all 20 session continuation directions passed against installed Pi 1.4.0 package files. Request differentials for the other APIs and a reference executable built from the pinned source revision remain unclaimed.
- P6-053–054: `go test ./internal/app -run 'TestActualAppPTYProviderToolCycleAndRestart|TestActualCopiedBinaryWithoutNodeUsesAllFourTools' -psi-test-binary="$PWD/psi" -count=1 -v` passed against the delivered executable. The five provider journeys checked restored model, credential use, one read-tool cycle, restart replay, endpoint/header, and key-leak canaries. The copied-binary journey also completed write/read/edit/bash without Node.
- P6-055–056: `FuzzResponsesSSE` passed 10 seconds (~1.2M executions). The two previously race-flaky PTY tests now wait for visible editor/output settlement; `go test -race ./internal/provider ./internal/agent ./internal/app -count=1` passed. `./scripts/check.sh` passed full vet, test, race, and build. Cross-builds passed for Linux, macOS, and Windows amd64/arm64. Commands and hashes are recorded in the release section below.
- P6-057: updated `COMPATIBILITY.md`, `README.md`, `docs/config.md`, `docs/quickstart.md`, and `docs/limitations.md` with registered providers, credentials, API boundaries, offline catalogs, and unrun live acceptance.
- P6-058–059: this ledger records local evidence. PLAN6 live tests require the `liveprovider` build tag plus `PSI_LIVE_PROVIDER=1`, an explicitly selected provider/API/model, and its key. `go test -tags liveprovider ./internal/provider -run '^TestLive' -count=1 -v` skipped because no opt-in was supplied. The separate legacy Anthropic smoke requires `PSI_LIVE_ANTHROPIC=1`. No live requests ran.
- P6-060: `./psi` was built from source commit `374c36cf4ac3cf3c4e8e08f34d38062d9741526b`; its SHA-256 is `0f745e86230ee806e7ed307df8223df433db04777a9d46b2d35d3bc0261004e7`. Delivered-binary provider PTY and no-Node four-tool tests passed.

## Release evidence

- `./scripts/check.sh` passed: `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...`, and `go build ./...`.
- Cross-build command: `GOOS=<os> GOARCH=<arch> CGO_ENABLED=0 go build -o /tmp/psi-<os>-<arch> ./cmd/psi` for `linux`, `darwin`, and `windows`, each with `amd64` and `arm64`. All six builds passed. SHA-256:
  - Linux amd64: `0f6e7ce8a72a8df78d82a9b5e1ef2c667276a7ffe0aae56b263e60e9c1d9bdf7`
  - Linux arm64: `6d290be0a400d373d4f7ef067eaea0e236ac2856239a410d827208b15a884366`
  - macOS amd64: `c8178851422741ea9db386e79b70e3c5b405f752ab020ab5c1712b36d6cd57eb`
  - macOS arm64: `2bab01afba7e41abeaabfa94345303fa91f825541a5f845093f0c68c68e36250`
  - Windows amd64: `aa116653ca82c88be99aabdad090aefd3f9efe17368955bac661317b90cda5c3`
  - Windows arm64: `1f5559a9561453a925cbee95110ca1b2d1aa254850c1a43fd7945bc716e29fd2`
- `./psi` source commit: `374c36cf4ac3cf3c4e8e08f34d38062d9741526b`.
- `./psi` SHA-256: `0f745e86230ee806e7ed307df8223df433db04777a9d46b2d35d3bc0261004e7`.
- `go test ./internal/app -run 'TestActualAppPTYProviderToolCycleAndRestart|TestActualCopiedBinaryWithoutNodeUsesAllFourTools' -psi-test-binary="$PWD/psi" -count=1 -v` passed. Provider PTY journeys passed for MiniMax, OpenCode Go Responses, DeepSeek, Z.AI, and Google; copied-binary no-Node journey passed all four tools.

## Remaining acceptance boundaries

- Live-provider service acceptance is Blocked pending explicit operator approval and credentials. No live calls were made.
- Pi executable parity is Partial: installed Pi 1.4.0 request/session adapters were exercised, but no executable was verified as built from the pinned source commit. The Google SDK reference is also partial: installed 1.30.0 loopback capture supported the wire shape, while Pi pins 2.21.0.
- Native macOS/Windows runtime, terminal-human review, and remote CI are not claimed by this PLAN6 local evidence. No VCI coordinator was configured or contacted.
