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

## Read context usage

When a context estimate and window are available, the second footer row shows
stored usage counters first, then a compact estimate such as `5.7%/1.0M`. A
width-adaptive bar can sit between them.

- The bar has 4 to 16 track cells between two brackets. With the brackets it
  uses 6 to 18 cells. It grows continuously and does not snap to preset
  lengths.
- Green marks less than 40%, yellow marks 40% to less than 70%, and red marks
  70% or more. Empty track cells stay dim.
- Usage, brackets, track, caption, and the ` (auto)` marker must fit in the
  left half of the terminal. If they do not, psi omits the bar.
- `contextUsageDisplay: "percent"` (default) shows `40.0%/200k`. Set it to
  `"tokens"` to show a compact estimate such as `80k/200k`. Both modes omit
  the `~` and use no spaces around `/`. The token count is an estimate, not an
  exact tokenizer result.
- The bar still fills and changes color by percentage in both modes. A token
  count above the window is shown without text clamping.
- Change the setting in `/settings` under `Context usage`, or in the user
  `settings.json` followed by `/reload`. A trusted project `.pi/settings.json`
  can override it. If the window is unknown or the estimate is unavailable,
  psi omits the caption.
- The text-only footer keeps the complete estimate even when it extends past
  the midpoint. At 80 columns the bar uses a 16-cell track, for example
  `↑57k [▉░░░░░░░░░░░░░░░] 5.7%/1.0M (auto)`. At 40 columns the bar is omitted
  and the 21-cell text `↑57k 5.7%/1.0M (auto)` ends one cell past the midpoint.
- Counters stay first. At the full wrap-safe row width, they can use the room
  that the context label needs, so the context label can disappear.
- Sizing uses only the terminal width. psi does not request provider data to
  draw the bar.

## Enter a prompt

Type a request and press `Enter`. An empty `Enter` retries an unfinished turn (failed, interrupted, or recovered from a crash) and is ignored otherwise. `/b` and `/bump` request another response from the current context, including after a completed or output-limited answer, without adding a prompt.

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

- `Esc` cancels the current run, including the wait before an automatic retry.
- `/session` prints the session file, workspace, provider, model, thinking
  level, and usage counters.

## Retry a failed turn or ask for another response

Psi retries a failed response by itself first. A transient provider or network
failure, or a stream that drops before it completes, is retried up to three
times. psi prints the cause and a countdown, and `Esc` cancels the wait. See
[Recovery](recovery.md) for the rules.

When the turn still ends without an answer, psi can retry it without adding
another prompt. Press `Enter` on an empty editor or use
`/b` or `/bump`. Explicit commands can also request another response after a
completed or output-limited answer. A successful bump uses the current
model, thinking level, tools, system prompt, and compaction policy, and keeps
saved answers in the transcript.

Bump is available only while psi is idle, with no run, picker, or compaction
active. It keeps the normal auth, storage, and continuation checks. Empty
`Enter` works only for an unfinished turn; explicit commands bypass that gate
but keep all safety checks. While a shell command is running, psi refuses an
explicit bump and restores it to the draft. Other admission refusals stay
silent and do not queue or restore the command. Once a run starts, failures,
cancellation, partial output, and unsaved output use the normal message
behavior.

Visible partial output from the failed stream stays in the transcript. The
saved failure record is hidden and omitted from ordinary continuation
requests, though existing compaction can summarize visible partial progress.
A crash can hide whether a tool already ran: the retry sends a synthetic
missing-result marker for that call and never re-executes the old tool batch.
A retry can duplicate provider work and billing. Bump is interactive-only; RPC
and the Phi bridge do not expose it.

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
the draft. `steeringMode` and `followUpMode` pick `one-at-a-time` (default,
one message per turn) or `all`.

A bare `quit` or `exit` during a run only shows the confirmation. A second bare
keyword releases the terminal first and cancels the run, like `/quit`; any other
submission cancels the confirmation and follows its normal path.

## Change the model or thinking level

- `/model` opens a searchable model picker for providers that already have a
  credential, or `/model [provider/]model` sets a session-local choice. `Ctrl+L`
  opens the same picker.
- `/thinking` opens the supported-level picker, or `/thinking <level>` sets one.
  `Shift+Tab` cycles the levels the selected model supports. `Ctrl+S` in the
  picker saves the highlighted level as the global default.
- `/settings` sets a per-model default under `Default thinking level per model`.
  The current model uses a new default at once; other models use theirs on the
  next switch.
- `/model`, `/thinking`, and `/settings` work during a run. The run is
  not cancelled; request settings apply to the next request. `/session`,
  `/hotkeys`, and `/name` also run immediately without cancelling the run.
  Unknown slash text queues like ordinary prompt text: `Enter` for steering
  and `Alt+Enter` for follow-up.
