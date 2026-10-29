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
psi --tools read,bash,edit,write
psi --no-tools
psi --use-theme NAME
psi --theme PATH
psi --no-themes
psi --no-session
psi --help
psi --version
```

Project consent: `--allow-project` / `--deny-project`. Otherwise an inline
choice appears before psi reads `.pi/settings.json`, project skills/themes/packages,
or project `SYSTEM.md`/`APPEND_SYSTEM.md`. The default is deny; Escape and Ctrl+C also deny.
The choice applies to this run only. Context files such as `AGENTS.md` load
separately, as in Pi. User instructions come from the agent directory and
ancestor folders.

| Area | Keys | Notes |
|---|---|---|
| Provider/model | `defaultProvider`, `defaultModel` | `anthropic`, `openai-codex`, `minimax`, `opencode-go`, `deepseek`, `zai`, or `google` |
| Reasoning | `defaultThinkingLevel` | off/minimal/low/medium/high/xhigh/max |
| Sessions | `sessionDir` | CLI > env (`PI_CODING_AGENT_SESSION_DIR`) > settings > default |
| Tools | `defaultTools` | four tools; plain replaces, `+`/`-` modify |
| Skills | `skills`, `enableSkillCommands` | paths + selector |
| Compaction | `compaction.enabled,reserveTokens,keepRecentTokens` | bounded summaries |
| Theme | `theme`, `themes`, `terminal.trueColor` | Pi theme selection, local resources, terminal color mode |
| Display | `editorPaddingX`, `outputPad`, `autocompleteMaxVisible`, `markdown.codeBlockIndent` | bounded inline spacing and code-block indent |
| Shell | `shellPath`, `shellCommandPrefix` | consent for project executables |
| Network | `httpIdleTimeoutMs`, `retry.enabled`, `retry.maxRetries` | default idle timeout 300000 ms; 0 disables it; at most 10 retries |
| Instructions | user/project files | see below |
| Models | provider `baseUrl`, `models[]`, `modelOverrides`, `thinkingLevelMap` | bundled catalog and Pi cache; no network refresh |
| Auth | `auth.json` entries, env refs | no `!command` execution |
| Startup | `quietStartup` | inline only |
| Terminal | always inline | fullscreen and rich-display preferences are ignored |

`theme` selects `system` by default, a built-in (`dark` or `light`), or an exact discovered theme name. `themes` is an array of additional local file or directory paths. `--use-theme` overrides the selection; repeat `--theme PATH` to add resources; `--no-themes` disables configured and discovered resources. Theme files are data only: schema URLs are not fetched and theme/package code is not run. Project theme bodies load only after project consent. `terminal.trueColor` accepts the booleans `true`/`false` or the string `auto`; `NO_COLOR` always disables generated SGR. `/reload` installs valid theme and display settings atomically; an invalid reload keeps the last valid snapshot. `/session` reports theme, appearance, color mode, source, and diagnostics.

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

An unsupported stored auth type stops key selection instead of silently changing
the billing path. Codex uses its OAuth entry and never falls back to an API key.

With no saved provider/model, the default is Anthropic `claude-sonnet-4-5`.
The public snapshot contains provenance-tracked model metadata for the seven registered providers. It includes API, endpoint, input, thinking, and supported compatibility data from `@earendil-works/pi-ai` version `1.4.0`.
Source hashes are in `internal/config/data/catalog-source.json`.
The snapshot contains capabilities, not credentials, private endpoints, or user defaults.
Psi also reads newer model data from Pi's `models-store.json` cache.
Pi updates this cache. Psi does not fetch model catalogs from the network.
Psi reads the cache at startup and uses it only when it is newer than the bundled snapshot.
After Pi updates the cache, restart Psi to load the new data.
Explicit models and overrides in `models.json` take precedence over the cache.
`/model` searches the local catalog and configured models without a network request.

Provider model lists change over time. Use `/model` to select a model in the local catalog. Do not assume that an example model ID is present or available. OpenCode Go models select their own registered API and catalog endpoint; unsupported cached APIs remain visible as unavailable.

Existing `modelOverrides` apply without a duplicate `models[]` entry.

Codex uses the `openai-codex` OAuth credential in `auth.json`, including token
refresh. Psi updates only that entry and preserves other providers and unknown
fields. Startup reads credentials locally; refresh occurs on submission. It never
uses `OPENAI_API_KEY` as a fallback. Run `/login openai-codex` in Pi to log in.

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
