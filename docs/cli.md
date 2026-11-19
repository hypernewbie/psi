# Command line

psi runs interactively in a terminal by default. `--mode rpc` selects the
headless interface. There are no subcommands. Run `psi --help` for the exact
flags of the installed binary.

```sh
psi [options] [messages...]
```

Positional arguments are prompts. They are sent in order after the terminal
starts. Use `--` before a prompt that begins with `-`.

ChatGPT and OpenAI Codex sign-in are interactive-only: select OpenAI or
OpenAI Codex from `/login`, or enter `/login openai` / `/login openai-codex`
directly. Positional messages and RPC `prompt` commands never sign in; they
keep their existing prompt-data contract.

The built-in `openai-codex` provider periodically reads its used primary
ChatGPT quota and shows the remaining percentage in unused footer space when
available. It backs off after failed reads. The reading is best effort: it uses
the existing local OAuth entry without signing in, refreshing credentials, or
changing inference.

## Invocation

```sh
psi
psi "review the diff in this folder"
psi --continue
psi --mode rpc
```

psi has two modes. The default is interactive, inline, and needs a terminal on
stdin; redirected stdin fails with `noninteractive input: psi needs a terminal
(no print/RPC mode)`. `--mode rpc` runs headless, reading JSONL commands on
stdin and writing JSONL events on stdout: `prompt`, `abort`, `get_state`,
`get_available_models`, `set_model`, `get_available_thinking_levels`,
`set_thinking_level`, `compact`, `get_session_stats`, and
`extension_ui_response`. `get_available_models` lists only providers with a
usable credential, the same rule as `/model`. `get_available_thinking_levels`
lists the live model's advertised levels; `set_thinking_level` accepts any
level the model can apply and clamps it when the model does not advertise it.
`prompt` expands a `/skill:<name> [args]` message before any user event, queue,
store, or provider side effect. An unknown, unreadable, or oversized skill
returns an error response instead of submitting the unexpanded command.
It has no terminal UI, theme, or picker.

Print mode (`--print/-p`), Pi's JSON event mode, HTML export, and `@file`
arguments are not supported.

`--session-id <id>` reuses the session with that ID in this workspace, or
creates it. `--session <path|id>` opens an existing file or ID prefix.

## Models

```sh
psi --provider anthropic --model claude-sonnet-4-5:high
psi --list-models sonnet
```

- `--provider <name>` selects a provider. Without `--model` the configured
  default model for that provider is used. A provider that `models.json`
  declares works here too.
- `--model <id|provider/id>` selects a model ID or a qualified ID. An optional
  `:<thinking>` suffix overrides saved thinking defaults for the initial
  selection.
- `--thinking <level>` accepts `off`, `minimal`, `low`, `medium`, `high`,
  `xhigh`, or `max`. It overrides saved thinking defaults for the initial
  selection; a later model change resolves the per-model and global defaults
  again.
- `--list-models [search]` prints the bundled, cached, and configured models,
  optionally filtered by fuzzy search, and exits. It never touches the network.

Provider credentials come from `auth.json`, `models.json`, or the provider's
environment variable. See [Providers](providers.md).

## Interactive login

Login and logout require an interactive terminal and a writable active session.
They are unavailable during a run, picker, compaction, or reload. RPC and
noninteractive clients must use environment variables or pre-written credentials.

Use `/login [provider]` in an interactive session. With no provider argument,
psi opens a searchable provider picker. Select OpenAI (or enter `/login openai`)
to sign in with a ChatGPT subscription. Select OpenAI Codex (or enter
`/login openai-codex`) to choose **Browser login** or **Device code login**.

Codex browser login binds `127.0.0.1:1455`, then `127.0.0.1:1457` if that port
is busy, then one OS-assigned port. If the browser cannot return to psi, paste
the redirect URL, `code#state`, query string, or bare code into the masked
sign-in field. The paste must be one line of at most 8 KiB. If it includes
a state value, that value must match the current attempt. The paste never
enters chat or recall. An OS-assigned port is a best-effort fallback: OpenAI
can reject that redirect. If it does, close the tab, cancel, and choose
device code. Device code shows a user code and
`https://auth.openai.com/codex/device`; availability depends on the account.
While psi waits for approval, press `Esc` to cancel sign-in or `Ctrl+D` to
cancel and quit psi.

For other supported providers, the prompt masks the API key; do not put keys
in command arguments. API-key login supports `deepseek`, `google`, `groq`,
`kimi-coding`, `minimax`, `mistral`, `moonshotai`, `opencode-go`, `openrouter`,
`qwen-token-plan`, `xai`, `xiaomi`, `xiaomi-token-plan-ams`,
`xiaomi-token-plan-cn`, `xiaomi-token-plan-sgp`, and `zai`. It does not support
Anthropic API-key login. Use an environment variable or `models.json` for
Anthropic. OpenAI Codex has no API-key fallback.

