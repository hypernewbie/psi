# psi

Go coding agent with an inline terminal. Four file/shell tools: `read`, `write`, `edit`, `bash`, plus native web access: `web_search`, `fetch_content`, and `get_search_content`. See [Web access](docs/web.md), [Providers](docs/providers.md), and [Sessions](docs/sessions.md). Sessions use Pi-compatible v3 JSONL.

## Build

```sh
go build -o psi ./cmd/psi
```

## Run

```sh
./psi --help
./psi
./psi --continue
./psi --resume
```

For interactive credential setup, start `./psi` and enter `/login`. Select a provider and enter an API key at the masked prompt, or choose OpenAI or OpenAI Codex for subscription sign-in. psi stores Pi-compatible credentials in `auth.json`. API-key login does not check the key with the provider. `/logout` removes stored built-in API keys, not OAuth credentials, environment variables, or `models.json` credentials.

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
./scripts/check.sh
```

Live provider tests stay off unless you pass the `liveprovider` tag yourself.
