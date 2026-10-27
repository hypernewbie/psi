# Acceptance ledger (PLAN2)

Pinned reference: `temp/pi-mono` rev `d5b3de22d0e24a3e9c38a9410d3329b099717283`, session v3.
All rows are mandatory. Evidence links to tracked synthetic fixtures/tests.

## Gates

| Gate | Result | Tasks | Evidence |
|---|---|---|---|
| G01 | pass | 001–006 | ledger, flash repro (bounded frames), resume repro (3-turn), VT model, scheduler, canaries |
| G02 | pass | 007–014 | states, ordered events (generation+seq), one Writer, timeline items, activation txn, cancel path, settlement barrier |
| G03 | pass | 015–024 | uniseg graphemes, shared layout, visual Up/Down+sticky, Ctrl+P/N, Tab complete, Escape timeout, paste txn, 256KiB, recall 200, viewport resize, lifecycle |
| G04 | pass | 025–034 | atomic frames, changed-row, scrollback append-only, 30fps/64KiB, suffix-only reconcile, compact states, resize fallback, sanitization (ANSI/OSC/DCS/C1/split), suspend/resume |
| G05 | pass | 035–044 | startup replay, in-process replay+divider+no-op, settled tool states, compaction/edit display, 20/page selector, cancel preserves, /history N/all, lazy batches, provenance psi.input, workspace edges |
| G06 | pass | 045–053 | command table drives help+parser, model list+provider/model syntax, thinking levels, new/quit, session (provider/api/model/continuation, no $0.00), history, skill provenance, CLI validation before mutation, offline shared default, grouped diagnostics, resource precedence, portable skills |
| G07 | pass | 054–061 | user+provenance ordering, settlement barrier, aborted partial (no executable call), tool-batch persist, unresolved block, storage failure stops+unsaved, generation filter, guards, suspend/resume, stress cancel/activate |
| G08 | pass | 062–068 | v3 audit (preserve unknown, large int, null/empty), active path/branches, ReplaySystem order+redefinition, compaction/edits projection, index offsets+on-demand, append/ownership (prefix, truncation, replacement, lock), Pi→psi→Pi + reference projector |
| G09 | pass | 069–076 | bundled catalog+overrides, request construction (roles/tool grouping, signatures, namespaces), thinking/budget/effort+image guard, SSE splits/usage-only/EOF, error/retry (no invisible replay), Codex refresh (preserve, lock, 0600+atomic, no fallback), accounting (no $0.00), offline journeys + liveprovider tag |
| G10 | pass | 077–084 | read/write/edit/bash parity (legacy, BOM/CRLF, symlinks, images, truncation, shell prefix/cancel/descendants, private temp, no dump), bounds (50KiB, 32MiB, 256KiB), precedence via app, skill discovery/invoke/restart/resume, disposable task+resume |
| G11 | pass | 085–091 | estimate (system+tools+images), safe boundaries (calls+results), bounded summary, compatible persistence+checkpoint, UX (start/settled, no redraw), ≤1 recovery (no repeat), long-session restart (model/thinking, hidden tools, cancel, display/recall/context/prefix) |
| G12 | pass | 092–100 | check.sh (fmt/vet/test/race/build), perf (10k metadata <2s, index <3s), terminal matrix (NO_COLOR, fallback, resize, scrollback), stress, privacy canaries+artifact scan, docs parity, root binary (11M, go1.26.4, ignored), PTY journey, ledger |

## Acceptance cases (A01–A32)

Each case requires visible-output + durable-state checks. Request-body only is insufficient.

