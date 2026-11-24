# CLI and configuration

CLI (exact spelling, no hidden flags):

```text
psi
psi --continue
psi --resume
psi --session PATH
psi --provider PROVIDER --model MODEL
psi --thinking LEVEL
psi --session-dir PATH
psi --tools read,bash
psi --no-tools
psi --use-theme NAME
psi --theme PATH
psi --no-themes
psi --no-session
psi --docs [topic]
psi --help
psi --version
```

Project consent: `--allow-project` / `--deny-project`. Otherwise an inline
choice appears before psi reads `.pi/settings.json`, project skills/themes/packages,
or project `SYSTEM.md`/`APPEND_SYSTEM.md`. The default is deny; Escape and Ctrl+C also deny.
The choice applies to this run only. Context files such as `AGENTS.md` load
separately, as in Pi. User instructions come from the agent directory and
ancestor folders.

`psi --docs` lists the topics embedded in the binary and `psi --docs <topic>`
prints one. Those same topics appear in the `<docs>` system-prompt section, so
the agent can read this page when you ask about psi itself. Project settings
never change `--docs`; it reads embedded bytes only.

| Area | Keys | Notes |
|---|---|---|
| Provider/model | `defaultProvider`, `defaultModel` | `anthropic`, `openai-codex`, `minimax`, `opencode-go`, `deepseek`, `zai`, `google`, `xiaomi`, `xiaomi-token-plan-cn`, `xiaomi-token-plan-ams`, `xiaomi-token-plan-sgp`, `moonshotai`, `kimi-coding`, `qwen-token-plan`, `openrouter`, `xai`, `groq`, or `mistral` |
| Reasoning | `defaultThinkingLevel`, `modelThinkingLevels` | off/minimal/low/medium/high/xhigh/max; `modelThinkingLevels` keys are `provider/modelId` |
| Sessions | `sessionDir` | CLI > env (`PI_CODING_AGENT_SESSION_DIR`) > settings > default |
| Tools | `defaultTools` | names listed under `--tools` in `psi --help`; plain names replace, `+`/`-` modify (`powershell` is optional and Windows-only, e.g. `["-bash", "+powershell"]`) |
| Skills | `skills`, `enableSkillCommands` | skill paths; `enableSkillCommands` is parsed but not wired, so `/skill:` stays registered |
| Compaction | `compaction.enabled,reserveTokens,keepRecentTokens,modelOverrides` | bounded summaries; per-model reserve overrides; `/ac` controls are session-local |
| Queues | `steeringMode`, `followUpMode` | `one-at-a-time` (default) or `all`, as in Pi |
| Theme | `theme`, `themes`, `terminal.trueColor` | Pi theme selection, local resources, terminal color mode |
| Display | `editorPaddingX`, `outputPad`, `softWrap`, `autocompleteMaxVisible`, `markdown.codeBlockIndent`, `contextUsageDisplay` | bounded inline spacing, committed-output wrapping, code-block indent, and footer context caption |
| Reasoning display | `hideThinkingBlock` | `true` collapses reasoning blocks to one `Thinking...` label; `Ctrl+T` toggles and saves it |
| Shell | `shellPath`, `shellCommandPrefix` | `shellPath` overrides `bash` shell discovery (Windows defaults to Git Bash before `PATH`); `shellCommandPrefix` prepends `bash` commands; project values require consent |
| Network | `httpIdleTimeoutMs`, `retry.enabled`, `retry.maxRetries`, `retry.baseDelayMs`, `retry.maxAgentDelayMs` | default idle timeout 300000 ms; 0 disables it; at most 10 retries; summary retries default to 2s exponential backoff capped at 60s |
| Instructions | user/project files | see below |
| Models | provider `baseUrl`, `models[]`, `modelOverrides`, `thinkingLevelMap` | bundled catalog and Pi cache; no network refresh |
| Auth | `auth.json` entries, env refs | no `!command` execution |
| Startup | `quietStartup` | hides the Ψ banner, notes, and hint line; inline only |
| Terminal | always inline | fullscreen and rich-display preferences are ignored |

Web tools read `web-search.json`, not `settings.json`. See [Web access](web.md) for keys, enablement, and limits.

