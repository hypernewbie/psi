# psi

Go coding agent with an inline terminal. Four tools: `read`, `write`, `edit`, `bash`. Two providers: Anthropic Messages, ChatGPT Codex. Sessions are Pi-compatible v3 JSONL.

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

Anthropic takes an API key:

```sh
export ANTHROPIC_API_KEY="..."
./psi --provider anthropic --model claude-sonnet-4-5
```

## Test

```sh
./scripts/check.sh
```

Live provider tests stay off unless you pass the `liveprovider` tag yourself.
