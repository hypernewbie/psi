# Sessions

psi saves every conversation as a JSONL file unless `--no-session` is used.
A file records messages, tool calls and results, model and thinking changes,
compactions, shell executions, and session metadata. A shell entry can be the
first durable entry: psi then writes the header and setup state with it, without
creating a synthetic user prompt.

## Storage

Default location: `<agentDir>/sessions/--<workspace path>--/<session_id>.jsonl`,
with `/`, `\`, and `:` replaced by `-`. The agent directory is
`$HOME/.pi/agent` unless `PI_CODING_AGENT_DIR` or `PSI_CODING_AGENT_DIR` changes
it. New sessions, `/new`, and `/clone` use the canonical name immediately. The
file itself is created when the first session entry is written.

On Unix, psi creates new session directories and missing parents with mode
`0700`. This includes a new agent directory. Existing directory modes stay
unchanged. If psi cannot create the session directory, startup fails.

A session saved under an older timestamp-based name moves to its canonical
name when a persisted session is activated. The move holds both the old and
new writer locks, never replaces an existing file, and leaves the transcript
bytes unchanged. A name collision or a platform without a no-overwrite move
fails activation with an error instead of replacing the target. Read-only
opens and `--no-session` never rename. The old name is retired after the
move: a saved path to it no longer opens the session.

The canonical path must be a regular file. A session opened through a
symbolic link keeps its current name: renaming the link target would move the
session out of the session directory and leave the link stale. A canonical
name that is already taken by another file is reported instead of a second
session being written on top of it.

psi names a session file after its session ID. On Windows, IDs that are
reserved device names (`CON`, `PRN`, `AUX`, `NUL`, `COM1`-`COM9`,
`LPT1`-`LPT9`) cannot name a file and are rejected. Other platforms accept
them. A session file with such an ID keeps its current name instead of
moving.

Precedence for the session directory:

1. `--session-dir <dir>`
2. `PI_CODING_AGENT_SESSION_DIR`
3. `sessionDir` in `settings.json`
4. the per-workspace default above

`--no-session` keeps the conversation in memory and writes no file. `/session`
prints the active file, workspace, provider, model, thinking level, and usage.

Interactive `!command` saves one Pi v3 `bashExecution` entry with the command,
sanitized bounded output, exit or cancellation state, and truncation metadata.
It creates no user message and makes no provider request. `!!command` sets
`excludeFromContext`, so later ordinary and compaction requests omit it; the
entry stays in the local file and its output stays visible. Reopen is
storage-level: richer terminal replay and shell-history recall are not
implemented.

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

`--session` accepts a full path or a unique ID prefix. A file path uses the
working directory stored in the session. If that directory no longer exists,
psi asks whether to continue in the current directory. `--session-id` uses that
exact ID in the current workspace: the canonical `<id>.jsonl` file is tried
first without scanning the directory. A legacy filename is found by one
discovery pass only when the canonical file is absent; an existing canonical
file with a malformed header, a different ID, or a different workspace is an
error. Otherwise a new session is created with the ID.

## Inside a session

| Command | Effect |
|---|---|
| `/new` | Start a new session; scrollback stays |
| `/resume [filter]` | Open the session picker |
| `/name [name]` | Set or show the display name |
| `/session` | Print identity, workspace, model, usage |
| `/clone` | Copy the current branch into a new file |
| `/settings` | Change supported live settings |
| `/trust` | Save project trust for future sessions |
| `/quit` | Settle work and restore the terminal |

The native editor also accepts a bare `quit` or `exit` as a confirmation
keyword: submit either word again to leave, and any other submission cancels.
Use `/quit` to exit immediately or when the confirmation cannot fit. See
[Keyboard](keyboard.md).

`/clone` writes a new file and switches to it, so the original stays untouched.
The display name is stored in session metadata and shown by `/session` and the
session picker; it is not sent to the model.

### Reading earlier history

The model can use `read_session` to recover a specific fact from the current
session's raw active branch, including entries hidden by compaction or context
edits. It cannot inspect other sessions or sibling branches. It is also
available with `--no-session` and when selectable tools are disabled.

`list` returns up to 40 recent entry previews; `search` finds up to 20
case-insensitive literal matches. `read` requires a start and end entry ID from
those results and returns an excerpt in chronological order. Its `maxTokens`
uses a byte-based estimate, defaults to 4,000, and is clamped to 500–8,000.
Large ranges can continue from the reported next entry ID; oversized individual
entries are truncated to head and tail and cannot be paged within an entry.
Tool calls and their results are kept together; if a group cannot fit even after
truncation, the tool returns an error rather than silently dropping members. User and tool
result image blocks and assistant thinking blocks are excluded from the text
index. Empty searches, unknown IDs, reversed ranges, and unavailable branches
also return errors.

History may include sensitive messages and tool output. The tool reads only
this session and does not modify files or send data itself; returned text enters
the model context just like other tool results.

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
