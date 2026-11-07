# Keyboard and commands (matches binary)

Fixed map (no Kitty, no Shift+Enter detection; portable newline is Ctrl+J):

- `Enter` submit outside bracketed paste; pasted newlines are literal, never submit
- `Ctrl+J` newline (reliable legacy binding)
- `Left/Right` move by grapheme cluster (uniseg)
- `Up/Down` first control an active completion or picker; otherwise they move through visual draft rows and browse session prompt history at an outer row
- `Ctrl+P` / `Ctrl+N` browse history directly
- `Home/End`, `Ctrl+A`/`Ctrl+E` logical line; `Ctrl+Home`/`Ctrl+End` document
- `Backspace/Delete` one grapheme; `Ctrl+W` previous word; `Ctrl+U`/`Ctrl+K` line
- `/command` and `/skill:name` completion stays in the editor tail (at most five rows) with Pi's `→` selection pointer; `Up/Down` select, `Tab` applies, `Enter` applies and submits, `Esc` dismisses
- Search pickers replace the editor region, as Pi's `showSelector` does. The picker owns its border, its `> ` query row, and its bounded list. Search uses case-insensitive substring matching in the editor query; `Up/Down` select, `PageUp/PageDown` move by the visible list size (at most ten), `Enter` applies, `Esc` or `Ctrl+C` cancels and restores the prior draft
- `Ctrl+L` opens the model picker; `Shift+Tab` cycles the selected model's supported thinking levels
- `Ctrl+T` hides or shows reasoning blocks. The toggle works during a run, repaints the transcript, and is saved as `hideThinkingBlock` in the agent `settings.json`
- Completion and picker search never scan files or call a provider; picker queries are not conversation messages
- `Ctrl+C` cancels a picker; during a run it cancels the run. When idle it clears the draft first and exits only when the draft is empty
- `Ctrl+D` deletes at cursor when nonempty, exits when empty
- On Unix, `Ctrl+Z` settles cancellation, restores terminal modes, and suspends. `SIGCONT` resumes the draft without transcript replay.
- Bracketed paste: one literal bounded paste (256 KiB), CRLF→LF, no submit/cancel/exec, oversized rejected as whole edit
- Draft budget 256 KiB UTF-8, never splits grapheme; recalled draft editable, returning past newest restores unfinished draft

Commands (bounded inline completion and searchable editor pickers; no fullscreen):

- `/model [provider/model]` — open the model picker or set a model directly
- `/thinking [level]` — open the supported-level picker or set a level directly
- `/name [name]` — show or set the session display name
- `/session` — show session information and statistics
- `/hotkeys` — show keyboard shortcuts
- `/clone` — duplicate the session at the current position
- `/new` — start a session (keeps scrollback, resets recall)
- `/compact [focus]` — create a cancellable compact summary
- `/resume` — search workspace sessions; same-session selection is a no-op
- `/reload` — reload keybindings, extensions, skills, prompts, themes, and context files
- `/quit` — settle work and restore the terminal
- `/ac`, `/autocompact`, `/loop`, and `/loop-stop` — embedded adaptive-compaction and loop extensions
- `/skill:name [args]` — invoke a skill with reproducible input (`psi.input`)

`/help`, `/version`, and `/history` are not slash commands. Use `./psi --help` and `./psi --version` for CLI information. An unknown slash command is ordinary prompt text and is sent to the model, as in Pi.

Busy: the editor stays active during a run, like Pi. `Enter` queues steering for the next turn boundary; `Alt+Enter` queues a follow-up until the current run ends. Dim `Steering: …` and `Follow-up: …` rows show pending text. Queues deliver one at a time in their respective modes. `Esc`/`Ctrl+C` cancel and restore undelivered queued text to the draft. `/ac`, `/autocompact`, and `/loop-stop` have busy-run handling; other slash commands return to the draft.
Every command works after resume and after cancellation; draft preserved on errors.
