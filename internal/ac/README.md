# Native pi-ac port

`internal/ac` is a native Go port of the session-local policy in `hypernewbie/pi-ac` revision `96e92f5fcfe046466366bc5bc2972b813c03b09a` (local install: `~/.pi/agent/git/github.com/hypernewbie/pi-ac`). The upstream extension is AGPL-3.0-or-later; its license is preserved in `internal/ac/LICENSE`.

Psi exposes the controls through `/ac` and the `/autocompact` alias:

- `/ac status` reports context estimate, targets, growth forecast, compactor model, and last compaction.
- `/ac compact [focus]` compacts now. While a run is active, the request waits for settlement, compacts, then submits the native resume message.
- `/ac threshold <default|1-100|off>` controls the adaptive target. The default is `min(50% of the model window, 230,000 tokens)`, capped by psi's native threshold and adjusted by the largest of the last five positive turn-growth samples.
- `/ac end-turn <default|off|token-count|percent%>` controls the completed-run threshold. The default is `max(200,000 tokens, 40% of the model window)`.
- `/ac end-turn-delay <minutes>` sets the session timer. The default is ten minutes; zero means immediate.
- `/ac compactor current|clear` uses or clears a session-local summary-model override.
- `/ac enable` and `/ac disable` toggle proactive compaction. Psi's configured native compaction remains independent.

Controls reset when psi opens a different session. They do not change `settings.json`. Context use is psi's estimate, not provider-reported usage. Full behavioral source and tests are identified in `ac.go` and `ac_test.go`.
