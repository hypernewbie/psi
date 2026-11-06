# Providers

psi supports fourteen providers. Each has a fixed protocol list and its own
credential. There is no automatic provider discovery, routing, or virtual
model layer.

| Provider ID | Name | Protocol | Credential |
|---|---|---|---|
| `anthropic` | Anthropic | `anthropic-messages` | `ANTHROPIC_API_KEY` |
| `openai-codex` | OpenAI Codex | `openai-codex-responses` | Pi OAuth session |
| `minimax` | MiniMax | `anthropic-messages` | `MINIMAX_API_KEY` |
| `opencode-go` | OpenCode Go | per model (see below) | `OPENCODE_API_KEY` |
| `deepseek` | DeepSeek | `openai-completions` | `DEEPSEEK_API_KEY` |
| `zai` | Z.AI | `openai-completions` | `ZAI_API_KEY` |
| `xiaomi` | Xiaomi MiMo | `openai-completions` | `XIAOMI_API_KEY` |
| `moonshotai` | Moonshot AI | `openai-completions` | `MOONSHOT_API_KEY` |
| `qwen-token-plan` | Qwen Token Plan | `openai-completions` | `QWEN_TOKEN_PLAN_API_KEY` |
| `openrouter` | OpenRouter | `openai-completions` | `OPENROUTER_API_KEY` |
| `xai` | xAI | `openai-responses` | `XAI_API_KEY` |
| `groq` | Groq | `openai-completions` | `GROQ_API_KEY` |
| `mistral` | Mistral | `mistral-conversations` | `MISTRAL_API_KEY` |
| `google` | Google | `google-generative-ai` | `GEMINI_API_KEY` |

OpenCode Go models choose Anthropic Messages, OpenAI Chat Completions, OpenAI
Responses, or Google Generative AI per model. An unsupported provider/protocol
pair is rejected at startup, not retried against another endpoint.

## Credentials

psi resolves a credential in this order:

1. A stored entry in `<agentDir>/auth.json`.
2. Credentials declared for the provider in `models.json`.
3. The provider's environment variable.

`auth.json` entries are written by Pi. For API-key providers the shape is:

```json
{ "anthropic": { "type": "api_key", "key": "sk-ant-..." } }
```

`openai-codex` uses the ChatGPT OAuth entry from Pi instead. psi refreshes an
expired Codex token, preserves unrelated fields, and never substitutes a billed
OpenAI API key for Codex OAuth. Run `/login openai-codex` in Pi to sign in.

Each acquisition of the shared credential lock has a five-second wait limit and
respects cancellation. On Windows, access-denied errors during lock creation use
the same bounded wait.

Credential values in `models.json` accept `$VAR`, `${VAR}`, `$$`, and `$!`
escapes. `!command` values are rejected without being executed.

## Choose a model

Model metadata comes from the bundled public catalog plus Pi's local cache.
psi never refreshes it over the network.

```sh
psi --list-models                 # everything available
psi --list-models sonnet          # fuzzy filter
psi --provider anthropic --model claude-sonnet-4-5
psi --model anthropic/claude-sonnet-4-5:high
```

Inside a session, `/model` opens a searchable picker of models whose provider is
already credentialed and `/thinking` sets the reasoning level. Both changes
apply to the current session only. `Ctrl+L` and `Shift+Tab` are shortcuts for
the same two pickers.

A `:<thinking>` suffix is accepted in `--model` and in `defaultModel`. The level
is clamped to what the model supports: `off`, `minimal`, `low`, `medium`,
`high`, `xhigh`, or `max`.

## models.json

Declare extra models or override a catalog entry in
`<agentDir>/models.json`:

```json
{
  "providers": {
    "anthropic": {
      "baseUrl": "https://api.anthropic.com",
      "models": [
        {
          "id": "internal-sonnet",
          "api": "anthropic-messages",
          "contextWindow": 200000,
          "maxTokens": 64000,
          "reasoning": true,
          "input": ["text", "image"]
        }
      ]
    }
  }
}
```

Per-model keys psi reads: `id`, `api`, `baseUrl`, `contextWindow`, `maxTokens`,
`reasoning`, `input`, `headers`, `thinkingLevelMap`, and provider
`modelOverrides`. The same schema as Pi applies; a key psi does not implement is
ignored rather than rejected.

Provider selection precedence matches Pi: command-line flags, then the session's
recorded model, then `defaultProvider`/`defaultModel` from settings. See
[Configuration](config.md).

## Excluded providers

Cloud credential discovery, Vertex AI, Amazon Bedrock, Azure, Cloudflare, and
OpenAI API keys are not wired. Provider OAuth beyond Codex is not available.
See [Limitations](limitations.md).
