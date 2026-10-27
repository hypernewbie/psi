# PLAN2 acceptance ledger

## Review status

**PLAN2 is not release-complete.** The earlier blanket pass claims lacked sufficient evidence.
This review starts from `e3db458` and corrects runtime faults and regression tests.
Passing Go tests does not establish completion of all 100 tasks or 32 acceptance cases.

The compatibility reference is Pi revision `d5b3de22d0e24a3e9c38a9410d3329b099717283`, session v3.
The pinned Pi reader comparison requires a built reference module.
That comparison did not run during this review.

## Release gates

“Partial” means that tests cover some requirements, but the complete gate lacks evidence.
“Blocked” identifies a known missing requirement or unavailable reference evidence.
No row below declares release acceptance.

| Gate | Tasks | Status | Evidence and remaining requirements |
|---|---|---|---|
| G01 | 001–006 | Partial | Screen-model operations, actual output writes, PTY regressions, and canary detection have tests. The complete acceptance harness remains unproven. |
| G02 | 007–014 | Partial | The application uses shared console ownership and drains settled events. The full transition and activation matrix lacks end-to-end evidence. |
| G03 | 015–024 | Partial | Tests cover Backspace, graphemes, source boundaries, UTF-8, paste rejection, recall, and Escape timing. The complete keyboard and latency matrix remains open. |
| G04 | 025–034 | Partial | Tests cover changed rows, cursor movement, short writes, stream coalescing, and terminal restoration. Human terminal checks and sustained timing measurements remain open. |
| G05 | 035–044 | Partial | Actual-app PTY tests cover startup history, `/history all`, and `/new` recall reset. Every activation path and replay-interruption case remains necessary. |
| G06 | 045–053 | Partial | Existing command and configuration tests pass. Full model, offline, consent, and skill journeys lack complete visible-output evidence. |
| G07 | 054–061 | Partial | Tests cover authoritative text, durable partials, unstarted calls, and persistent storage blocking. The complete append/sync/close failure matrix remains open. |
| G08 | 062–068 | Blocked | Synthetic session tests pass. The live store does not use the record index. Actual pinned Pi interchange remains unproven. |
| G09 | 069–076 | Partial | Synthetic provider and OAuth tests pass. This review made no live provider calls. Full catalog and protocol acceptance remains open. |
| G10 | 077–084 | Partial | Tool tests, a failing-then-fixed disposable fixture, and an actual-binary coding smoke pass. Complete parity and resource journeys remain open. |
| G11 | 085–091 | Partial | Tests cover one bounded context-limit recovery and prohibit recovery after partial output. Full compaction and pinned-reader acceptance remains open. |
| G12 | 092–100 | Blocked | Routine checks cover formatting, vet, tests, race detection, and build. Large-session, latency, platform, and complete delivered-binary acceptance remain open. |

## Review regression evidence

These tests exercise specific behavior, not an entire release gate.
All provider requests in these tests use synthetic responses or loopback servers.

| Files | Behavior covered |
|---|---|
| `internal/terminal/review_test.go` | Real cell overwrites and erases, nonduplicated submission, bounded tail updates, target cursor columns, Backspace, paste rejection, decoder limits, grapheme widths, short writes, stream coalescing, split UTF-8, and tool outcomes. |
| `internal/app/review_test.go` | Actual-app PTY history display, Backspace in requests, one submitted draft, display-only history, `/new` recall reset, suspend/resume, restored attributes, readable unsupported selections, and replay cancellation. |
| `internal/agent/review_test.go` | Missing final suffixes, canonical content, contradictory finals, durable interrupted text, and settled unstarted calls without side effects. |
| `internal/agent/recovery_test.go` | A real loop/provider recovery path, one recovery limit, and refusal to retry visible partial output. |
| `internal/agent/coding_task_test.go` | A behavior test fails before the edit and passes through the shell tool afterward. Reopening does not repeat tools. |
| `internal/session/review_test.go` | Whole-view call/result linkage, explicit outcomes, provenance without context changes, bounded index reads, malformed records, sticky storage failures, and hidden image payloads. |
| `internal/testutil/process_test.go` | Direct shell-tool tests cannot inherit parent credentials or `BASH_ENV`. Temporary directories are removed. |
| `internal/testutil/isolate_test.go` | The artifact scanner rejects an actual synthetic canary leak and accepts safe content. |

The suspend regression also ran 40 consecutive times after the SIGCONT barrier fix.
A process-directed SIGSTOP can return on one Go thread before another thread stops.
The application now waits for SIGCONT before it restores raw mode.

## Acceptance cases

The cases below remain mandatory.
A related unit test does not replace the required visible-output and durable-state evidence.

