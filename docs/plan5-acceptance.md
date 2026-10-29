# PLAN5 acceptance ledger

## Status

PLAN5 implementation is in progress. The compaction and theme-core checkpoints are complete. Theme application, terminal color replies, discovery/reload, role-aware output, bounded tool summaries, replay, and retry status now have local tests. All PLAN5 gates remain open until the required acceptance evidence is complete.

No live provider request ran. Provider tests use synthetic data and loopback servers. The pinned references remain Pi `d5b3de22d0e24a3e9c38a9410d3329b099717283` and Codex `23934d31b15b1127c83535f84854e5009f7cd51d`.

## Compaction checkpoint

Ported Pi's projected cut-point policy for psi's retained session roles from `packages/coding-agent/src/core/compaction/compaction.ts` (`findProjectedCutPoint`, `isCutPointMessage`, and `isTurnStartMessage`). Preparation uses the canonical `session.Projection`, so context edits and the latest compaction boundary are respected. It distinguishes eligible work, empty history, history within the retained budget, and an already-compacted path. The existing explicit zero-retention behavior remains a documented psi exception.

A split turn is summarized in two stages, following Pi's history and turn-prefix model and the pinned prompts. Previous summaries are sent through Pi's update-summary prompt path. Usage from completed summary calls is aggregated in the compaction entry; known usage is separately persisted if a required later summary is incomplete or fails. No checkpoint is appended unless every required summary completes. Preparation also follows Pi's recovery-omission suffix adjustment, projects context replacements before selecting summary input, and never cuts between a tool call and its trailing result.

Manual `/compact` performs read-only preparation before showing a spinner or requiring credentials. Expected no-work results use a muted informational notice and make no provider request or session change. Threshold no-work continues the pending request; overflow no-work returns the original context-limit failure without retrying. Successful compaction uses a compact metadata marker.

### Evidence

| Level | Evidence | Result |
|---|---|---|
| Component | `TestPrepareCompactionReportsNoWorkOutcomes`, `TestPrepareCompactionSplitsLongAssistantSpanAtPiCutPoint`, `TestLongSingleUserSpanCompactsAtOrdinaryRetentionBudget` | Pass; the long-span fixture uses the ordinary 20,000-token retention budget. |
| Agent/storage | Two-stage split summary, previous-summary update, aggregate usage, failure/incomplete/cancelled-summary usage, failed-checkpoint append, no-checkpoint, threshold no-op, overflow no-op, retain-none, projected replacement, omitted recovery suffix, and tool-call/result boundary regressions | Pass with fake providers and storage fault injection. |
| Actual app | Empty and persistent short-session `/compact` offline no-ops; the short-session case preserves exact file bytes. A real-PTY ordinary-budget long-span summary runs through a loopback Anthropic server, is cancelled in a separate actual-app journey without changing session bytes, then is reopened in a second process with compact default replay and explicit `/history all`. | Pass; the summary request and saved `firstKeptEntryId` are checked. |
| Reference | Pinned Pi cut-point and split-summary source reviewed | Source comparison only; no full Pi runtime session. |
| Human | Visual review, selection/copy, flicker, macOS and Windows Terminal acceptance | Not performed. |

## Theme and presentation implementation checkpoint

Implemented strict Pi theme JSON parsing for all 56 pinned tokens, their foreground/background slots, five missing-only fallbacks, chained variable references, inert `$schema`, exact names, BOM handling, and strict top-level/color/export fields. Added unmodified Pi dark/light built-ins with hashes and resolved-color checks. Color support includes RGB, indexed/default, OKLCH, OKHSL, gamut mapping, and 256-color/truecolor output. The port retains the OKHSL/OKLab MIT notice.

Ported Pi's adaptive `system` theme, including appearance detection, contrast rules, three fallback tiers, panel surfaces, and dim-role behavior. Seven complete 56-token fixtures generated from the pinned `system-theme.ts` match token by token. These fixtures establish a local source comparison, not terminal or actual-app acceptance.

Theme and display settings load with user/project precedence and project-trust gating. CLI theme overrides, local resource paths, built-ins, project/user theme discovery, package theme data discovery, collision diagnostics, and terminal color-mode settings are connected to startup. The app applies the user theme before project consent, then loads trusted project resources after consent. `/reload` installs a resolved theme/display snapshot transactionally; `/session` reports selected theme diagnostics. A selected user theme file has a debounced `fsnotify` watch with last-good retention. File events are handled by the app loop, not by the watcher goroutine.

Terminal color and appearance replies use the existing input owner. Tests cover bounded OSC parsing, split and late replies, held keystrokes, appearance pairs, and replies not becoming editor input. No-color mode avoids generated color styles and color queries.

Role-aware output now covers user surfaces, assistant Markdown, metadata, notices, tool rows, completion, pickers, editor, and footer. Display settings include `outputPad`, `editorPaddingX`, `autocompleteMaxVisible`, and `markdown.codeBlockIndent`. Theme reload redraws the active tail and open picker/completion without replaying native scrollback or replacing the provider/session.

### Evidence

