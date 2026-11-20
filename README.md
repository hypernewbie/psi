# psi

Go coding agent with an inline terminal. Four file/shell tools: `read`, `write`, `edit`, `bash`, plus native web access: `web_search`, `fetch_content`, and `get_search_content`. See [Web access](docs/web.md), [Providers](docs/providers.md), and [Sessions](docs/sessions.md). Sessions use Pi-compatible v3 JSONL.

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

## Run

```sh
./psi --help
./psi
./psi --continue
./psi --resume
```

For interactive credential setup, start `./psi` and enter `/login`. Select a provider and use the masked API-key prompt or the provider's sign-in flow. psi stores credentials in `auth.json`. API-key login does not check the key with the provider. `/logout` removes the selected stored API key or OAuth credential. For OpenAI and Codex OAuth, it attempts one revocation request before local deletion. Kimi logout is local only. `/logout` does not remove environment variables or `models.json` credentials.

Interactive login supports ChatGPT sign-in, OpenAI Codex browser or device-code sign-in, the Global Kimi Code (OAuth) trial, and 16 built-in API-key providers. Anthropic API-key login remains unavailable. Use `/login openai-codex` for the separate Codex OAuth grant; there is no OpenAI API key fallback.

API-key providers also accept stored or configured keys and their own environment variables. See [Providers](docs/providers.md) for credential precedence and supported login providers. OpenCode Go selects the API and endpoint from each model's metadata.

For example, Anthropic takes an API key:

```sh
export ANTHROPIC_API_KEY="..."
./psi --provider anthropic --model claude-sonnet-4-5
```

Use `/model` to see locally available models. Psi reads the bundled public snapshot and Pi's local model cache; it does not fetch catalogs from the network. A model that needs an unsupported API or compatibility feature appears unavailable.

## Test

```sh
./scripts/check.sh
```

Live provider tests stay off unless you pass the `liveprovider` tag yourself.
