# psi

Go coding agent with an inline terminal. Four file/shell tools: `read`, `write`, `edit`, `bash`. See [Providers](docs/providers.md) for supported providers and protocols. Sessions use Pi-compatible v3 JSONL.

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

Codex uses your existing ChatGPT OAuth login from Pi. Log in through Pi first (`/login openai-codex`). No login UI here, and no OpenAI API key fallback.

Each API-key provider uses its own environment variable. See [Providers](docs/providers.md) for the credentials each provider accepts. OpenCode Go selects the API and endpoint from each model's metadata.

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
