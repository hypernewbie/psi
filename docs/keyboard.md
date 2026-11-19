# Keyboard and commands (matches binary)

Fixed map (no Kitty, no Shift+Enter detection; portable newline is Ctrl+J):

- `Enter` submit outside bracketed paste; pasted newlines are literal, never submit. When psi is idle, one empty `Enter` retries an unfinished turn (failed, interrupted, or recovered from a crash). Otherwise the empty submission is ignored silently.
- `/b` and `/bump` request another response from the current context without adding a user message. They work after a completed or output-limited answer as well as after an unfinished turn, and use the current model, thinking level, tools, system prompt, and compaction policy. `/b anything` and `/bump anything` are ordinary prompt text.
- `Ctrl+J` newline (reliable legacy binding)
- `Left/Right` move by grapheme cluster (uniseg)
- `Up/Down` first control an active completion or picker; otherwise they move through visual draft rows
- `Ctrl+P` cycles to the next available model; if the current model is no longer available, it selects the first available model
- `Home/End`, `Ctrl+A`/`Ctrl+E` logical line; `Ctrl+Home`/`Ctrl+End` document
- `Backspace/Delete` one grapheme; `Ctrl+W` previous word; `Ctrl+U`/`Ctrl+K` line
- `/command`, `/skill:name`, and existing `/ac`/`/autocompact` argument completion stays in the editor tail (up to five rows by default; `autocompleteMaxVisible` sets the limit from 3 to 20) with Pi's `→` selection pointer. A whole-token match remains eligible, and the current highlight stays when its item is still a candidate. `Up/Down` select, `Tab` applies, `Enter` applies and submits, `Esc` dismisses
- Search pickers replace the editor region, as Pi's `showSelector` does. The picker owns its border, its `> ` query row, and its bounded list. Search uses case-insensitive substring matching in the editor query; `Up/Down` select, `PageUp/PageDown` move by the visible list size (at most ten), `Enter` applies, `Esc` or `Ctrl+C` cancels and restores the prior draft
- `Ctrl+L` opens the model picker; `Shift+Tab` cycles the selected model's supported thinking levels
- In `/thinking`, `Ctrl+S` saves the highlighted level as the global `defaultThinkingLevel`. `Enter` changes only the current session. The per-model level picker in `/settings` does not use `Ctrl+S`.
- `Ctrl+T` hides or shows reasoning blocks. The toggle works during a run, repaints the transcript, and is saved as `hideThinkingBlock` in the agent `settings.json`
- `Ctrl+O` expands or collapses all skill blocks and web tool cards, also during a run. It changes only the transcript display: it does not submit a message or call a provider, and it leaves the draft and cursor position intact. The state is not saved; a restart starts collapsed. It affects skill blocks and the `web_search`, `fetch_content`, and `get_search_content` cards only
- Completion and picker search never scan files or call a provider; picker queries are not conversation messages
- `Ctrl+C` cancels a picker; during a run it cancels the run. When idle it clears the draft first and exits only when the draft is empty
- `Ctrl+D` deletes at cursor when nonempty, exits when empty
- On Unix, `Ctrl+Z` settles cancellation, restores terminal modes, and suspends. `SIGCONT` resumes the draft without transcript replay.
- Bracketed paste: one literal bounded paste (256 KiB), CRLF→LF, no submit/cancel/exec, oversized rejected as whole edit
- Codex login panel: `Esc` cancels and `Ctrl+D` cancels and quits. In browser mode, `Enter` submits an optional masked redirect URL or code. In device-code mode, `Enter` opens the verification page.
- Secret prompt: keys are masked and never enter prompt recall. `Enter` saves, `Esc` or `Ctrl+C` cancels. Paste must fit within 256 KiB and contain no newline. `Ctrl+J` does not submit.
- Draft budget 256 KiB UTF-8, never splits grapheme

Commands (bounded inline completion and searchable editor pickers; no fullscreen):

- `/settings` — change supported live settings, including during a run. Display changes apply immediately. Request settings apply to the next request. Persisted values merge into the user `settings.json`.
- `/trust` — save a project trust decision for future sessions; restart psi for the decision to take effect
- `/login [provider]` — pick a provider; OpenAI opens ChatGPT sign-in, OpenAI Codex offers browser or device-code sign-in, and other supported providers open a masked key prompt. Anthropic API-key login is unavailable in psi
- `/logout` — remove a selected stored built-in API key or OAuth credential; OAuth removal attempts one best-effort revocation request and local deletion continues if it fails; environment variables and `models.json` stay unchanged
- `/model [provider/model]` — open the model picker or set a model directly
- `/thinking [level]` — open the supported-level picker or set a level directly; `Ctrl+S` saves the highlighted level as the global default
- `/name [name]` — show or set the session display name
- `/session` — show session information and statistics
- `/hotkeys` — show keyboard shortcuts
- `/clone` — duplicate the session at the current position
- `/new` — start a session (keeps scrollback, resets recall)
- `/compact [focus]` — create a cancellable compact summary
- `/b`, `/bump` — request another response without adding a prompt. Available only while idle, outside pickers, runs, and compaction. Explicit commands bypass the unfinished-turn gate but keep storage, auth, and continuation checks. Empty `Enter` retries unfinished turns only. All admission refusals stay silent; started runs report failures, cancellation, partial output, and unsaved output normally.
- `/resume` — search workspace sessions; same-session selection is a no-op
- `/reload` — reload keybindings, extensions, skills, prompts, themes, and context files
- `/quit` — release the terminal first, then settle and exit
- `/ac`, `/autocompact`, `/loop`, and `/loop-stop` — embedded adaptive-compaction and loop extensions
- `/skill:name [args]` — invoke a skill with reproducible input (`psi.input`). The transcript shows `[skill] name (ctrl+o to expand)` with the arguments in a separate user row; `Ctrl+O` expands or collapses the body. An unknown, unreadable, or oversized skill is refused instead of sent as text

`/help`, `/version`, and `/history` are not slash commands. Use `./psi --help` and `./psi --version` for CLI information. An unknown slash command is ordinary prompt text and is sent to the model, as in Pi. `/skill:` is the exception: an unknown skill is always refused, never sent as prompt text.

Busy: the editor stays active during a run, like Pi. `Enter` queues steering for the next turn boundary; `Alt+Enter` queues a follow-up until the current run ends. This includes `/skill:<name> [args]`, which queues the expanded block and keeps its invocation provenance. Dim `Steering: …` and `Follow-up: …` rows show pending text. Queues deliver one at a time in their respective modes. `Esc`/`Ctrl+C` cancel and restore undelivered queued text to the draft; queued skills restore as `/skill:name args`, not as expanded bodies. `/ac`, `/autocompact`, `/loop-stop`, and `/skill:` have busy-run handling. `/model`, `/thinking`, and `/settings` work during a run. `/session`, `/hotkeys`, and `/name` run immediately without cancelling the run. Request settings apply to the next request. `/quit` releases the terminal first, then the run settles behind it and the process exits. Unknown slash text follows the same `Enter` and `Alt+Enter` queue rules as ordinary prompt text. Recognized commands without busy-run handling and a bare `/` return to the draft. `/b` and `/bump` are discarded during a run, never queued, steered, or restored to the draft; use them after the run settles.

A bump keeps visible partial output and saved answers in the transcript. The persisted failure record stays invisible and is omitted from ordinary continuation requests. Existing compaction can still summarize visible partial progress. A failed crash can hide whether a tool already ran; bump sends synthetic missing-tool results in request context and never re-executes the old batch. A retry can bill the provider again.