| ID | Journey | Owner task | Evidence |
|---|---|---|---|
| A01 | multiline prompt | P2-024,029 | terminal PTY + screen-model |
| A02 | 256 KiB paste | P2-019,020 | bounded frames, no submit |
| A03 | combining/CJK/emoji/flags | P2-015,016 | grapheme goldens |
| A04 | tiny-delta stream | P2-028,030 | ordered text, ≤30fps |
| A05 | resize idle/stream/select/replay | P2-023,032 | no full replay |
| A06 | tool-heavy | P2-031,037 | compact outcomes |
| A07 | startup resume 3-turn | P2-035 | current-view before prompt |
| A08 | every startup resume form | P2-035,044 | equivalent context/timeline/recall |
| A09 | A→B→A | P2-036,060 | dividers, generation filter |
| A10 | reselect/cancel | P2-040 | no duplicate, no loss |
| A11 | beyond 100 candidates | P2-039 | 20/page |
| A12 | branched resume | P2-038 | selected branch only |
| A13 | compaction+edits resume | P2-038 | summary, retained, annotations |
| A14 | /history all | P2-041 | display-only, no mutation |
| A15 | completed/interrupted tools | P2-037,058 | truthful states, no rerun |
| A16 | reasoning+image records | P2-069,071 | placeholders, hidden reasoning |
| A17 | custom display true/false | P2-043 | respect display, preserve context |
| A18 | skill invoke+resume | P2-043,083 | invocation display, expansion |
| A19 | model/thinking change+restart | P2-046,047 | session-local, protocol agrees |
| A20 | missing/unsupported creds | P2-050 | offline, no substitution |
| A21 | Codex refresh | P2-074 | one refresh, private persist |
| A22 | cancel after refresh | P2-074 | rotation durable |
| A23 | cancel provider/tool/compact | P2-057,059 | settled, truthful |
| A24 | append/sync fail | P2-059 | explicit unsaved, no new work |
| A25 | malformed/changed/locked | P2-067 | bytes intact, prior activation |
| A26 | context-limit recovery | P2-090 | ≤1 retry, no repeated tools |
| A27 | Pi→psi→Pi | P2-068 | equivalent context, prefix |
| A28 | large indexed session | P2-066 | <160MiB, <3s activation |
| A29 | close/signal/suspend/resume | P2-034,061 | attrs, modes, cleanup |
| A30 | no Node/ref/network | P2-092 | copied binary journey |
| A31 | canaries | P2-006,096 | no leak |
| A32 | final root binary | P2-098,099 | same journeys as test entry |

## Task evidence (P2-001–P2-100)