| Cases | Requirements | Review status |
|---|---|---|
| A01–A03 | Multiline editing, 256 KiB paste, Unicode clusters | Partial regression coverage |
| A04–A06 | Tiny-delta streaming, resize states, tool-heavy runs | Partial regression coverage, sustained timing open |
| A07–A08 | Startup resume and every startup selection form | Actual-app startup history covered, full selection matrix open |
| A09–A11 | A→B→A activation, cancellation, more than 100 candidates | Full actual-app journeys open |
| A12–A13 | Selected branches, compaction, context edits | Synthetic projection coverage, full visible journeys open |
| A14–A15 | Display-only history and truthful interrupted tools | Actual-app history and outcome regressions, full interruption matrix open |
| A16–A18 | Reasoning/images, custom display, skill provenance | Synthetic coverage, full provider-to-restart journeys open |
| A19–A20 | Model/thinking restart and unavailable credentials | Synthetic coverage, full visible journeys open |
| A21–A22 | Codex refresh and cancellation after rotation | Synthetic OAuth coverage, complete acceptance open |
| A23–A25 | Cancellation, storage failures, malformed or changed sessions | Regression coverage, complete fault matrix open |
| A26 | One context-limit recovery without repeated tools | Loop/provider regressions, full protocol journey open |
| A27 | Actual Pi→psi→Pi continuation | Blocked on pinned-reader evidence |
| A28 | Indexed 512 MiB session activation | Blocked by the live-store design |
| A29 | Exit, signals, suspension, and restoration | Actual-app Ctrl+Z/SIGCONT coverage, full signal matrix open |
| A30 | Copied binary without Node, reference checkout, or network | Complete standalone journey open |
| A31 | Private-data canaries | Positive and negative regression coverage, complete artifact audit still required |
| A32 | Final root binary with the same acceptance journeys | Complete journey matrix open |

## Performance limits and missing evidence

The live reader retains full payloads and rejects files larger than 256 MiB.
The standalone `RecordIndex` helper does not remove this limit.
The 512 MiB acceptance input therefore exceeds the current live-store limit.
Bounded terminal batches do not make session loading lazy.

`TestMetadataScanSmoke` uses 200 files, not 10,000 files.
`TestRecordIndexSmoke` uses approximately 2 MiB and does not call application activation.
Neither smoke test establishes a PLAN2 performance gate.

Required measurements remain:

- Ten idle seconds without animation redraws or animation wakeups.
- 10,000 tiny deltas per second with ordered text and at most 30 scheduled frames per second.
- Editor response p95 at most 50 ms and cancellation dispatch p95 at most 100 ms.
- Shell-descendant cleanup within 2 seconds on the reference workload.
- A 256 KiB paste visible within 200 ms after its end marker.
- A usable first page from 10,000 metadata entries within 2 seconds on local storage.
- A 512 MiB compacted session active within 3 seconds, excluding deliberate terminal output.
- Peak RSS less than 160 MiB for the specified indexed workload.
- Cancellable replay with bounded output batches and bounded memory.

These are incomplete PLAN2 requirements, not permanent product exclusions.

## Reproduction

Run routine checks:

```sh
./scripts/check.sh
```

Run repeated application regressions:

```sh
go test ./internal/app -run '^TestActual' -count=10
```

Run the same PTY regressions against the rebuilt root executable:

```sh
go test ./internal/app -run '^TestActual' -count=1 -args -psi-test-binary="$PWD/psi"
```

Run the bounded session-decoder fuzz check:

```sh
go test ./internal/session -run '^$' -fuzz '^FuzzDecodeEntries$' -fuzztime=3s -parallel=2
```

Live tests require the `liveprovider` build tag and explicit operator approval.
An inherited environment flag alone cannot compile live provider tests.
No live test forms part of routine acceptance evidence.

## Local review results

- `./scripts/check.sh` passed, including race detection.
- Actual-app PTY regressions passed ten consecutive helper runs and three consecutive runs against the rebuilt executable.
- The 3-second decoder fuzz run passed with two workers and 93,498 executions.
- A static Linux/amd64 root executable built with Go 1.26.4.
- An offline actual-binary coding smoke passed through `read`, `edit`, and `bash`.
- That smoke also covered Backspace, resume/history, unchanged session prefixes, hidden tool payloads, and restored terminal attributes.
- Resume and history issued no additional provider requests or tool calls.
- Vci was unavailable because its client configuration was absent. All reported checks ran locally.

The root `psi` executable is ignored by Git.
Its build and smoke check establish a delivered artifact, not the missing release gates.
The final handoff records the commit and build result separately.