| Level | Evidence | Result |
|---|---|---|
| Component | Theme parser/color/system/catalog tests; display, surface, picker, Markdown, and reply fixtures | Pass. |
| Actual app | Two distinct custom-theme user surfaces, late appearance reply, atomic-save completion reload, explicit `/reload`, open model-picker query/current selection/cursor, invalid-save last-good retention and one warning, theme save while suspended with picker focus retained on resume, and a mid-stream theme change that preserves the committed sentinel while restyling the active tail | Pass in isolated Unix PTYs. |
| Watch lifecycle | Eight atomic saves coalesce to one notification; directory creation, unselected-file filtering, debounce, close, suspend/resume with a pending selected-theme save, and 100-cycle goroutine/file-descriptor checks | Pass locally. |
| No-color | Theme serializer and replay tests assert no generated SGR while retaining text/geometry | Pass locally; human readability remains open. |
| Human/platform | Native selection/copy, perceived flicker, native macOS, and Windows Terminal review | Not performed. |

## Tool summary, replay, and retry checkpoint

Live tool rows display bounded, allowlisted invocation summaries: read path/range, write/edit target, or `$` plus a redacted command preview. Results, file bodies, edit text, shell output, and arbitrary argument maps remain hidden. Rows use distinct pending/success/error surfaces and retain explicit non-success outcomes. Replay uses the same formatter and structured timeline fields.

For historical tool calls, the session index extracts only bounded display summaries while it already scans the record. It does not retain argument maps or decode hidden body strings. Lazy replay does not load a tool-only assistant record merely to recover its summary; a `LoadHook` regression checks this. This is a bounded-summary index rather than on-demand raw-record parsing. The summary count is capped per record, and each retained path/range or command field is bounded.

Default replay displays a compact compaction marker. `/history` explicitly loads and displays the saved compaction summary with a metadata role. Trusted theme styles are not stripped after formatting. Retry observers report attempt, maximum, delay, and error class at the existing wait boundary for Anthropic and Codex; they do not alter retry policy or settlement.

### Evidence

| Level | Evidence | Result |
|---|---|---|
| Component | Summary allowlists, 512-byte bounds, path/range, Unicode suffix, command redaction, tool states, retry status formatting | Pass. |
| Lazy session | Large hidden-body canaries, bounded timeline items, no full tool-call load for summary, compact/default versus explicit compaction history | Pass locally. |
| Provider protocol | Loopback Anthropic and Codex retry tests verify a real retry and observed backoff metadata | Pass. |
| Actual app | Codex tool round trip, atomic user-theme reload with completion query retained, open-picker theme reload, mid-stream theme reload, and Anthropic backoff status | Pass in isolated Unix PTYs. |
| Resource profile | 512 MiB compacted-history fixture; summary bounds; 10k metadata/picker/deltas; paste and key latency; idle frames; 100 watcher lifecycles | Pass locally. On WSL2 Linux/amd64 (Go 1.26.4): 512 MiB activation 2,323 ms / 44,680 KiB peak RSS; 10k metadata first page 675 ms; 10k picker query/render p95 4.17 ms; actual 10k deltas 1.095 s; paced 10k delta profile 1.257 s, 14 timer frames, 5 event frames, 27 writer frames / 25 writes, 20,882 bytes, 11,301-byte max write; 1k/10k single-character allocations 108,864/896,000 bytes (8.23×); 1 KiB/512 KiB hidden write bodies allocated 5,110/5,106 bytes per summary; key-to-frame p95 207 μs; cancellation dispatch p95 40 ns; 256 KiB paste 81.6 ms; 10 idle seconds emitted no frames; 100 watcher create/close cycles added no goroutine or file descriptor. |

## Validation and open acceptance

`./scripts/check.sh` passes on the current working tree, including the race suite. It covers the existing packages and new local regressions. This is local evidence only.

### Final executable

Built `./psi` from clean source revision `2cf2d7518d3b87c44a63102d260be4ba3de3db36` on WSL2 Linux/amd64 with Go 1.26.4, using `CGO_ENABLED=0`, `-trimpath`, and `-buildvcs=false`. SHA-256: `5a294b78ade259fea27c3f8a6f03fb39773c8aa51da9e5a994dbcee31bbab15b`. The executable is an ignored build artifact, not a tracked source file.

The copied executable passed loopback-only actual-binary journeys for all four tools, empty and short-session no-op compaction, ordinary-budget compaction and a second-process replay/history check, cancelled compaction without session mutation, two selected custom-theme user surfaces, atomic theme reload, a mid-stream theme reload, and provider retry status. The binary used for the temporary-path runs had the same SHA-256. No live provider request ran.

Cross-build and test-binary compilation passed for windows/amd64 and darwin/amd64 and darwin/arm64. These checks do not establish native runtime behavior.

Still open:

- Finish remaining compaction differential and pinned-reader comparisons. Agent/storage and actual-app cancellation, failed-append, no-op byte-preservation, and restart-replay regressions pass locally.
- Complete the documented Linux/macOS/Windows platform matrix and native console journeys.
- Complete human light/dark/no-color readability, selection/copy, SSH/tmux, and flicker review.
- Run remote CI only if an authorized existing VCI coordinator destination is provided. Do not configure or create a coordinator.

G5-01 through G5-08 remain open. Local tests, cross-builds, and PTY fixtures do not close blocked remote, native-platform, or human gates.
