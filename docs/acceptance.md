# Acceptance ledger (PLAN2)

Pinned reference: `temp/pi-mono` rev `d5b3de22d0e24a3e9c38a9410d3329b099717283`, session v3.
All rows are mandatory. Evidence links to tracked synthetic fixtures/tests.

## Gates

| Gate | Result | Tasks | Evidence |
|---|---|---|---|
| G01 | pending | 001–006 | docs/acceptance.md, terminal screen-model, repro tests, privacy canaries |
| G02 | pending | 007–014 | app state, ordered events, one writer, transcript items, settlement |
| G03 | pending | 015–024 | grapheme editor, layout, input decoder, paste txn, recall, completion |
| G04 | pending | 025–034 | atomic frames, changed-row, scrollback, scheduling, sanitization, restore |
| G05 | pending | 035–044 | startup/in-process resume, tool replay, compaction display, paging, /history |
| G06 | pending | 045–053 | help/model/thinking/new/quit/session/config/instructions/skills |
| G07 | pending | 054–061 | persistence, settlement, interruption, storage failure, stale-run, guards |
| G08 | pending | 062–068 | v3 audit, branch/state, system/tool replay, compaction/edits, index, interchange |
| G09 | pending | 069–076 | catalog, request, thinking, streaming, retry, refresh, accounting, journeys |
| G10 | pending | 077–084 | read/write/edit/bash, bounds, precedence, skill journeys, coding task |
| G11 | pending | 085–091 | accounting, boundaries, summary, persistence, UX, recovery, restart journey |
| G12 | pending | 092–100 | matrix, perf, terminal matrix, stress, privacy, docs, artifact, binary, ledger |

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
