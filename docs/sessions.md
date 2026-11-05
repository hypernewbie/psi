# Sessions

psi saves every conversation as a JSONL file unless `--no-session` is used.
A file records messages, tool calls and results, model and thinking changes,
compactions, and session metadata.

## Storage

Default location: `<agentDir>/sessions/--<workspace path>--/<timestamp>.jsonl`,
with `/`, `\`, and `:` replaced by `-`. The agent directory is
`$HOME/.pi/agent` unless `PI_CODING_AGENT_DIR` or `PSI_CODING_AGENT_DIR` changes
it.

Precedence for the session directory:

1. `--session-dir <dir>`
2. `PI_CODING_AGENT_SESSION_DIR`
3. `sessionDir` in `settings.json`
4. the per-workspace default above

`--no-session` keeps the conversation in memory and writes no file. `/session`
prints the active file, workspace, provider, model, thinking level, and usage.

## Resume

```sh
psi --continue            # most recent session for this folder
psi --resume              # pick from sessions in this folder
psi --session <path|id>   # open a file or an ID prefix
psi --session-id <id>     # reuse or create this session ID
psi --session <path> --name "release prep"
```

`--resume` opens a searchable picker. `Up/Down` selects, `PageUp/PageDown`
moves by page, `Enter` opens, `Esc` cancels. Opening the session you are already
in is a no-op.

`--session` accepts a full path or a unique ID prefix. If the session belongs to
a different workspace, psi asks before continuing there. `--session-id` uses
that exact ID in the current workspace: an existing session is reused, otherwise
a new one is created with it.

## Inside a session

| Command | Effect |
|---|---|
| `/new` | Start a new session; scrollback stays |
| `/resume [filter]` | Open the session picker |
| `/name [name]` | Set or show the display name |
| `/session` | Print identity, workspace, model, usage |
| `/clone` | Copy the current branch into a new file |
| `/history [N\|all]` | Reprint user prompts; display only |
| `/quit` | Settle work and restore the terminal |

`/clone` writes a new file and switches to it, so the original stays untouched.
The display name is stored in session metadata and shown by `/session` and the
session picker; it is not sent to the model.

## Format

psi writes Pi session version 3. Reading:

- v1 and v2 are accepted for diagnosis only; psi does not migrate them.
- A version newer than 3 is refused rather than rewritten.
- Branch entries project onto the active path; sibling branches stay out of
  context.
- Content edits use `replacement: {content} | null`, latest wins.
- Tool results stay in the file even though the terminal shows only a short
  status row.

The full compatibility matrix is in `COMPATIBILITY.md`. Recovery from
truncated, malformed, or concurrently written files is covered in
[Recovery](recovery.md).

## Sharing a file

A session file contains prompts, tool output, file contents, and anything a
command printed. It is as sensitive as the work it describes. A concurrent psi
process cannot write the same session; the file is locked. Pi does not honor
that lock, so never let Pi and psi write one file at the same time.
