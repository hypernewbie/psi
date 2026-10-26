# psi: minimal Go coding agent

Minimal Go implementation of the useful core of Pi.
See `COMPATIBILITY.md` for the pinned baseline and supported boundaries.
See `temp/PLAN.md` (ignored, not shipped) for the full phase plan during development.

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

Authentication (Anthropic Messages, API key):

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

Runs `gofmt`, `go vet`, `go test ./...`, and `go test -race ./...`.
Live provider tests are opt-in: `PSI_LIVE_PROVIDER=1 go test -run Live ./...`.

## Layout

```text
cmd/psi/          entrypoint
internal/session/ Pi v3 JSONL storage, indexing, projection
internal/config/  settings, models.json, auth, instructions
internal/skills/  skill discovery and invocation
internal/provider/ Anthropic Messages adapter + fake for tests
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
- One provider: Anthropic Messages API.
- No extensions, MCP, subagents, RPC, themes, or update checks.
