# Environment variables

This page covers provider credentials, directories, shell selection, and display.
For the private Phi bridge variables, see [Bridge](bridge.md).

## Provider credentials

Each provider has exactly one variable. A stored `auth.json` entry or a
`models.json` credential takes precedence; the variable is the fallback.

| Variable | Provider |
|---|---|
| `ANTHROPIC_API_KEY` | `anthropic` |
| `MINIMAX_API_KEY` | `minimax` |
| `OPENCODE_API_KEY` | `opencode-go` |
| `DEEPSEEK_API_KEY` | `deepseek` |
| `ZAI_API_KEY` | `zai` |
| `XIAOMI_API_KEY` | `xiaomi` |
| `MOONSHOT_API_KEY` | `moonshotai` |
| `KIMI_API_KEY` | `kimi-coding` |
| `QWEN_TOKEN_PLAN_API_KEY` | `qwen-token-plan` |
| `OPENROUTER_API_KEY` | `openrouter` |
| `XAI_API_KEY` | `xai` |
| `GROQ_API_KEY` | `groq` |
| `MISTRAL_API_KEY` | `mistral` |
| `GEMINI_API_KEY` | `google` |

`openai-codex` has no environment variable. It uses the ChatGPT OAuth entry in
`auth.json`. See [Providers](providers.md).

A provider that `models.json` declares has no environment variable. Its key
comes from `auth.json` or from its `apiKey` entry in `models.json`, in that
order.

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