`retry.enabled` and `retry.maxRetries` control provider retries and transient summary retries. Summary backoff uses `retry.baseDelayMs` (default 2000) and `retry.maxAgentDelayMs` (default 60000). Quota, billing, and account-limit errors are not retried.

Model-specific compaction reserves use the exact `provider/model` key. For example:

```json
{
  "compaction": {
    "reserveTokens": 16384,
    "modelOverrides": {
      "anthropic/claude-example": { "reserveTokens": 32768 }
    }
  }
}
```

`/ac` policy controls are in-memory and reset when psi opens another session.

`theme` selects `system` by default, a built-in (`dark` or `light`), or an exact discovered theme name. `themes` is an array of additional local file or directory paths. `--use-theme` overrides the selection; repeat `--theme PATH` to add resources; `--no-themes` disables configured and discovered resources. Theme files are data only: schema URLs are not fetched and theme/package code is not run. Project theme bodies load only after project consent. `terminal.trueColor` accepts the booleans `true`/`false` or the string `auto`; `NO_COLOR` always disables generated SGR. `/reload` installs valid theme and display settings atomically; an invalid reload keeps the last valid snapshot. `/session` reports theme, appearance, color mode, source, and diagnostics.

`contextUsageDisplay` is a psi-specific setting for the footer context caption. It accepts `"percent"` (default) or `"tokens"`. Percentage mode shows `40.0%/200k`. Token mode shows a compact estimate such as `80k/200k`. Both modes omit the `~` and use no spaces around `/`. Both modes show the model's context window after `/`. The bar fill and color bands stay percentage-based. User settings apply first and a trusted project `.pi/settings.json` can override the value; without project consent, project settings are ignored. An invalid value, including an empty string, different case, whitespace padding, null, or a non-string value, stops startup with an error that names the setting and file. `/reload` applies a valid change and re-reads both files; a settings-parse, enum, or theme-resolution error keeps the last good display. `/reload` does not promise one transaction across unrelated resources it reloads. `/settings` writes the choice to the user `settings.json`; if that write fails, the choice stays active for the session but is not saved as a default.

`softWrap` is a boolean and defaults to `true`. It lets the terminal wrap committed output, so copying a finished paragraph does not add newlines at visual wrap points. Markdown soft line breaks become spaces; hard breaks and block boundaries stay. The live streaming tail, tables, tool rows, editor, and footer stay hard-wrapped. Blocks that exceed the 64 KiB active-source limit also stay hard-wrapped. Set `softWrap` to `false` to restore hard-wrapped committed output. Other value types, including `null`, are rejected.

`/settings` edits these values in place: auto-compact (session only), editor padding, output padding, soft wrap, autocomplete row limit, code block indent, quiet startup, theme, color mode, context usage (percent or tokens), and the default thinking level per model. The picker also opens during a run: display changes apply at once and request settings apply to the next request. The per-model row opens a model picker, then a level picker; choosing a level writes `modelThinkingLevels[provider/modelId]` and an existing entry offers `(clear override)`. A change for the current model applies at once; another model uses it on its next switch. A persisted change merges into the user `settings.json`: unknown keys, `$schema`, and sibling keys stay, top-level key order is kept, and malformed JSON is refused without writing. Project `.pi/settings.json` is never written and can still override a user value. With `--use-theme`, a theme change updates only the stored default; `--no-themes` lists built-in themes only. `quietStartup` loads `"header"` as on and writes a boolean only when you change that row. `Ctrl+T` toggles thinking-block visibility and persists `hideThinkingBlock` in the user settings file. In `/thinking`, `Ctrl+S` saves the highlighted level as the global default; `Enter` changes only the current session. Per-model and global saves write the user `settings.json` only. A project `.pi/settings.json` value can override them on the next start. A failed write for the current model keeps that session change but saves no default; a failure for another model changes nothing.

Credential references support `$VAR`, `${VAR}`, `$$`, and `$!` escapes.
Psi rejects `!command` values without running them. Stored provider API-key entries
precede `models.json` credentials, then the selected provider's environment variable:

