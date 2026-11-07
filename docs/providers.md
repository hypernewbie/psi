# Providers

psi supports fifteen built-in providers. Each has a fixed protocol list and its
own credential. There is no automatic provider discovery, routing, or virtual
model layer. `models.json` can add providers of its own; see
[Custom providers](#custom-providers).

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
| `kimi-coding` | Kimi For Coding | `openai-completions` | `KIMI_API_KEY` |
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
`reasoning`, `input`, `headers`, `thinkingLevelMap`, `compat`, and provider
`modelOverrides`. The same schema as Pi applies; a key psi does not implement is
ignored rather than rejected, but an `openai-completions`, `openai-responses`,
`google-generative-ai`, or `mistral-conversations` model refuses a `compat`
field psi does not know instead of ignoring it. MiniMax and OpenCode Go
`anthropic-messages` models also refuse a known feature that adapter cannot
replay, so such a model stays unavailable instead of running without it. The
first-party Anthropic adapter ignores compatibility fields.

An `openai-completions` model whose `compat.cacheControlFormat` is `anthropic`
receives Anthropic-style prompt-cache markers on the instruction message, the
last tool, and the last conversation text part. psi also detects this for
`openrouter` models whose id starts with `anthropic/`, as Pi does. No cache
retention (`ttl`) is sent.

Provider selection precedence matches Pi: command-line flags, then the session's
recorded model, then `defaultProvider`/`defaultModel` from settings. See
[Configuration](config.md).

## Custom providers

`models.json` can declare a provider ID that is not built in. Such a provider
uses the protocol it names and the endpoint it declares, and it stays inside
the loaded configuration: it never changes the built-in provider list.

```json
{
  "providers": {
    "local-openai": {
      "name": "Local OpenAI-compatible endpoint",
      "baseUrl": "http://localhost:11434/v1",
      "api": "openai-completions",
      "apiKey": "$LOCAL_API_KEY",
      "headers": { "X-Deployment": "$LOCAL_DEPLOYMENT" },
      "models": [
        { "id": "qwen2.5-coder:7b", "contextWindow": 32768, "maxTokens": 8192 }
      ]
    }
  }
}
```

Provider keys psi reads: `name`, `baseUrl`, `api`, `apiKey`, `authHeader`,
`headers`, `compat`, `models`, and `modelOverrides`.

- The provider supplies the API, the endpoint, and the compatibility defaults.
  The model entry overrides them, and `modelOverrides` apply last.
- `api` and `baseUrl` are required, at the provider level or at the model level.
  A provider without them stays unavailable.
- A missing `contextWindow`, `maxTokens`, `name`, or `input` takes Pi's
  defaults: `128000`, `16384`, the model ID, and text input.
- A `models` entry with a built-in model ID replaces that catalog entry. A
  provider that sets only `baseUrl` keeps the whole built-in catalog.
- `modelOverrides` for a model that does not exist are ignored, as in Pi.
- Allowed protocols: `anthropic-messages`, `openai-completions`,
  `openai-responses`, `google-generative-ai`, and `mistral-conversations`.
  The Codex protocol is not available: it needs the ChatGPT OAuth session.
- A configured provider has no environment variable of its own. Its key comes
  from `auth.json` or from its `apiKey`, in that order. An unsupported stored
  credential stops the selection instead of falling back.

`authHeader: true` sends the resolved key as an `Authorization: Bearer` header
in addition to the protocol's own key header. It needs a resolvable key.

```sh
psi --provider local-openai --model 'qwen2.5-coder:7b'
psi --model local-openai/qwen2.5-coder:7b
```

`/model`, `Ctrl+P`, `--list-models`, the RPC model list, and the bridge model
list use the same provider set, so a configured provider appears in all of
them. `/reload` reads `models.json` again and reports a malformed file while
keeping the last working configuration.

Fields psi does not implement stay unimplemented: `cost`, `inputLimits`,
`promptCache`, `samplingParams`, `samplingParamsByThinkingLevel`, image and
classifier models, and OAuth providers.

## Excluded providers

Cloud credential discovery, Vertex AI, Amazon Bedrock, Azure, Cloudflare, and
OpenAI API keys are not wired. Provider OAuth beyond Codex is not available.
See [Limitations](limitations.md).
