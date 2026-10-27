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
- P2-007…P2-100: see phase commits; each task has a focused test or recorded no-change result with source revision.

Synthetic only. No real credentials, paths, file contents, or prompts.
Regression subprocesses use temp HOME/workspace/agent/session dirs and an allowlisted env.

## Artifact (P2-098,099)

- Binary: `psi` at repo root (git-ignored), built via `go build -o psi ./cmd/psi`.
- Platform: Linux/WSL2, Go 1.26.4, source rev `ref: refs/heads/main` (see `git log`).
- Checksum: see `sha256sum psi` at release time (not committed).
- Exercise: `./psi --help/--version` (non-terminal) + PTY journey (`--no-session`, startup heading, divider, prompt, `/quit`, modes restored) via isolated HOME/agent/work.
