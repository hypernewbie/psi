# Providers

psi supports nineteen built-in providers. Each has a fixed protocol list and its
own credential. There is no automatic provider discovery, routing, or virtual
model layer. `models.json` can add providers of its own; see
[Custom providers](#custom-providers).

| Provider ID | Name | Protocol | Credential |
|---|---|---|---|
| `anthropic` | Anthropic | `anthropic-messages` | `ANTHROPIC_API_KEY` |
| `openai` | OpenAI | `openai-responses` | ChatGPT sign-in in psi or `OPENAI_API_KEY` |
| `openai-codex` | OpenAI Codex | `openai-codex-responses` | Codex sign-in in psi or Pi OAuth session |
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

### Optional keys

A custom provider may omit its API key when it uses `openai-completions`,
`openai-responses`, or `anthropic-messages`. The request then has no generated
`Authorization` or `x-api-key` header. Headers that you configure stay in
effect. `authHeader: true` still requires a key.

```json
{
  "providers": {
    "local-openai": {
      "baseUrl": "http://localhost:11434/v1",
      "api": "openai-completions",
      "models": [{ "id": "qwen2.5-coder:7b" }]
    }
  }
}
```

The built-in `openai` and `anthropic` providers can omit the key only when
`models.json` sets a provider or model `baseUrl` to an explicit non-default
endpoint. A catalog or cached endpoint, or a URL that normalizes to the official
endpoint, still requires a key. The normal credential order does not change:
`auth.json`, then the configured `models.json` key, then the exact environment
variable. A key that is present is still sent.

On an eligible built-in override, an explicit empty `apiKey`, a blank
environment variable, an unsupported stored entry, or a malformed auth
document stops selection with an error. It does not fall back to a keyless
request. Google Generative AI, Mistral Conversations, and Codex still require
their credentials. OpenAI ChatGPT OAuth still works only with the official
endpoint.

Pi's documented local-endpoint setup uses a dummy key. A keyless request on an
eligible endpoint is an intentional psi divergence.

Pi and psi write compatible `auth.json` entries. For API-key providers the shape is:

```json
{ "anthropic": { "type": "api_key", "key": "sk-ant-..." } }
```

API-key login and `/logout` preserve an existing `auth.json` symlink. They use
the logical path for the shared lock, resolve the regular-file target under
that lock, and rename beside that target. `/logout` also takes Pi's
resolved-target lease when that lock path differs, so deletion coordinates
with Pi writers that use the resolved file. The acquisitions share a
five-second budget. Link text, target mode, unknown fields, and unrelated
entries stay unchanged. Broken, cyclic, and nonregular targets fail closed. A
link retargeted before a final recheck is refused; the final rename remains
subject to filesystem races.

`openai-codex` uses a separate ChatGPT OAuth grant. Run `/login openai-codex`
in psi and choose **Browser login** or **Device code login**. Browser login
tries `127.0.0.1:1455`, then `1457`, then one OS-assigned port; the masked
sign-in field also accepts a redirect URL, `code#state`, query string, or bare
code. Device-code availability depends on the account. psi refreshes an expired
Codex token, preserves unrelated fields, and never substitutes a billed OpenAI
API key for Codex OAuth. An `auth.json` symlink is preserved during login, refresh, and logout; the
logical path remains the lock target, while the resolved regular file receives
the atomic replacement. Logout holds the logical lease and, when distinct,
Pi's resolved-target lease across the revocation request and the local write.

By default, Codex credential checks and refreshes wait up to thirty seconds for
each shared lock, matching Pi. API-key login and `/logout` lease acquisition
wait up to five seconds. Both paths respect cancellation. An incomplete or invalid
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

ChatGPT sign-in credentials work only with the official OpenAI endpoint
(`https://api.openai.com/v1`). API keys can still use a configured endpoint.
Built-in OpenAI requests do not follow redirects with either credential type.

When the active built-in `openai-codex` model uses the
`openai-codex-responses` API, psi periodically reads the account's used primary
quota from the fixed `https://chatgpt.com/backend-api/wham/usage` endpoint and
shows the remaining percentage in a ten-cell track (one cell per ten percent)
inside unused footer space. Polling is faster near a limit, backs off
exponentially after failures, honors `Retry-After`, and stops for the rest of
the session when the server asks for more than 24 hours. The reading is best
effort and uses only a locally valid Codex OAuth entry; it does not sign in,
refresh or write credentials, create an auth lease, or use an API-key fallback.
The indicator is hidden for custom models, non-official endpoints, and any
configured `Authorization` header or `authHeader`. Direct-use `openai`
credentials do not provide this reading; manage ChatGPT plan usage at
`https://chatgpt.com/settings/usage`.

Credential values in `models.json` accept `$VAR`, `${VAR}`, `$$`, and `$!`
escapes. `!command` values are rejected without being executed.

Interactive API-key login supports the 16 built-in API-key providers
listed in [the CLI guide](cli.md#interactive-login). The prompt masks the key and writes a
Pi-compatible `api_key` entry to `auth.json`; key values are not accepted as
command arguments. The login flow does not validate the key against the
provider. If an offline session has a default model for the provider, psi
selects that model and saves the new defaults. Otherwise, it keeps the current
model. Anthropic API-key login is not available in psi; use the matching
environment variable or `models.json`. OpenAI Codex uses `/login openai-codex`.

Use `/logout` to select and remove a stored built-in credential. The picker
lists API keys for built-in API-key providers, direct OpenAI ChatGPT OAuth,
and OpenAI Codex OAuth. For OAuth, psi sends one bounded request to
`https://auth.openai.com/oauth/revoke` and reports the remote outcome
separately. Local deletion continues if that request is rejected, redirected,
times out, or cannot be sent. A 2xx response only accepts the request; it does
not prove that already-issued access tokens stopped working. Removal does not
affect environment variables or credentials from `models.json`; an API key is
removed locally and is not revoked at the provider. Removing the active
provider credential reloads that provider from the remaining credential
sources. If none exists, an eligible keyless override can stay online;
otherwise, psi goes offline until you configure a credential. If Pi uses the
same `auth.json`, its next successful read sees the deletion.

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
  credential stops the selection instead of falling back. For the three
  in-scope protocols, the key can be omitted on an eligible endpoint; see
  [Optional keys](#optional-keys).

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
