# Limitations

## Permanent product boundaries

psi is an inline terminal agent, not a visual Pi clone.
It has no fullscreen mode, widgets, panels, session-tree interface, or Pi TUI API.
It has no extension runtime, MCP, codemode, tool search, subagents, public SDK, RPC, or JSON event mode.
Print mode, HTML export, sharing, bug uploads, hosted services, telemetry, and automatic updates are excluded.

psi supports Anthropic, ChatGPT Codex, MiniMax, OpenCode Go, DeepSeek, Z.AI, and Google. OpenCode Go selects Anthropic Messages, OpenAI Chat Completions, Google Generative AI, or generic OpenAI Responses per model. Unsupported provider/API pairs and compatibility features remain unavailable.

Cloud credential discovery, routing, virtual models, and automatic catalog refresh are excluded. Google Vertex and OAuth, OpenCode Zen, MiniMax China, and Z.AI China endpoints are not included. Live-provider acceptance has not run.
Background provider jobs, cache warming, classifier models, and image/audio/video generation are excluded.
Clipboard access, image paste, external editors, prompt templates, and browser automation are excluded.
The `read` tool can read supported image files.

Tools run with the current OS user permissions.
Project consent and workspace paths do not provide a sandbox.
Per-tool approval, a permission-policy language, and automatic Git operations are excluded.
The shell tool intentionally inherits the psi process environment.
Regression shell processes instead use an isolated environment.

## Tool execution

Tool calls from one assistant response run concurrently, like Pi's parallel default.
A `read` can overlap a `write`, `edit`, or `bash` call.
If one call depends on another call's result, the model must use separate turns.
`write` and `edit` serialize per file through one shared queue, in registration order; mutations to different files overlap.
A missing path keys by its resolved absolute path, matching Pi.
`read` and `bash` calls never queue there.
Results settle in assistant-call order behind a barrier the next model request cannot cross.

Two psi deviations from Pi stay explicit:

1. Internal control calls (`loop_control`) keep call order relative to each other, because their state transitions and continuation messages are order-sensitive. Unrelated calls in the same batch still overlap.
2. Terminal success rows commit only after the corresponding result is durable. Pi emits tool completion events when a worker finishes, before result messages join the context.

Cancellation during a batch admits nothing new, joins every started worker, records unstarted calls as `not_started`, and never labels a started call unstarted.
A storage failure stops the batch without rolling back completed mutations, and unsaved results never reach a later request.

The pinned reference trace for the shared behavior is `internal/agent/parallel_reference_test.go` (see `docs/reference-manifest.md`).
These deviations and tests do not claim full Pi parity.

Measured profile on the recorded Linux host: a 200-call read batch (4 KiB files) settled all calls in 31 ms with about 10.9 MiB of total allocation (`TestParallelLargeBatchStaysBounded`).
The six Linux/macOS/Windows amd64/arm64 cross-builds passed after this change; macOS and Windows binaries were not executed.

## Authentication

Anthropic, MiniMax, OpenCode Go, DeepSeek, Z.AI, and Google use provider-specific API keys from `auth.json`, `models.json`, or their exact environment variable. Codex uses existing ChatGPT OAuth credentials with refresh and a Pi-compatible directory lock. Refresh preserves unrelated fields and existing permission bits. It persists credential changes through atomic replacement.

psi has no login interface, does not support Anthropic OAuth, and does not execute `!command` credentials. Codex login remains Pi `/login openai-codex`. psi does not substitute a billed OpenAI API key for Codex OAuth. Partial streams do not permit an invisible request replay.

## Current implementation limits

Sessions use Pi JSONL v3 with a 32 MiB record limit.
The live store indexes offsets and small metadata without a whole-file size cutoff.
It decodes the selected context on demand and streams history one record at a time.
The 512 MiB compacted fixture passed the 3-second and 160 MiB RSS limits on the recorded local profile.
A large active context still requires memory for its decoded payloads.

Recall retains at most 200 prompts within a 4 MiB text budget.

Persistent continuation resolves symlinks to one canonical writer-lock path.
Hard-linked sessions permit read-only history but require a separate copy for persistent continuation.

The Pi JSONL v3 format retains provider/API/model identity and supported opaque signatures. PLAN6 added synthetic replay coverage for MiniMax/OpenCode Anthropic, DeepSeek/OpenCode Completions, Google/OpenCode Google, and OpenCode Responses. The optional reference session matrix has been run with the installed Pi 1.4.0 package; that run does not claim the installed artifact is byte-for-byte built from the pinned source revision.
The fixtures cover branches, context edits, compaction, signed reasoning, images, and state-only `psi.input` provenance.
The acceptance ledger separates these results from unfinished user journeys.

The editor uses `uniseg` for grapheme segmentation and width, with an explicit keycap-width correction.
Word navigation groups adjacent CJK ideographs without the ICU dictionary segmentation that Pi uses.
`wordnav_test.go` records this port limitation separately from the 24 supported reference cases.
Emoji and symbols no longer count as letters.
Session decoders use exact field names, including nested usage fields.
Prompts and bracketed pastes have a 256 KiB UTF-8 budget.
The terminal writer changes only its owned tail and uses writes of at most 64 KiB.
Stream output coalesces at scheduled frames and flushes at semantic boundaries.
Assistant Markdown commits stable blocks and code lines to native scrollback.
Only the unresolved block stays in the bounded active tail; live and replay output use the same formatter.

Regression tests cover startup history, `/history all`, Backspace, nonduplicated submission, and `/new` recall reset.
Actual-app PTY tests cover Ctrl+Z/SIGCONT and terminal-attribute restoration.
These tests do not establish every terminal profile, scheduled frame-rate bound, or human flicker check.
See [the UI acceptance ledger](ui-acceptance.md) for measured coverage and blocked platform/human checks.
Linux, WSL2, and SSH acceptance still require the complete platform matrix.

History hides reasoning, tool arguments, tool results, diffs, and image payloads.
Tool outcomes use explicit result flags and cancellation metadata, not guesses from output text.
Unresolved calls remain interrupted rather than successful.

The agent reconciles authoritative final text and saves visible interrupted text.
It rejects contradictory finals and does not execute their tools.
Cancellation records known unstarted calls without executing them.
A storage failure blocks further writes until the operator opens another checked session or creates a new session.

During a run, the editor stays active and `Enter` queues a steering message, as in Pi.
Two Pi behaviors remain open: slash commands do not execute mid-run (their text returns to the draft), and there is no separate follow-up queue (`Alt+Enter`).

Context-limit recovery permits at most one compaction and retry before visible partial output.
Synthetic loop/provider tests cover that path.
Full compaction, provider, storage-fault, and actual-binary journeys remain incomplete acceptance requirements.

PLAN6 delivered-binary provider journeys passed, but live-provider service acceptance remains unrun. The broader requirements remain in [the acceptance ledger](acceptance.md), [the UI acceptance ledger](ui-acceptance.md), and [the robustness ledger](robustness.md).
PLAN3 acceptance remains incomplete.
These requirements are not new permanent exclusions.
