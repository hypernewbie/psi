# Command line

psi runs interactively in a terminal by default. `--mode rpc` selects the
headless interface. There are no subcommands. Run `psi --help` for the exact
flags of the installed binary.

```sh
psi [options] [messages...]
```

Positional arguments are prompts. They are sent in order after the terminal
starts. Use `--` before a prompt that begins with `-`.

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
  `:<thinking>` suffix sets the thinking level for this run.
- `--thinking <level>` accepts `off`, `minimal`, `low`, `medium`, `high`,
  `xhigh`, or `max`.
- `--list-models [search]` prints the bundled, cached, and configured models,
  optionally filtered by fuzzy search, and exits. It never touches the network.

Provider credentials come from `auth.json`, `models.json`, or the provider's
environment variable. See [Providers](providers.md).

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
  allowlist of `read`, `bash`, `edit`, and `write`.
- `-xt`, `--exclude-tools <list>` removes names from whatever is selected.
- `-nt`, `--no-tools` disables `read`, `bash`, `edit`, and `write`.
- `-nbt`, `--no-builtin-tools` behaves like `--no-tools`.

These flags affect the four file/shell tools only. `loop_control` remains
available during an active goal or exact-count loop. `syncboard` remains
available while the connected Phi bridge accepts that capability.

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
- `-h`, `--help` prints help. `-v`, `--version` prints the version.

## Documentation

```sh
psi --docs             # list every topic
psi --docs cli         # print one topic
psi --docs=cli         # same
```

`--docs` reads the documentation embedded in the binary. It needs no settings
file, no terminal, and no network. An unknown topic fails with the topic list.
