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

For API-key setup without Pi, start `./psi` and enter `/login`. Select a provider and enter the key at the masked prompt. psi stores the key in Pi-compatible `auth.json`. Login does not check the key with the provider. `/logout` removes stored built-in API keys, not OAuth credentials, environment variables, or `models.json` credentials.

Interactive login supports 16 built-in API-key providers, excluding Anthropic and OpenAI Codex. Codex uses your existing ChatGPT OAuth login from Pi. Log in through Pi first (`/login openai-codex`). There is no OpenAI API key fallback.

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
