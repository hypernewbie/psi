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
| Provider/model | `defaultProvider`, `defaultModel` | `anthropic` or `openai-codex` |
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
| Models | provider `baseUrl`, `models[]`, `modelOverrides`, `thinkingLevelMap` | local metadata only; no catalog refresh |
| Auth | `auth.json` entries, env refs | no `!command` execution |
| Startup | `quietStartup` | inline only |
| Terminal | always inline | fullscreen and rich-display preferences are ignored |

`theme` selects `system` by default, a built-in (`dark` or `light`), or an exact discovered theme name. `themes` is an array of additional local file or directory paths. `--use-theme` overrides the selection; repeat `--theme PATH` to add resources; `--no-themes` disables configured and discovered resources. Theme files are data only: schema URLs are not fetched and theme/package code is not run. Project theme bodies load only after project consent. `terminal.trueColor` accepts the booleans `true`/`false` or the string `auto`; `NO_COLOR` always disables generated SGR. `/reload` installs valid theme and display settings atomically; an invalid reload keeps the last valid snapshot. `/session` reports theme, appearance, color mode, source, and diagnostics.

Credential references support `$VAR`, `${VAR}`, `$$`, and `$!` escapes.
Psi rejects `!command` values without running them. Stored Anthropic credentials
precede `models.json` credentials, then `ANTHROPIC_API_KEY`. An OAuth entry stops
API-key selection instead of changing the billing path silently.

With no saved provider/model, the default is Anthropic `claude-sonnet-4-5`.
The public snapshot contains 17 Anthropic models and seven Codex models from `@earendil-works/pi-ai` version `1.4.0`.
Source hashes are in `internal/config/data/catalog-source.json`.
The snapshot contains capabilities, not credentials, private endpoints, or user defaults.
`/model` searches the local catalog and configured models without a network request.

A saved Codex selection uses the GPT-5.6 and GPT-6 families.
Its default is `gpt-5.6-luna`.
Existing `modelOverrides` apply without a duplicate `models[]` entry.

Codex uses the `openai-codex` OAuth credential in `auth.json`, including token
refresh. Psi updates only that entry and preserves other providers and unknown
fields. Startup reads credentials locally; refresh occurs on submission. It never
uses `OPENAI_API_KEY` as a fallback. Run `/login openai-codex` in Pi to log in.

Configure other compatible models in Pi's normal shape:

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
