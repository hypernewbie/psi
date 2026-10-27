# Limitations

## Permanent product boundaries

psi is an inline terminal agent, not a visual Pi clone.
It has no fullscreen mode, widgets, panels, session-tree interface, or Pi TUI API.
It has no extension runtime, MCP, codemode, tool search, subagents, public SDK, RPC, or JSON event mode.
Print mode, HTML export, sharing, bug uploads, hosted services, telemetry, and automatic updates are excluded.

psi supports Anthropic Messages and Codex Responses only.
Additional providers, cloud credential discovery, routing, virtual models, and automatic catalog refresh are excluded.
Background provider jobs, cache warming, classifier models, and image/audio/video generation are excluded.
Clipboard access, image paste, external editors, prompt templates, and browser automation are excluded.
The `read` tool can read supported image files.

Tools run with the current OS user permissions.
Project consent and workspace paths do not provide a sandbox.
Per-tool approval, a permission-policy language, and automatic Git operations are excluded.
The shell tool intentionally inherits the psi process environment.
Regression shell processes instead use an isolated environment.

## Authentication

Anthropic uses API keys.
Codex uses existing ChatGPT OAuth credentials with refresh and a Pi-compatible directory lock.
Refresh preserves unrelated fields and uses atomic persistence with restrictive permissions.

psi has no login interface and does not support Anthropic OAuth or `!command` execution.
Codex login remains Pi `/login openai-codex`.
psi does not substitute a billed OpenAI API key for Codex OAuth.
Partial streams do not permit an invisible request replay.

## Current implementation limits

Sessions use Pi JSONL v3 with a 32 MiB record limit.
The live store indexes offsets and small metadata without a whole-file size cutoff.
It decodes the selected context on demand and streams history one record at a time.
The 512 MiB compacted fixture passed the 3-second and 160 MiB RSS limits on the recorded local profile.
A large active context still requires memory for its decoded payloads.

Recall retains at most 200 prompts within a 4 MiB text budget.

Persistent continuation resolves symlinks to one canonical writer-lock path.
Hard-linked sessions permit read-only history but require a separate copy for persistent continuation.

The unmodified pinned Pi reader and writer passed both continuation directions for both provider metadata forms.
The fixtures cover branches, context edits, compaction, signed reasoning, images, and state-only `psi.input` provenance.
The acceptance ledger separates these results from unfinished user journeys.

The editor uses `uniseg` for grapheme segmentation and width, with an explicit keycap-width correction.
Prompts and bracketed pastes have a 256 KiB UTF-8 budget.
The terminal writer changes only its owned tail and uses writes of at most 64 KiB.
Stream output coalesces at scheduled frames and flushes at semantic boundaries.

Regression tests cover startup history, `/history all`, Backspace, nonduplicated submission, and `/new` recall reset.
Actual-app PTY tests cover Ctrl+Z/SIGCONT and terminal-attribute restoration.
These tests do not establish every terminal profile, latency target, or human flicker check.
Linux, WSL2, and SSH acceptance still require the complete platform matrix.

History hides reasoning, tool arguments, tool results, diffs, and image payloads.
Tool outcomes use explicit result flags and cancellation metadata, not guesses from output text.
Unresolved calls remain interrupted rather than successful.

The agent reconciles authoritative final text and saves visible interrupted text.
It rejects contradictory finals and does not execute their tools.
Cancellation records known unstarted calls without executing them.
A storage failure blocks further writes until the operator opens another checked session or creates a new session.

Context-limit recovery permits at most one compaction and retry before visible partial output.
Synthetic loop/provider tests cover that path.
Full compaction, provider, storage-fault, and actual-binary journeys remain incomplete acceptance requirements.

The complete status and required measurements are in [the acceptance ledger](acceptance.md).
These incomplete requirements are not new permanent exclusions or a deferred PLAN3.
