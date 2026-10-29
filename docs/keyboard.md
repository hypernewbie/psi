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
- Completion and picker search never scan files or call a provider; picker queries are not conversation messages
- `Ctrl+C` cancels a picker; during a run it cancels the run. When idle it clears the draft first and exits only when the draft is empty
- `Ctrl+D` deletes at cursor when nonempty, exits when empty
- On Unix, `Ctrl+Z` settles cancellation, restores terminal modes, and suspends. `SIGCONT` resumes the draft without transcript replay.
- Bracketed paste: one literal bounded paste (256 KiB), CRLF→LF, no submit/cancel/exec, oversized rejected as whole edit
- Draft budget 256 KiB UTF-8, never splits grapheme; recalled draft editable, returning past newest restores unfinished draft

Commands (bounded inline, searchable editor pickers, no fullscreen):

- `/help` — commands, keys, limits, examples
- `/new` — new session (keeps scrollback, resets recall)
- `/resume [filter]` — search workspace sessions, use arrows/PageUp/PageDown and Enter; same-session no-op
- `/model` — searchable model picker with Pi's `→` selection pointer, `✓` current marker, provider badges, and selected-model credential details; `/model [provider/]model` applies a direct session-local choice
- `/thinking` — searchable supported-level picker with Pi's `→` pointer; `/thinking [level]` applies a direct session-local choice
- `/compact [focus]` — real cancellable summary, start/settled outcome
- `/session` — identity, workspace, provider/API/model, thinking, continuation, usage, and theme diagnostics (no secrets, no invented $0.00)
- `/reload` — reload supported display/theme settings and resources without replaying committed scrollback
- `/history [N|all]` — last N user spans (default 20) or full history, display-only, no mutation
- `/skill:name [args]` — resolve, persist reproducible input (`psi.input`), show invocation once
- `/quit` — settle work, restore terminal

Busy: ordinary submission disabled while a run is active; cancellation only.
Unknown `/commands` error locally, never become prompts.
Every command works after resume and after cancellation; draft preserved on errors.
