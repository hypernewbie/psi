# Quickstart (matches binary)

Install with Go 1.25 or later (no Node, npm, or Pi packages). Go installation
requires psi v0.5.0 or later:

```sh
go install github.com/hypernewbie/psi@latest
psi --help
```

The install directory must be on `PATH`. Find it with `go env GOBIN`. If that
prints nothing, use `bin` under the first directory from `go env GOPATH`.

To build from a checkout instead:

```sh
go build -o psi .
./psi --help
./psi --version
./psi
```

On Windows, build and run as:

```sh
go build -o psi.exe .
.\psi.exe --help
```

When an example below uses `./psi`, use `psi` if you installed the binary.

## Add a credential

For ChatGPT subscription access, start psi, enter `/login`, and select
**OpenAI**. Finish signing in in your browser. No API key or Pi is required.

For OpenAI Codex subscription access, enter `/login openai-codex` and choose
**Browser login** or **Device code login**. Browser login opens a local callback
on port 1455 (1457, then an OS-assigned port if busy); if the callback cannot
return, paste the redirect URL or code into the masked sign-in field.

For API-key setup without Pi:

1. Start `./psi`.
2. Enter `/login` and select a provider, or enter `/login deepseek` directly.
3. Enter the key at the masked prompt.
4. Press `Enter` to save, or `Escape` to cancel.

psi saves a Pi-compatible API-key entry in `<agentDir>/auth.json`. Login does
not check the key with the provider. If the session is offline and the provider
has a default model, psi selects that model and saves the defaults.

Login supports 16 built-in API-key providers, excluding Anthropic. For
Anthropic, set `ANTHROPIC_API_KEY` or configure an API key in `auth.json` or
`models.json`. OpenAI Codex uses its own OAuth sign-in, not an API key. See
[Providers](providers.md) for the supported login providers and credential
precedence.

API-key providers also accept their own environment variables. For example:

```sh
export ANTHROPIC_API_KEY="..."
./psi
```

Use `/logout` to remove a stored built-in API key or OAuth credential. OAuth
logout attempts one best-effort OpenAI revocation request; local deletion
continues if that request fails, and the status says remote revocation is
unconfirmed. This does not remove environment variables or credentials in
`models.json`. If Pi uses the same `auth.json`, its next successful read sees
the deletion.

Use `/model` to select a provider and model with a usable credential. Providers
without a credential are not listed. A provider that `models.json` declares
uses a key from `auth.json` or from its `apiKey` entry instead. The `--provider`
and `--model` options accept the exact IDs shown by `/model`.

Model lists come from the bundled public snapshot and Pi's local cache. Psi
does not fetch catalogs from the network. OpenCode Go's API and endpoint are
selected per model.

Theme selection is available at startup, for example `./psi --use-theme dark` or `./psi --theme /path/to/theme.json`. Set `theme` and optional `themes` paths in `~/.pi/agent/settings.json`; use `/reload` to apply supported display/theme changes. Project themes load only after project consent. See `docs/config.md` for settings and resource rules.

Resume (every form shows current-view history before the next prompt):

```sh
./psi --continue
./psi --resume
./psi --session ~/.pi/agent/sessions/<dir>/<file>.jsonl
./psi --no-session
```

Inline commands: `/login /logout /model /thinking /name /session /hotkeys /clone /new /compact /resume /reload /quit /ac /autocompact /loop /loop-stop /skill:name`.
`/name [name]` sets or shows the session display name. `/clone` copies the current branch to a new session file.
`/resume` opens the searchable workspace picker; use arrows or PageUp/PageDown, Enter to select, Escape to cancel.
`/help`, `/version`, and `/history` are not commands. Use `./psi --help` and `./psi --version` for CLI information. Unknown slash commands are sent as ordinary prompts, like Pi.
`/ac status` shows adaptive compaction state; `/ac threshold`, `/ac end-turn`, and `/ac end-turn-delay` change session-local policy. By default, `/ac` targets the lower of 50% of context or 230K tokens, with growth forecasting, and checks completed runs at max(200K, 40%) after ten minutes. `/autocompact` is an alias. `/ac compact` entered during a run waits for it to settle, compacts, then resumes the task. `/loop goal <description>` repeats work until the model's completion claim is confirmed, `/loop <N> <task>` runs exactly N iterations, and `/loop forever <task>` repeats until you stop it. A goal loop ends each iteration when the model ends its turn; a count loop also lets the model call `loop_next` to end a pass without ending the turn, and a turn end right after `loop_next` is not counted twice. `/loop-stop` ends the active loop. Ctrl+X stops the loop and aborts its active turn.

Initial prompts are interactive. Run `./psi "Review this change"` to start a run. Psi returns to the editor when the run ends.

Common Pi aliases include `-h`, `-v`, `-c`, `-r`, `-t`, `-nt`, `-nbt`, `-a`, and `-na`. Options can appear before or after initial prompts.

`--list-models [search]` lists models from the bundled catalog, `models.json`, and Pi's local cache. It does not use the network. `--system-prompt` and repeatable `--append-system-prompt` accept text or file paths.

Psi does not support these Pi options:

- `--print/-p`, Pi's JSON event mode, or fullscreen mode.
- `@file`, `--api-key`, `--models`, `--fork`, or `--export`.
- Extensions or prompt templates.

MCP servers use `<agentDir>/mcp.json` and trusted `.pi/mcp.json`. This layer
supports stdio only; HTTP entries are unavailable. See [MCP servers](mcp.md).
`--no-mcp` disables the client for one run.

Keys: `Enter` submit, `Ctrl+J` newline, `Up/Down` visual rows (or completion/picker selection),
`Ctrl+P` model cycling, five-row slash completion, `Ctrl+L` model picker, `Shift+Tab` thinking cycle,
`Ctrl+S` saves the highlighted level in `/thinking`, `Ctrl+T` hide/show reasoning, `Ctrl+C` cancels a picker/run (idle clears, empty exits),
`Ctrl+D` delete/exit, paste literal, `Escape` dismisses completion or cancels a picker.

Cancel a run, then keep typing: `Ctrl+C` stops provider/tool/compact work with
settled cleanup and truthful transcript; next prompt appears after persistence.

Tool calls from one response run concurrently and settle in call order.
Same-file writes and edits serialize; unrelated calls overlap.
See `docs/limitations.md` for the execution model and Pi deviations.

Recovery: interrupted interior records are skipped and the session continues after
that gap. Truncated tails and locked sessions preserve original bytes; use a copy,
`/new`, or `/resume` when recovery is needed. See `docs/recovery.md`.

See `COMPATIBILITY.md` for the pinned baseline, `docs/keyboard.md` for key precedence, and `docs/ui-acceptance.md` for UI evidence and remaining gates.

New here? Start with [the psi doc map](index.md).
Every topic is embedded in the binary: `./psi --docs` lists them and
`./psi --docs quickstart` prints this page.
