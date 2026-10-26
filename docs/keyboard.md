# Keyboard and commands

Fixed map (no Kitty, no Shift+Enter detection):

- `Enter` submit (outside bracketed paste; pasted newlines are literal)
- `Ctrl+J` newline (reliable legacy binding)
- `Ctrl+C` cancel run; idle: clear draft first, second exits
- `Ctrl+D` exit (`/quit` also exits)
- `Up/Down` history (in-memory only, no persistent history file)
- Paste inserts literally (bracketed paste honored)

Commands (bounded, no fullscreen dialogs):

- `/help`, `/new`, `/resume` (numbered list, max 10 shown), `/model`, `/thinking`, `/compact [focus]`, `/session`, `/quit`
- `/skill:name [args]` — explicit skill invocation (missing skill errors without a provider request)

Busy: ordinary submission disabled while a run is active; cancellation only.
No steering/follow-up queues. No double-Escape navigation.