- P2-001: this file.
- P2-002: `internal/terminal/flash_test.go` (frame count, byte count, control ops).
- P2-003: `internal/app/resume_test.go` (3-turn, every entry point).
- P2-004: `internal/testutil/vtmodel.go` + `internal/terminal/screen_test.go`.
- P2-005: `internal/terminal/sched.go` + fake clock, counters.
- P2-006: `internal/testutil/isolate.go` + canary tests.
- P2-007: internal/app/state.go (explicit states/transitions).
- P2-008: internal/event/event.go (generation+seq+outcome) + app filter.
- P2-009: internal/terminal/frame.go single Writer.
- P2-010: internal/session/timeline.go TranscriptItem.
- P2-011: timeline CurrentView/AllHistoryView.
- P2-012: internal/app/activation.go transaction.
- P2-013: cancel path (busy-only, idempotent, stress_test.go).
- P2-014: settlement barrier (persistence before RunEnd + drain before Draw).
- P2-015: uniseg graphemes + width.go (pi utils.ts).
- P2-016: layout.go shared + goldens.
- P2-017: key-action table (visual Up/Down, Ctrl+P/N, word, line, doc).
- P2-018: input.go Decoder (50ms, 256B, splits, CRLF, drain, replies).
- P2-019: paste txn (single Insert+Draw, no submit/cancel).
- P2-020: 256 KiB budget, grapheme-prefix, whole-edit reject (A02).
- P2-021: PromptRecall 200 + SetHistory on activation.
- P2-022: Complete() + Tab (bounded list, no exec).
- P2-023: viewport resize-safe (1-col, very short).
- P2-024: lifecycle (clear/submit/cancel/EOF/exit, stale recall cleared, blank local).
- P2-025: atomic frames (WriteBounded, no separate clear).
- P2-026: changed-row + cursor-only + zero-byte unchanged.
- P2-027: scrollback append-only (no ED2/3J).
- P2-028: 30fps coalesce + 64 KiB + backpressure + fake clock.
- P2-029: commitAccepted once (scrollback + session, no duplicate).
- P2-030: reconcile streamed vs authoritative (suffix-only, contradiction error).
- P2-031: compact tool states (throttled, no dumps) + 100-call.
- P2-032: fallback only (no synchronized), tail-only resize.
- P2-033: sanitize ANSI/OSC/DCS/C1/bidi/split (sanitize_test.go).
- P2-034: term restore + SIGTSTP/CONT/HUP + PTY (pty_test.go, lifecycle_test.go).
- P2-035: startup replay before prompt (resume_test.go A07).
- P2-036: in-process replay + divider + no-op + generation (acceptance_test.go A09).
- P2-037: tool/interruption replay truthful (timeline + settlement).
- P2-038: compaction/edit/branch display + projection tests (A12/A13).
- P2-039: selector 20/page + filter + stable IDs (A11).
- P2-040: selector cancel/invalid preserves + reprompt (A10).
- P2-041: /history N/all display-only (A14).
- P2-042: bounded batches + cancellable replay.
- P2-043: psi.input custom provenance + recall prefers invocation (A17/A18).
- P2-044: explicit cross-workspace/in-memory/empty/metadata/offline paths.
- P2-045: commands_test.go table drives help+parser.
- P2-046: /model list + syntax + validate-then-record.
- P2-047: /thinking levels + session-local.
- P2-048: /new (keep scrollback, reset recall) + /quit (settle+restore).
- P2-049: /session (provider/api/model/continuation, no $0.00, no secrets).
- P2-050: precedence_test.go (new vs resumed, offline, CLI usage error).
- P2-051: resources_test.go declined stays out.
- P2-052: skills discover/invoke portable journey.
- P2-053: grouped diagnostics + quiet preserves blocking + next action.
- P2-054: Submit/SubmitWithProvenance ordering.
- P2-055: settlement_test.go (terminal/stored/next agree, unsaved explicit).
- P2-056: aborted partial (no executable call).
- P2-057: runBatch persist each + stop unstarted + distinct outcomes.
- P2-058: CheckContinuable + Unresolved block (no auto rerun).
- P2-059: storage failure stops + unsaved + stress.
- P2-060: generation filter (no cross-session write/display).
- P2-061: guards + EOF/signal/suspend + stress no leaks.
- P2-062: audit_test.go v3 shapes.
- P2-063: projection branch/state (siblings excluded, recall branch-only).
- P2-064: ReplaySystem order + checkpoint + stale tools excluded.
- P2-065: compaction/edits projection (retain-none, checkpoints, no file mutate).
- P2-066: index.go offsets/on-demand + perf_test.go.
- P2-067: append/ownership (prefix, truncation, replacement, lock, perms).
- P2-068: interchange_test.go Pi↔psi + reference projector.
- P2-069: catalog_test.go bundled + overrides.
- P2-070: buildRequest roles/grouping/signatures/namespaces.
- P2-071: thinking/budget/effort + image guard.
- P2-072: SSE splits/usage/EOF + authoritative before exec.
- P2-073: error/retry (no invisible replay).
- P2-074: codex_auth_test.go refresh (preserve/lock/0600/atomic, no fallback).
- P2-075: accounting separate + no $0.00 + /session agrees.
- P2-076: offline PTY journeys + liveprovider tag (explicit approval).
- P2-077: read parity (bounds/offsets/images).
- P2-078: write (mkdir/perms/symlinks, truthful).
- P2-079: edit (legacy/BOM/CRLF/Unicode, no partial).
- P2-080: bash (prefix/cancel/descendants/private temp).
- P2-081: bounds_test.go (50KiB/32MiB/256KiB, UTF-8, ownership).
- P2-082: resources_test.go precedence via app.
- P2-083: skill-to-tool journey (read + explicit + restart).
- P2-084: coding_task_test.go disposable + resume.
- P2-085: Estimator (system/tools/images, estimate vs usage).
- P2-086: Boundary (calls+results, zero/long, kept IDs).
- P2-087: bounded summary (budget, no reasoning, failure preserves).
- P2-088: Compact compatible + checkpoint + pi comparison.
- P2-089: compact UX (static, settled, no redraw).
- P2-090: Recovery ≤1 + no repeat (recovery_test.go).
- P2-091: long-session restart (recovery_test.go).
- P2-092: scripts/check.sh (fmt/vet/test/race/build) + A30.
- P2-093: perf_test.go thresholds + generator params.
- P2-094: matrix_test.go + pty_test.go (NO_COLOR, fallback, resize, scrollback).
- P2-095: stress_test.go (cancel/activation, failures, no leaks).
- P2-096: privacy_test.go (synthetic, allowlisted, no imports, no secrets).
- P2-097: docs/ (quickstart/config/keyboard/recovery/sessions/limitations match binary).
- P2-098: root psi (go build, ignored, platform/Go/rev recorded).
- P2-099: plain ./psi --help/--version + isolated PTY journey.
- P2-100: this ledger (no PLAN3, permanent boundaries).

Synthetic only. No real credentials, paths, file contents, or prompts.
Regression subprocesses use temp HOME/workspace/agent/session dirs and an allowlisted env.

## Artifact (P2-098,099)

- Binary: `psi` at repo root (git-ignored), built via `go build -o psi ./cmd/psi`.
- Platform: Linux/WSL2, Go 1.26.4, source rev `ref: refs/heads/main` (see `git log`).
- Checksum: see `sha256sum psi` at release time (not committed).
- Exercise: `./psi --help/--version` (non-terminal) + PTY journey (`--no-session`, startup heading, divider, prompt, `/quit`, modes restored) via isolated HOME/agent/work.