- An explicit CLI, restored, or `/thinking` level applies to that selection.
  A later model change applies `modelThinkingLevels[provider/modelId]` first,
  then `defaultThinkingLevel`, then the carried level. An unadvertised level
  is clamped to the nearest supported level.
- `/reload` re-reads themes, display settings, prompt inputs (including
  context files), skills, and `steeringMode`/`followUpMode`. New queue modes
  apply to the next run. It refreshes the local model catalog, including
  `models.json`, without changing the selected model. If `models.json` is
  malformed or unreadable, psi warns and keeps the previous catalog. It also
  re-reads MCP configuration in the background unless MCP is disabled. It
  does not change keybindings or reload built-in tool, shell, or web settings.

Startup selection uses `--provider`, `--model`, and `--thinking`; see
[Command line](cli.md). Model IDs come from the bundled catalog and Pi's local
cache; psi never refreshes it over the network.

## Read or hide reasoning

When the model streams reasoning, psi shows it as an italic block before the
answer. `Ctrl+T` hides the block behind one `Thinking...` label and shows it
again on the next press. The toggle also works during a run and is saved as
`hideThinkingBlock` in the agent `settings.json`, so the choice survives a
restart.

Reasoning stays in the session file, so a resumed or replayed session shows the
same blocks in the same order. A run that ends early keeps the reasoning that
arrived before the interruption.

## Continue or start over

Sessions are saved automatically unless `--no-session` is used.

- `/new` starts a new session and keeps the scrollback.
- `/resume [filter]` opens the workspace session picker.
- `/name [name]` sets or shows the session display name.
- `/clone` copies the current branch into a new session file.
- `/quit` releases the terminal first, then settles and exits.

The native editor also accepts a bare `quit` or `exit` as a confirmation
keyword. Submit either word again to exit; any other submission cancels the
hint. Use `/quit` to exit immediately or when the hint cannot fit. See
[Keyboard](keyboard.md).

After leaving psi, run `psi --continue` from the same folder to resume the most
recent session. See [Sessions](sessions.md).

## Run a shell command

In the editor, `!command` runs a local shell command in the session working
directory. psi trims the whole submission, consumes `!!` before `!`, and trims
the command. Quoting, internal whitespace, and newlines stay verbatim. An empty
`!` or `!!` is ordinary prompt text. `!!! ls` runs the excluded command `! ls`.
The prefix works only in the interactive editor: CLI positional prompts, RPC
prompts, and bridge requests do not parse it.

`!!command` saves the command and output locally but sets
`excludeFromContext`, so they do not enter later ordinary or compaction model
requests. The output is still visible in the terminal. The JSONL session file
is not secret storage.

An admitted command runs once, makes no provider request, and does not need
provider credentials or model-tool allowlists. psi shows the command row, a
running state, then final sanitized output and an explicit no-output, exit,
cancellation, or truncation marker. There are no live chunks. Esc/Ctrl+C, quit,
EOF, suspend, and terminal failure cancel and join the owned process before psi
returns. A second shell, a model run, bridge preparation, or a session mutation
is refused while the shell is unsettled; a refused submission restores the
draft, including `/b` and `/bump` submissions. psi keeps up to the last 50 KiB
and 2000 lines of sanitized output without cutting a UTF-8 character. A capture
file is advertised only when it exists.

The command runs with the psi process environment and permissions. It can
modify files before cancellation or a storage error; psi cannot roll it back
and never retries it automatically. If the first durable append fails, psi
reports `UNSAVED`, keeps the committed leaf unchanged, and blocks later
mutations.

The `bash` tool uses the same shell selection and runs in the workspace. It
returns the last 2000 lines or 50 KiB. If a timeout occurs after the shell has
exited while a background process keeps writing output, the timeout message
includes the shell's exit code and elapsed time. See [Environment](env.md) for
the exact shell precedence.

RPC `bash`, streamed terminal chunks, replay formatting, shell-history recall,
and model/shell concurrency are not implemented.

## Web access

`web_search` finds pages through SearXNG or Exa. `fetch_content` fetches a URL
or a batch of up to 20. `get_search_content` pages retained text by
`responseId`. Web tools read `web-search.json`; see [Web access](web.md) for
configuration, limits, and the network guard.

## Use MCP tools

MCP servers are configured in `<agentDir>/mcp.json` and, for a trusted
project, `.pi/mcp.json`. psi starts them in the background, so the first
request can proceed before they are ready. Use `/mcp` to check server state and
discovered or exposed tool counts. `/reload` re-reads the configuration.
`--no-mcp` disables the client for one run. See [MCP servers](mcp.md).

## Adjust the terminal

psi is inline in the terminal by default. `--tui-mode regular` and `--mode text`
are accepted for Pi compatibility; fullscreen mode and rich display preferences
are ignored. `--mode rpc` runs the headless JSONL interface described in
[Command line](cli.md). `/hotkeys` lists the shortcuts active in this session.
See [Keyboard](keyboard.md).
