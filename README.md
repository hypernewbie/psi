# psi: minimal Go coding agent

Minimal Go implementation of the useful core of Pi.
See `COMPATIBILITY.md` for the pinned baseline and supported boundaries.
See [the acceptance ledger](docs/acceptance.md) for regression evidence and incomplete release requirements.
The implementation does not yet meet all PLAN2 acceptance gates.

## Quickstart (Linux / WSL2)

Prerequisites: Go 1.24+, `bash`, a terminal with native scrollback.

```sh
# build
go build -o psi ./cmd/psi

# run
./psi --help
./psi
./psi --continue
./psi --resume
```

Psi reads your provider/model defaults and credentials from Pi's agent directory.
For `openai-codex`, it uses your existing ChatGPT OAuth login and refreshes it when
needed. Log in through Pi (`/login openai-codex`) first; psi has no login UI.
It does not fall back to a separately billed OpenAI API key.

Anthropic Messages also supports API keys:

```sh
export ANTHROPIC_API_KEY="..."
./psi --provider anthropic --model claude-sonnet-4-5
```

Configuration lives under `~/.pi/agent/` (`settings.json`, `models.json`, `auth.json`).
Project configuration lives under `.pi/settings.json` and requires explicit consent
on first use per run (`--allow-project` / `--deny-project` or interactive prompt).

## Checks

```sh
./scripts/check.sh
```

Checks formatting, runs `go vet`, tests, race detection, and builds all packages.
Live tests require explicit operator approval and the `liveprovider` build tag:
`PSI_LIVE_PROVIDER=1 go test -tags liveprovider ./internal/provider -run Live`.
The normal suite uses local HTTP servers and real PTYs to exercise both providers.
Regression fixtures are synthetic. Test subprocesses have a temporary HOME and an
allowlisted environment; they do not inherit your credentials or Pi configuration.

An optional differential test uses a real Pi session reader:

```sh
PSI_PI_SESSION_MODULE=/path/to/pi/dist/core/session-manager.js \
  go test ./internal/session -run TestReferencePiProjection -count=1
```

Node and Pi are needed only for this reference test, not for psi.
The test is optional for routine development but required as release compatibility evidence.

## Layout

```text
cmd/psi/          entrypoint
internal/session/ Pi v3 JSONL storage, indexing, projection
internal/config/  settings, models.json, auth, instructions
internal/skills/  skill discovery and invocation
internal/provider/ Anthropic and ChatGPT Codex adapters + fake for tests
internal/tools/   read, write, edit, bash
internal/agent/   run-at-a-time model/tool loop
internal/terminal/ inline terminal, prompt, status, cleanup
internal/app/     startup, dispatch, session lifecycle
internal/testutil/ fake provider, temp HOME, fixtures
```

## Scope

- Interactive inline terminal only (main screen, native scrollback).
- Four tools: `read`, `write`, `edit`, `bash`.
- Pi-compatible v3 sessions (read/write/append/projection).
- Providers: Anthropic Messages and ChatGPT Codex Responses over HTTP/SSE.
- No extensions, MCP, subagents, RPC, themes, or update checks.
