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
| 2. MiniMax and OpenCode Anthropic | In progress | Shared Anthropic-compatible client construction and provider-specific key resolution are wired. Protocol parity, OpenCode session headers, and full stream/replay tests remain. |
| 3. OpenAI Chat Completions | Not started | — |
| 4. Google Generative AI | Not started | — |
| 5. OpenCode Responses | Not started | — |
| 6. Integration, safety, and release | Not started | — |

### Slice 1 evidence

- `node --check scripts/snapshot-models.mjs` passed.
- Regenerated `catalog.json` and `catalog-source.json` from installed public `@earendil-works/pi-ai` 1.4.0; both matched the checked-in files byte-for-byte.
- A synthetic package with a non-public name was rejected by the generator.
- Registry tests assert exactly seven provider IDs and the allowed API pairs. Cache tests cover missing, malformed, stale, oversized, valid multi-provider, unsupported-API, malicious metadata, and OpenCode endpoint cases.
- Credential tests cover stored key → `models.json` → exact provider environment variable for each new API-key provider, and fail-closed unsupported stored auth and command credentials.
- `go test ./... -count=1` passed after the slice 1 changes.

G6-01 is met by local synthetic evidence. Remaining gates stay open until their listed A6 cases have evidence. Local protocol mocks will not be described as live-provider acceptance.
