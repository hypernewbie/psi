# Keyboard and commands (matches binary)

Fixed map (no Kitty, no Shift+Enter detection; portable newline is Ctrl+J):

- `Enter` submit outside bracketed paste; pasted newlines are literal, never submit
- `Ctrl+J` newline (reliable legacy binding)
- `Left/Right` move by grapheme cluster (uniseg)
- `Up/Down` move through visual draft rows; at outer row browse session prompt history
- `Ctrl+P` / `Ctrl+N` browse history directly
- `Home/End`, `Ctrl+A`/`Ctrl+E` logical line; `Ctrl+Home`/`Ctrl+End` document
- `Backspace/Delete` one grapheme; `Ctrl+W` previous word; `Ctrl+U`/`Ctrl+K` line
- `Tab` complete `/command` and `/skill:name` (bounded list on ambiguity, no exec/scan/provider)
- `Esc` cancels selector/completion, never blocks (50ms prefix timeout)
- `Ctrl+C` cancels run; idle clears draft first, empty exits; busy is cancellation-only (one quiet notice)
- `Ctrl+D` deletes at cursor when nonempty, exits when empty
- `Ctrl+Z` settles cancellation, restores terminal modes, and suspends. `SIGCONT` resumes the draft without transcript replay.
- Bracketed paste: one literal bounded paste (256 KiB), CRLF→LF, no submit/cancel/exec, oversized rejected as whole edit
- Draft budget 256 KiB UTF-8, never splits grapheme; recalled draft editable, returning past newest restores unfinished draft

Commands (bounded inline, 20/page selector, no fullscreen):

- `/help` — commands, keys, limits, examples
- `/new` — new session (keeps scrollback, resets recall)
- `/resume [filter]` — 20/page, n/p, /filter, number, empty cancels; same-session no-op
- `/model` — list with provider/credential availability; `/model [provider/]model` session-local
- `/thinking [level]` — list or set (off,minimal,low,medium,high,xhigh,max), session-local
- `/compact [focus]` — real cancellable summary, start/settled outcome
- `/session` — identity, workspace, provider/API/model, thinking, continuation, usage (no secrets, no invented $0.00)
- `/history [N|all]` — last N user spans (default 20) or full history, display-only, no mutation
- `/skill:name [args]` — resolve, persist reproducible input (`psi.input`), show invocation once
- `/quit` — settle work, restore terminal

Busy: ordinary submission disabled while a run is active; cancellation only.
Unknown `/commands` error locally, never become prompts.
Every command works after resume and after cancellation; draft preserved on errors.
