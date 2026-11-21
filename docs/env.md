# Environment variables

This page covers provider credentials, directories, shell selection, and display.
For the private Phi bridge variables, see [Bridge](bridge.md).

## Provider credentials

Each provider has exactly one variable. A stored `auth.json` entry or a
`models.json` credential takes precedence; the variable is the fallback.

A custom provider that uses `openai-completions`, `openai-responses`, or
`anthropic-messages` can omit the key. Built-in `openai` and `anthropic` can
omit it only when `models.json` sets a provider or model `baseUrl` to a
non-default endpoint. A blank variable on such an override is an error, not
permission to omit the key. See
[Providers](providers.md#optional-keys).

| Variable | Provider |
|---|---|
| `ANTHROPIC_API_KEY` | `anthropic` |
| `MINIMAX_API_KEY` | `minimax` |
| `OPENCODE_API_KEY` | `opencode-go` |
| `OPENAI_API_KEY` | `openai` |
| `DEEPSEEK_API_KEY` | `deepseek` |
| `ZAI_API_KEY` | `zai` |
| `XIAOMI_API_KEY` | `xiaomi` |
| `XIAOMI_TOKEN_PLAN_CN_API_KEY` | `xiaomi-token-plan-cn` |
| `XIAOMI_TOKEN_PLAN_AMS_API_KEY` | `xiaomi-token-plan-ams` |
| `XIAOMI_TOKEN_PLAN_SGP_API_KEY` | `xiaomi-token-plan-sgp` |
| `MOONSHOT_API_KEY` | `moonshotai` |
| `KIMI_API_KEY` | `kimi-coding` |
| `QWEN_TOKEN_PLAN_API_KEY` | `qwen-token-plan` |
| `OPENROUTER_API_KEY` | `openrouter` |
| `XAI_API_KEY` | `xai` |
| `GROQ_API_KEY` | `groq` |
| `MISTRAL_API_KEY` | `mistral` |
| `GEMINI_API_KEY` | `google` |

`openai-codex` has no environment variable. It uses the separate Codex OAuth
entry in `auth.json`; run `/login openai-codex` to create it. See
[Providers](providers.md).

`openai` also accepts a ChatGPT OAuth login from `/login openai`. A stored
`openai` entry, including an invalid or expired OAuth login, never falls back
to `OPENAI_API_KEY`; only a missing entry permits `models.json`, then the
variable.

A provider that `models.json` declares has no environment variable. Its key
comes from `auth.json` or from its `apiKey` entry in `models.json`, in that
order. It can omit the key when it uses one of the three protocols noted above.

## Directories

| Variable | Effect |
|---|---|
| `PI_CODING_AGENT_DIR` | Agent directory: settings, `models.json`, `auth.json`, skills, sessions |
| `PSI_CODING_AGENT_DIR` | Same, with higher precedence |
| `PI_CODING_AGENT_SESSION_DIR` | Default session directory; `--session-dir` wins |

Default agent directory: `$HOME/.pi/agent`. Sessions default to
`<agentDir>/sessions/--<workspace path>--`, grouped per working folder.
`HOME` (`USERPROFILE` on Windows) is what resolves those paths when the
overrides are unset.

## Web access

| Variable | Effect |
|---|---|
| `SEARXNG_BASE_URL` | Overrides `searxngBaseUrl`; empty leaves SearXNG unconfigured |
| `EXA_BASE_URL` | Overrides `exaBaseUrl`; empty is invalid |
| `EXA_API_KEY` | Overrides a configured literal Exa key |
| `CRAWL4AI_BASE_URL` | Overrides a nonempty `crawl4aiBaseUrl`; empty or whitespace-only uses the file value |
| `CRAWL4AI_API_TOKEN` | Overrides a configured literal Crawl4AI token; empty or whitespace-only uses the file value |
| `PI_WEB_ACCESS_ALLOW_HTTP_BASE_URLS` | `true`, `false`, `1`, or `0`; allows plain-HTTP provider base URLs |

These variables affect `web-search.json` only. They also apply when the file is absent. See [Web access](web.md).

A configured `$NAME` or `${NAME}` Crawl4AI token is resolved before
`CRAWL4AI_API_TOKEN`. A missing or empty reference disables extraction; the
environment token does not rescue it. An empty or whitespace-only
`CRAWL4AI_API_TOKEN` uses the file's literal token. With no token from either
source, psi calls the service without an `Authorization` header.

## Shell

| Variable | Effect |
|---|---|
| `PSI_SHELL` | Fallback shell for the `bash` tool when `shellPath` is unset; default `bash` |

`shellPath` selects the shell first. When it is empty, psi uses `PSI_SHELL`, then
`bash`. `shellCommandPrefix` is prepended to each command with a newline. Project
settings for either key require project consent.

The `bash` tool inherits the whole environment, including provider keys. A
command that dumps the environment writes secrets into the session.

## Display

| Variable | Effect |
|---|---|
| `NO_COLOR` | Set at all disables generated color |
| `COLORFGBG` | Hint for light or dark terminal detection |
| `TERM` | Terminal capability detection |

`NO_COLOR` overrides `terminal.trueColor`. Theme selection still applies; only
SGR color is withheld.

## Not supported

Pi reads variables psi does not: `PI_OFFLINE`, `PI_PACKAGE_DIR`,
`PI_SELF_UPDATE_COMMAND`, `PI_EXTENSION_*`, and MCP-related settings. The
`--offline` flag exists only for one-run compatibility and changes nothing,
because psi never fetches catalogs. There is no variable that enables print
mode, extensions, or fullscreen mode; `--mode rpc` is a flag, not a variable.
