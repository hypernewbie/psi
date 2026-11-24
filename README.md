# psi

Go coding agent with an inline terminal. Four file/shell tools: `read`, `write`, `edit`, `bash`, plus native web access: `web_search`, `fetch_content`, and `get_search_content`. MCP tools over stdio come from `mcp.json`. See [Web access](docs/web.md), [MCP servers](docs/mcp.md), [Providers](docs/providers.md), and [Sessions](docs/sessions.md). Sessions use Pi-compatible v3 JSONL.

## Install

Go 1.25 or later is required. Go installation requires psi v0.5.0 or later:

```sh
go install github.com/hypernewbie/psi@latest
psi --help
```

The install directory must be on `PATH`. Find it with `go env GOBIN`. If that prints nothing, use `bin` under the first directory from `go env GOPATH`.

## Build

To build from a checkout:

```sh
go build -o psi .
./psi --help
```

On Windows:

```sh
go build -o psi.exe .
.\psi.exe --help
```

In the examples below, use `psi` instead of `./psi` when you run an installed binary.

## Developer commands

Run these checkout-local commands from the repository root with Go 1.25 or later.
`check` requires Git on `PATH`. It uses Git to list tracked and non-ignored
untracked files for the JavaScript and TypeScript gate. It uses `gofmt` from
the Go toolchain.

```sh
go tool dev build
go tool dev run [args...]
go tool dev install
go tool dev bake <new-output-directory>
go tool dev check
```

`build` creates the native `psi` or `psi.exe` binary in the checkout. `install`
installs psi from this checkout to `GOBIN`, or to Go's default install directory
when `GOBIN` is empty. `build`, `run`, and `install` wrap `go build .`, `go run .`,
and `go install .`; the direct Go forms remain available. The public
`go install github.com/hypernewbie/psi@latest` command in the Install section
installs a published version instead.

`build`, `run`, and `install` do not bake catalogs. `bake` runs the existing
catalog generator and needs network access; its destination and fetch limits are
in [Catalog bake](docs/config.md#catalog-bake-developer). Go commands use Go's
normal download and verification settings. A failure is reported without an
automatic retry or stale-binary fallback.

`check` runs the Git file gate, gofmt, vet, tests, race tests, and build.
`--no-js-only` runs only the Git file gate. Race tests run by default. On Linux
and Windows, race tests require cgo and a compatible C compiler. If the
required toolchain is unavailable, use `--skip-race` to run the other checks.
Windows CI uses `--skip-race`.

## Run

```sh
./psi --help
./psi
./psi --continue
./psi --resume
```

For interactive credential setup, start `./psi` and enter `/login`. Select a provider and enter an API key at the masked prompt, or choose OpenAI or OpenAI Codex for subscription sign-in. psi stores Pi-compatible credentials in `auth.json`. API-key login does not check the key with the provider. `/logout` removes the selected stored API key or OAuth credential. OAuth logout attempts one best-effort OpenAI revocation request; local deletion continues if that request fails. `/logout` does not remove environment variables or `models.json` credentials.

Interactive login supports ChatGPT sign-in, OpenAI Codex browser or device-code sign-in, and 16 built-in API-key providers. Anthropic API-key login remains unavailable. Use `/login openai-codex` for the separate Codex OAuth grant; there is no OpenAI API key fallback.

API-key providers also accept stored or configured keys and their own environment variables. See [Providers](docs/providers.md) for credential precedence and supported login providers. OpenCode Go selects the API and endpoint from each model's metadata.

For example, Anthropic takes an API key:

```sh
export ANTHROPIC_API_KEY="..."
./psi --provider anthropic --model claude-sonnet-4-5
```

Use `/model` to see locally available models. Psi reads the bundled public snapshot and Pi's local model cache; it does not fetch catalogs from the network. A model that needs an unsupported API or compatibility feature appears unavailable.

## Test

```sh
go tool dev check
```

`./scripts/check.sh` remains available as a compatibility shim. Live provider tests stay off unless you pass the `liveprovider` tag yourself.
