# Use psi in the terminal

Run `psi` from the folder you want to work in. psi uses that folder to discover
instructions, configuration, and skills, and to group saved sessions. If the
binary is not built yet, follow the [Quickstart](quickstart.md).

psi may ask whether you trust the working folder before reading
`.pi/settings.json`, project skills, project themes, or a project `SYSTEM.md`.
The default answer is deny. See [Security](security.md).

The transcript shows your prompts, psi's responses, tool calls, tool results,
and errors. You write prompts and slash commands in the editor below it. The
footer shows the folder, session, model, and context usage.

## Enter a prompt

Type a request and press `Enter`.

- `Ctrl+J` inserts a newline. psi has no Shift+Enter detection.
- Bracketed paste inserts up to 256 KiB as one literal edit; pasted newlines
  never submit.
- Positional messages on the command line are sent in order after the terminal
  starts.
- `@file` arguments are rejected. Ask psi to read the file instead; the `read`
  tool is the supported path.

## Follow psi's work

Tool calls appear as they run, followed by their result. Commands and file
changes are not approved one by one; review the transcript and the diff.

- `Esc` cancels the current run.
- `/history [N|all]` reprints the last N user prompts (default 20) or the whole
  prompt history. It is display-only and never mutates the session.
- `/session` prints the session file, workspace, provider, model, thinking
  level, and usage counters.

## Change direction while psi works

The editor stays active during a run:

| What you want | Action |
|---|---|
| Adjust the current task | Type a message and press `Enter` (steering) |
| Add work after this run | Type a message and press `Alt+Enter` (follow-up) |
| Stop the run | Press `Escape` or `Ctrl+C` |

Steering is delivered at the next turn boundary, between model requests.
Follow-ups wait until the whole run ends. Pending text is shown as dim
`Steering: …` and `Follow-up: …` rows. Cancelling returns undelivered text to
the draft. Queues deliver one entry at a time in their own mode.

## Change the model or thinking level

- `/model` opens a searchable model picker for providers that already have a
  credential, or `/model [provider/]model` sets a session-local choice. `Ctrl+L`
  opens the same picker.
- `/thinking` opens the supported-level picker, or `/thinking <level>` sets one.
  `Shift+Tab` cycles the levels the selected model supports.
- `/reload` re-reads instructions, skills, and theme/display settings and
  resources. Model, tool, shell, and network settings are not reloaded.

Startup selection uses `--provider`, `--model`, and `--thinking`; see
[Command line](cli.md). Model IDs come from the bundled catalog and Pi's local
cache; psi never refreshes it over the network.

## Continue or start over

Sessions are saved automatically unless `--no-session` is used.

- `/new` starts a new session and keeps the scrollback.
- `/resume [filter]` opens the workspace session picker.
- `/name [name]` sets or shows the session display name.
- `/clone` copies the current branch into a new session file.
- `/quit` settles work and restores the terminal.

After leaving psi, run `psi --continue` from the same folder to resume the most
recent session. See [Sessions](sessions.md).

## Run a shell command

There is no `!command` prefix. Ask psi to run the command, or use your own
terminal. The `bash` tool runs in the workspace with the psi process
environment and returns the last 2000 lines or 50 KiB.

## Adjust the terminal

psi is inline in the terminal by default. `--tui-mode regular` and `--mode text`
are accepted for Pi compatibility; fullscreen mode and rich display preferences
are ignored. `--mode rpc` runs the headless JSONL interface described in
[Command line](cli.md). `/hotkeys` lists the shortcuts active in this session.
See [Keyboard](keyboard.md).