| Provider | Environment variable |
|---|---|
| Anthropic | `ANTHROPIC_API_KEY` |
| MiniMax | `MINIMAX_API_KEY` |
| OpenCode Go | `OPENCODE_API_KEY` |
| DeepSeek | `DEEPSEEK_API_KEY` |
| Z.AI | `ZAI_API_KEY` |
| Google Generative AI | `GEMINI_API_KEY` |
| Xiaomi MiMo | `XIAOMI_API_KEY` |
| Moonshot AI | `MOONSHOT_API_KEY` |
| Kimi For Coding | `KIMI_API_KEY` |
| Qwen Token Plan | `QWEN_TOKEN_PLAN_API_KEY` |
| OpenRouter | `OPENROUTER_API_KEY` |
| xAI | `XAI_API_KEY` |
| Groq | `GROQ_API_KEY` |
| Mistral | `MISTRAL_API_KEY` |

A provider that `models.json` declares has no environment variable: its key
comes from `auth.json` or from its `apiKey` entry.

A custom provider that uses `openai-completions`, `openai-responses`, or
`anthropic-messages` can omit the key. Built-in `openai` and `anthropic` can
omit it only when an explicit provider or model `baseUrl` selects a non-default
endpoint. An omitted key sends no generated `Authorization` or `x-api-key`
header; explicit `headers` still apply, and `authHeader: true` still needs a
key. A blank, malformed, or unsupported credential stops selection instead of
falling back to a keyless request. See [Providers](providers.md#optional-keys).

An unsupported stored auth type stops key selection instead of silently changing
the billing path. Codex uses its OAuth entry and never falls back to an API key.

With no saved provider/model, the default is Anthropic `claude-sonnet-4-5`.
The public snapshot contains provenance-tracked model metadata for every registered provider. It includes API, endpoint, input, thinking, and supported compatibility data from `@earendil-works/pi-ai` version `1.5.2`.
Source hashes are in `internal/config/data/catalog-source.json`.
The snapshot contains capabilities, not credentials, private endpoints, or user defaults.
Psi also reads newer model data from Pi's `models-store.json` cache.
Pi updates this cache. Psi does not fetch model catalogs from the network.
Psi reads the cache at startup and uses it only when it is newer than the bundled snapshot.
After Pi updates the cache, restart Psi to load the new data.
Explicit models and overrides in `models.json` take precedence over the cache.
`/model` searches the local catalog and configured models without a network request.
It lists only models the session can use: a provider without a usable credential does not appear, and neither does a model whose request cannot be built. A custom provider on an eligible protocol, or a built-in `openai`/`anthropic` non-default override, can be usable without a key; other providers need one.
The bridge `pi.models.v1` list and the RPC `get_available_models` command apply the same rule.
`--list-models` reports every catalog entry that passes activation, including models whose provider is not logged in; unlike `/model`, it does not apply the request-construction filter. It does not promise that a listed model can make a request.

Provider model lists change over time. Use `/model` to select a model in the local catalog. Do not assume that an example model ID is present or available. OpenCode Go models select their own registered API and catalog endpoint.

### Catalog bake (developer)

A separate, manually run developer command produces a candidate embedded
catalog. It requires network access and explicit catalog review; no build,
install, or startup step runs it, and the agent has no tool for it.

From the checkout root, use the repo-local developer command:

```sh
go tool dev bake <new-output-directory>
```

The direct Go command remains available:

```sh
go run ./cmd/catalog-bake <new-output-directory>
```

Each bake applies reviewed token-limit overrides and warns when an override
target is missing. A missing target or a warning write failure does not block
an otherwise valid candidate. On refresh, review missing keys. Compare reviewed
context with usable source `limit.input`, not total `limit.context`, and compare
reviewed output with `limit.output`. Record missing or unusable `limit.input`
separately from a numeric disagreement.

The command fetches two unauthenticated public inputs, in this order:
`https://models.dev/experimental/v2.0/api.json` and
`https://openrouter.ai/api/v1/models`. It sends no project content or
credentials and ignores ambient proxy settings. The total budget is 120 seconds,
with at most 30 seconds per attempt. Each input allows at most three attempts,
32 MiB after decompression, and one complete JSON document. It
retries only transient failures and HTTP 408, 429, 500, 502, 503, and 504.
Redirects, TLS certificate failures, other HTTP statuses, malformed or oversized
bodies, and invalid shapes fail immediately. Invalid data is never replaced with
a stale copy.

`<new-output-directory>` is one path. Its parent must already exist, and the
target must not exist, including as a symlink or empty directory. The command
validates the complete catalog first, then writes `catalog.json` and
`catalog-source.json` into a sibling staging directory and publishes that
directory with one rename. A failure removes only its own staging directory and
leaves the requested path untouched. Errors name the failing source and HTTP
status or error category; response bodies are not printed. Help works without
network access.

`catalog-source.json` records the generator, the catalog rule revision, the
separate session-protocol reference, UTC generation and fetch times, both input
URLs and SHA-256 digests, each provider's source kind, and the catalog digest.
The two files are byte-identical when the input bytes and provenance values are
identical. A live run uses new timestamps.

The command does not modify `internal/config/data/`. Replacing the embedded
catalog is a separate reviewed change that checks the generated output,
resolved defaults, and the tracked data diff.

The embedded catalog includes a reviewed offline limit amendment on top of the
source refresh in commit `9a404afefdc2d9952f283a8fd3693de4441ce2d1`. That
source bake produced catalog digest
`2789419950f3330ef5f2e2ba61759db87886d9958485121dc3f9ee48802ad6df`. The
amendment changes only reviewed context windows and output limits. It sets the
source manifest's generator to
`catalog-bake-go-v1+reviewed-limit-amendment` and records the digest of the
amended catalog bytes. The input URLs, hashes, fetch times, and `generatedAt`
describe the original bake; the new digest describes the amended bundle. This
amendment is not a live fetch and does not change the timestamp used for cache
precedence.

Existing `modelOverrides` apply without a duplicate `models[]` entry.

Codex uses the `openai-codex` OAuth credential in `auth.json`, including token
refresh. Psi updates only that entry and preserves other providers and unknown
fields. Startup reads credentials locally; refresh occurs on submission. It never
uses `OPENAI_API_KEY` as a fallback. Run `/login openai-codex` to sign in.

Configure explicit model definitions in Pi's normal shape. The following is an Anthropic example; use the API registered for the selected provider:

```json
{
  "providers": {
    "anthropic": {
      "baseUrl": "https://api.anthropic.com/v1",
      "models": [
        {
          "id": "your-model-id",
          "api": "anthropic-messages",
          "contextWindow": 200000,
          "maxTokens": 16384,
          "reasoning": true,
          "input": ["text", "image"]
        }
      ]
    }
  }
}
```

`models.json` can also declare a provider ID that is not built in. Such a
provider uses the protocol and the endpoint it names, and it stays inside the
loaded configuration. `api` and `baseUrl` are required, `authHeader` sends the
resolved key as a bearer header, and the allowed protocols are
`anthropic-messages`, `openai-completions`, `openai-responses`,
`google-generative-ai`, and `mistral-conversations`. See
[Providers](providers.md#custom-providers).

An Anthropic thinking budget must fit below the model's output limit.
Adaptive Anthropic models use effort levels instead of token budgets.
Managed-effort models retain historical effort markers and send the current marker before the next response.
Codex reasoning uses effort names, not Anthropic token budgets.
Model `thinkingLevelMap` entries override those names.
`/thinking` lists accepted levels and states the effective effort or token budget.
An unavailable off switch appears as a provider default, not as disabled reasoning.
`/session` separates stored usage counters, estimated context, and requested versus returned models.
Prices remain unknown because psi does not calculate costs.
Skill paths resolve from their source configuration directory. User and project
skill lists combine. An empty `defaultTools` list disables tools. Project `+`/`-`
tool entries modify the inherited user selection.

## MCP settings

`mcp.allowProjectCommands` is a user-only boolean in
`<agentDir>/settings.json`. It defaults to `false`. When true, a trusted
project `mcp.json` may use whole-value `!command` interpolation in `env` and
`headers`. A project settings file cannot set or override it. MCP server
definitions themselves live in `mcp.json`, not in `settings.json`; see
[MCP servers](mcp.md). Stdio server processes receive the resolved values in
their own `env` setting, not psi's general environment. On Windows, Go may also
add `SYSTEMROOT` when the configured `env` omits it. The separate `!command`
environment is described in [MCP servers](mcp.md).