API-key login stores the key in `<agentDir>/auth.json`. The agent directory is
`PSI_CODING_AGENT_DIR`, then `PI_CODING_AGENT_DIR`, then
`~/.pi/agent`. A stored key takes precedence over a `models.json` key and the
provider's environment variable. An existing `auth.json` symlink is preserved:
psi locks the logical path and atomically replaces the resolved regular-file
target. Broken or nonregular targets fail. See [Security](security.md) for
target-race and memory-cleanup limits. psi does not validate the key during
login; the first provider request can still fail. If the session is offline and
the provider has a default model, login selects it and saves the default
provider and model. Otherwise, the key is saved without changing the current
model. If model selection or defaults persistence fails after the key is saved,
psi reports the failure and keeps the saved key.

Use `/logout` to select and remove a stored built-in API key from
`auth.json`. This includes Anthropic API keys. It does not remove OAuth
credentials, environment variables, or `models.json` credentials. If the key
belongs to the active provider, psi reloads that provider from the remaining
credential sources. If none is available, psi goes offline until you configure
a credential again. `/logout` does not accept an argument.

## Sessions

```sh
psi --continue
psi --resume
psi --session ~/.pi/agent/sessions/--work-proj--/0193f2b1-7c4a-7d1e-9f0a-3b6c8d2e5a01.jsonl
```

- `-c`, `--continue` resumes the most recent session for this folder.
- `-r`, `--resume` opens the session picker for this folder.
- `--session <path|id>` opens a session file or an ID prefix.
- `--session-id <id>` reuses that session ID in this workspace or creates it.
- `--session-dir <dir>` overrides the session directory for this run.
- `--no-session` keeps the session in memory and writes no file.
- `-n`, `--name <name>` sets the session display name.

Forking and session export are not supported. Use `/clone` to copy a session.
See [Sessions](sessions.md).

## Tools

```sh
psi --tools read,bash
psi --exclude-tools bash
psi --no-tools
```

- `-t`, `--tools <list>` replaces the default selection with a comma-separated
  allowlist. Run `psi --help` to see the names under `--tools`.
- `-xt`, `--exclude-tools <list>` removes names from whatever is selected.
- `-nt`, `--no-tools` disables all selectable tools.
- `-nbt`, `--no-builtin-tools` behaves like `--no-tools`.

These flags apply only to the tools listed under `--tools`. Web tools honor the
`web-search.json` switches described in [Web access](web.md). Excluding
`fetch_content` also blocks `web_search` with `includeContent: true` before any
network request; metadata-only search remains available. A readable
`fetch_content` call may use a configured Crawl4AI fallback (`crawl4aiBaseUrl`)
after a direct failure; `raw` mode is direct HTTP only. `loop_done`/`loop_approve`
remains available during an active goal loop (one at a time, by phase). `read_session`
is always available, independently of these flags, and reads bounded excerpts from
the raw active branch, including entries hidden by compaction or context edits.
`syncboard` remains available while the connected Phi bridge accepts that capability.

`defaultTools` in `settings.json` accepts the same names, plus `+name` and
`-name` entries that add to or remove from the default set.

## Resources

```sh
psi --theme ./themes/custom.json --use-theme custom
psi --no-skills --no-context-files
```

- `--theme <path>` loads an extra theme file or directory. Repeatable.
- `--use-theme <name>` selects the initial theme.
- `--no-themes` disables configured and discovered themes.
- `-ns`, `--no-skills` disables skill discovery.
- `-nc`, `--no-context-files` disables instruction discovery entirely:
  `AGENTS.md`, `AGENTS.override.md`, `CLAUDE.md`, `SYSTEM.md`, and
  `APPEND_SYSTEM.md`, including the agent-directory copies.

Project skills, themes, settings, and `.pi/SYSTEM.md` / `.pi/APPEND_SYSTEM.md`
load only after project consent. Context files such as `AGENTS.md` load without
consent. psi has no extension runtime, MCP client, skill-file flag, or prompt templates,
so the Pi flags that load them do not exist.

## Prompts and process

- `--system-prompt <text|file>` replaces the base preamble. A path that exists
  is read as a file.
- `--append-system-prompt <text|file>` appends a section, repeatable.
- `-a`, `--approve` trusts project-local resources for this run.
- `-na`, `--no-approve` denies them. Escape and `Ctrl+C` at the prompt also
  deny.
- `--offline` is accepted and does nothing; psi never refreshes catalogs.
- `-h`, `--help` prints help. `-v`, `--version` prints three lines: the release,
  the commit stamp Go embeds at build time, and the build toolchain with platform.
  A build with no stamp, for example one made in a Git worktree where `.git` is a
  file rather than a directory, prints `commit unknown (no VCS stamp)`.

## Documentation

```sh
psi --docs             # list every topic
psi --docs cli         # print one topic
psi --docs=cli         # same
```

`--docs` reads the documentation embedded in the binary. It needs no settings
file, no terminal, and no network. An unknown topic fails with the topic list.
