# Quickstart (matches binary)

Build with Go alone (no Node, npm, or Pi packages):

```sh
go build -o psi ./cmd/psi
./psi --help
./psi --version
./psi
```

Daily use (Anthropic Messages, ChatGPT Codex Responses, and the registered API-key providers):

```sh
export ANTHROPIC_API_KEY="..."
./psi
```

Use `/model` to select a locally available provider and model. Each API-key
provider needs its own environment variable; see `docs/config.md`. The `--provider`
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

Inline commands: `/help /hotkeys /new /resume /model /thinking /compact /session /history /name /clone /skill:name /quit`.
`/name [name]` sets or shows the session display name. `/clone` copies the current branch to a new session file.
`/resume [filter]` opens the searchable workspace picker; use arrows or PageUp/PageDown, Enter to select, Escape to cancel.
`/history [N|all]` is display-only (default 20).

Keys: `Enter` submit, `Ctrl+J` newline, `Up/Down` visual rows (or completion/picker selection),
`Ctrl+P/N` history, five-row slash completion, `Ctrl+L` model picker, `Shift+Tab` thinking cycle,
`Ctrl+C` cancels a picker/run (idle clears, empty exits), `Ctrl+D` delete/exit, paste literal,
`Escape` dismisses completion or cancels a picker.

Cancel a run, then keep typing: `Ctrl+C` stops provider/tool/compact work with
settled cleanup and truthful transcript; next prompt appears after persistence.

Recovery: truncated/malformed/locked sessions refuse writes with original bytes
intact; use a copy, `/new`, or `/resume`. See `docs/recovery.md`.

See `COMPATIBILITY.md` for the pinned baseline, `docs/keyboard.md` for key precedence, and `docs/ui-acceptance.md` for UI evidence and remaining gates.
