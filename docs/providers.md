# Providers

psi supports nineteen built-in providers. Each has a fixed protocol list and its
own credential. There is no automatic provider discovery, routing, or virtual
model layer. `models.json` can add providers of its own; see
[Custom providers](#custom-providers).

| Provider ID | Name | Protocol | Credential |
|---|---|---|---|
| `anthropic` | Anthropic | `anthropic-messages` | `ANTHROPIC_API_KEY` |
| `openai` | OpenAI | `openai-responses` | ChatGPT sign-in in psi or `OPENAI_API_KEY` |
| `openai-codex` | OpenAI Codex | `openai-codex-responses` | Pi OAuth session |
| `minimax` | MiniMax | `anthropic-messages` | `MINIMAX_API_KEY` |
| `opencode-go` | OpenCode Go | per model (see below) | `OPENCODE_API_KEY` |
| `deepseek` | DeepSeek | `openai-completions` | `DEEPSEEK_API_KEY` |
| `zai` | Z.AI | `openai-completions` | `ZAI_API_KEY` |
| `xiaomi` | Xiaomi MiMo | `openai-completions` | `XIAOMI_API_KEY` |
| `xiaomi-token-plan-cn` | Xiaomi Token Plan CN | `openai-completions` | `XIAOMI_TOKEN_PLAN_CN_API_KEY` |
| `xiaomi-token-plan-ams` | Xiaomi Token Plan AMS | `openai-completions` | `XIAOMI_TOKEN_PLAN_AMS_API_KEY` |
| `xiaomi-token-plan-sgp` | Xiaomi Token Plan SGP | `openai-completions` | `XIAOMI_TOKEN_PLAN_SGP_API_KEY` |
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
pair is rejected at startup, not retried against another endpoint. Z.AI's
code-1308 plan-limit response keeps its provider message visible, including the
reset time, and is not retried.

## Credentials

psi resolves a credential in this order:

1. A stored entry in `<agentDir>/auth.json`.
2. Credentials declared for the provider in `models.json`.
3. The provider's environment variable.

Malformed JSON, invalid UTF-8, and documents larger than 4 MiB stop credential
selection and `/logout` listing. psi does not fall back to another credential
source after these errors.

Pi and psi write compatible `auth.json` entries. For API-key providers the shape is:

```json
{ "anthropic": { "type": "api_key", "key": "sk-ant-..." } }
```

API-key login and `/logout` preserve an existing `auth.json` symlink. They use
the logical path for the shared lock, resolve the regular-file target under
that lock, and rename beside that target. Link text, target mode, unknown
fields, and unrelated entries stay unchanged. Broken, cyclic, and nonregular
targets fail closed. A link retargeted before a final recheck is refused; the
final rename remains subject to filesystem races.

`openai-codex` uses the ChatGPT OAuth entry from Pi instead. psi refreshes an
expired Codex token, preserves unrelated fields, and never substitutes a billed
OpenAI API key for Codex OAuth. Run `/login openai-codex` in Pi to sign in. An
`auth.json` symlink is preserved during refresh; the logical path remains the
lock target, while the resolved regular file receives the atomic replacement.

By default, Codex credential checks and refreshes wait up to thirty seconds for
each shared lock, matching Pi. API-key writes from `/login` and `/logout` wait
up to five seconds. Both paths respect cancellation. An incomplete or invalid
Codex token response leaves the auth file unchanged. If a valid rotated token
response arrives and the lease and target checks pass, psi commits it before
returning cancellation so Pi can use the new refresh token. psi reclaims a stale
lease after its heartbeat stops. On Windows, access-denied errors during lock
creation use the same bounded wait.

`openai` signs in from `/login` by selecting OpenAI, or directly with
`/login openai`. It can also use a stored API key. A stored `openai`
entry owns the provider: OAuth failures and invalid entries never fall back
to another key. Only a missing entry permits `models.json`, then
`OPENAI_API_KEY`. An OAuth token that is missing or near expiry refreshes
when you submit a request. A request waits up to five seconds for the shared
credential lock. Once an exchange starts, it has its own 15-second deadline.
psi waits for the exchange and saves a valid rotation before returning
cancellation.

ChatGPT sign-in needs no prerequisite credential. It opens the authorization
page in your browser and serves the callback on `127.0.0.1:1455`; psi and the
browser must run on the same machine. If the browser does not open, use the
link in the notice. Never paste the callback URL into chat. `Esc` or `Ctrl+C`
cancels; `Ctrl+D` cancels and exits. The browser wait lasts five minutes. A
failed code exchange is not retried; start a new login. Cancelling before the
save starts keeps the previous credential. An admitted save finishes without
activating the model after cancellation. OpenAI sign-in and refresh refuse a
symlinked `auth.json`. The user `settings.json` stores a stable installation
UUID as `deviceId`; it is not a token.

Credential values in `models.json` accept `$VAR`, `${VAR}`, `$$`, and `$!`
escapes. `!command` values are rejected without being executed.

Interactive API-key login supports the 16 built-in API-key providers
listed in [the CLI guide](cli.md#interactive-login). The prompt masks the key and writes a
Pi-compatible `api_key` entry to `auth.json`; key values are not accepted as
command arguments. The login flow does not validate the key against the
provider. If an offline session has a default model for the provider, psi
selects that model and saves the new defaults. Otherwise, it keeps the current
model. Anthropic API-key login and OpenAI Codex OAuth login are not available
in psi. Use the matching environment variable or `models.json` for Anthropic,
and Pi's `/login openai-codex` for Codex OAuth.

Use `/logout` to select and remove a stored built-in API key. It includes
Anthropic API keys, but does not remove OAuth credentials, environment
variables, or credentials from `models.json`. Removing the active provider key
reloads that provider from the remaining credential sources; if none exists,
psi goes offline until you configure a credential again.

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

## Request limits

The output limit of a request is the smaller of the model's output cap and the
room left in its context window. A model whose output cap equals its context
window, such as an OpenCode Go Kimi model, would otherwise ask for more tokens
than the endpoint can return, and the endpoint rejects that request with a
`400`. Every adapter applies the cap, and a longer prompt lowers the limit.

## Provider errors

A failed request reports the kind of failure, the HTTP status, and the reason
the server gave. An endpoint that rejects a request usually states why, so the
reason is shown instead of the status text alone. The reason comes from a
structured error body, it stays on one line, and it is bounded in length. The
rest of the response body stays private, because a gateway can echo request
data into it. An endpoint that returns no usable reason reports the HTTP
status text.

## Anthropic beta features

An `anthropic-messages` request that uses a beta feature has to declare it in
the `anthropic-beta` header, or the endpoint refuses the request. psi computes
the header from the request body:

- `fine-grained-tool-streaming-2025-05-14` for a request with tools that the
  model does not stream eagerly.
- `interleaved-thinking-2025-05-14` for a model with budget-based thinking, so
  a replayed thinking block stays valid.
- `server-side-fallback-2026-07-01` for a model with allowed fallback models.
- `mid-conversation-output-config-2026-07-01` and
  `thinking-binding-controls-2026-08-01` for a model that uses managed effort.

A configured `anthropic-beta` header replaces the computed set, and an empty
configured value removes the header instead of sending an empty feature list.
psi does not declare the features that need native tool changes or message
threads, because it does not send those features.

An `anthropic-messages` request declares eager tool input streaming on every
tool, unless the model does not support it. A model without that support gets
the fine-grained tool streaming feature instead. A budget thinking profile
states its display, so the endpoint returns summarized thinking. A model that
lists allowed fallback models names them in the request, so the endpoint can
fall back to another model when the first one is unavailable.

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
classifier models are not wired. Provider OAuth beyond Codex and OpenAI is
not available. OpenAI signs in with `/login openai` in psi.
