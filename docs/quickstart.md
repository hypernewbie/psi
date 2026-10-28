# Quickstart (matches binary)

Build with Go alone (no Node, npm, or Pi packages):

```sh
go build -o psi ./cmd/psi
./psi --help
./psi --version
./psi
```

Daily use (Anthropic Messages and ChatGPT Codex Responses):

```sh
export ANTHROPIC_API_KEY="..."
./psi --provider anthropic --model claude-sonnet-4-5
./psi --provider openai-codex --model gpt-5.6-luna
```

Resume (every form shows current-view history before the next prompt):

```sh
./psi --continue
./psi --resume
./psi --session ~/.pi/agent/sessions/<dir>/<file>.jsonl
./psi --no-session
```

Inline commands: `/help /new /resume /model /thinking /compact /session /history /skill:name /quit`.
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
