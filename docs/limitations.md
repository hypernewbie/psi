# Limitations (matches binary, permanent boundaries)

No extensions, JS runtime, npm packages, MCP, codemode, tool search, subagents,
public SDK, RPC, JSON event mode, print mode, fullscreen, widgets, Pi TUI API,
session tree/branch/fork/clone UI, HTML export, sharing, bug uploads, hosted
services, update checks, self-update, catalog refresh, telemetry, browser
automation, cloud credential discovery, extra providers, cache warming,
background jobs, virtual models, routing, classifier/image/audio/video models,
clipboard, image paste (reads only), external editor, prompt templates, sandbox,
per-tool approval, permission-policy language, automatic Git ops, or visual Pi clone.

Tools run with the current OS user permissions. Project consent and workspace
paths do not provide isolation.

Authentication: Anthropic API keys + existing ChatGPT Codex OAuth with refresh
(Pi-compatible directory lock, preserves unknown fields, 0600 + atomic replace).
No login UI, no Anthropic OAuth, no `!command` execution. One checked refresh
before streaming; no invisible replay after partial streaming; no paid-key fallback.

Providers: Anthropic Messages + Codex Responses, correct auth + request protocols.
Reasoning via budgets/effort maps; image capability checked before durable work;
unsupported fields rejected before billable calls; backend output-cap limits explained;
unknown prices never $0.00.

Sessions: Pi JSONL v3, prefix-preserved, truncation guard, advisory lock + generation
filter, 32 MiB records, 256 MiB files. Branches project to active path; siblings out.
Compactions rebuild through same projector (retain-none supported). Context edits
latest-wins, never mutate file bytes. `psi.input` provenance (`type: custom`) is
state-only, pi-ignored for context.

Terminal: main screen only, native scrollback, append-only transcript, one writer,
atomic frames, changed-row updates, 30fps coalesce, 64 KiB writes, grapheme editor
(uniseg + runewidth, pi utils.ts parity), 256 KiB prompts, 256 KiB pastes, no
redraw storms, sanitized output (ANSI/OSC/DCS/C1/bidi/split), NO_COLOR layout-preserving,
resize-safe viewport (1-column + very short safe), suspend/resume without replay.

History: current view (compaction-aware, full visible text) + all-history
(`/history all`, display-only, no mutation). Prompt recall 200 session-scoped,
provenance-preferred, no plaintext file. Tool states compact (`read: done`),
interrupted/outcome-unknown never `done`, no args/bodies/diffs.

Compaction: estimated (not usage), safe boundaries (calls+results together),
bounded summaries, compatible entries + checkpoints, threshold at settled boundaries,
≤1 context-limit retry, no repeated tools, no scrollback rewrite.

Cancellation/interruption: complete only after owned work settles; durable
aborted/error forms for partial text; no fabricated success; persistence failure
stops work with explicit unsaved notice; stale generations rejected.

Resources: bounded metadata (20/page, no transcript load for listing), indexed
large sessions (offsets, on-demand decode, <160 MiB, <3s activation), private temp
files, allowlisted subprocess env, synthetic fixtures/canaries only.
