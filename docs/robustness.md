# Robustness ledger (PLAN3)

Reference: `temp/pi-mono` rev `d5b3de22d0e24a3e9c38a9410d3329b099717283`, session v3.
psi baseline: `d45a88b8563b7c1cde13e0f33f89b9abacb6cf24`.
Environment: Linux amd64, WSL2, Go 1.26.4 (module declares 1.24.2), Node v24.11.1.

## Evidence classes

| Label | Meaning |
|---|---|
| Reproduced | Synthetic psi probe observed the stated behavior. |
| Reference executed | Probe ran pinned Pi source or stated Node primitive. |
| Reference reviewed | Comparison from pinned Pi source. |
| Review risk | Code path needs deterministic test; no failure claim. |
| Shared limitation | Pi shares it; no new psi requirement. |
| Existing protection | Current implementation already guards/tests. |

Survey probes that observe a target defect report `PASS` as instruments,
not as acceptance. Permanent regressions assert corrected behavior.

## Finding ledger

| ID | Pri | Finding | Evidence | Status |
|---|---|---|---|---|
| F01 | P1 | Auth reads ignore writer lease | Reproduced, reference reviewed (R01); `auth_lease_test.go` concurrent read-during-exclusive-write, SelectAPIKey lease + missing-file env fallback, 8-client + Pi mixed contention, failover successor allowed | Fixed (P3-007–012) |
| F02 | P1 | Abandoned auth leases never recover | Reproduced, reference executed (R02); stale/ownership/heartbeat matrices, crash/partial-write successor, mode-preserving revocation | Fixed (P3-007–012) |
| F03 | P2 | OAuth refresh changes existing permissions | Reproduced, reference executed; refresh preserves mode/0600/atomicity, symlink/permission/disk faults, single pre-stream recovery, token-auth mismatch refused | Fixed (P3-007–012) |
| F04 | P1 | Lazy reads mix file generations | Reproduced, reference reviewed (R03); pinned inode+hash+size+mtime identity, per-read recheck, LoadHook seam, same-length/same-ID rewrite, external replacement, truncation, mid-read replacement matrices | Fixed (P3-013–018) |
| F05 | P1 | Decoders accept case aliases | Reproduced, Node primitive executed; exact probes, byte-exact hand scanner, alias-defold, 22M+120s differential fuzz execs, 7 fuzzer-found divergences fixed and pinned as corpus | Fixed (P3-013–018, P3-049–054) |
| F06 | P1 | Mutation continues after cancellation | Reproduced with barriers, reference reviewed (R05/R06); context-aware checkpoints, settlement barrier, sequential dispatch, persistence-before-RunEnd | Fixed (P3-019–024) |
| F07 | P1 | Fixed drain deadline loses descendant output | Reproduced, reference executed (R08); idle-grace drain (100ms re-armed), 16-chunk Pi-parity timing 0.67s, quiet-pipe status preservation, group cleanup | Fixed (P3-025–030) |
| F08 | P1 | Anthropic accumulation quadratic | Reproduced, source reviewed, V8 microcheck; amortized builders, allocation gate ≤15x per 10x (measured ~10x), 1k/10k text+thinking+signature benchmarks | Fixed (P3-031–036) |
| F09 | P1 | Word deletion scans prefixes quadratically | Reproduced, reference executed (R13); single-pass segmentation, 18/19 Pi parity + documented CJK dictionary divergence, 256 KiB in 69ms (was ~days extrapolated) | Fixed (P3-037–042) |
| F10 | P1 | Multiline status breaks row accounting | Reproduced, reference reviewed (R14); SanitizeStatusRow single-line boundary, VT emulator + scrollback-sentinel tests | Fixed (P3-037–042) |
| F11 | P2 | Recall bypasses editor limit | Reproduced, psi invariant defect; whole-draft admission on SetHistory/Prev/Next, skip-with-notice, draft preserved | Fixed (P3-037–042) |
| F12 | P2 | Stream failure discards known usage | Reproduced, reference reviewed (R10/R11/R12); StreamInterrupt partial path, usage exactly once (777-input test), no retry after partial, no unfinished-tool execution, compaction coverage | Fixed (P3-031–036) |
| F13 | P2 | Tool modes differ from Node defaults | Reproduced, Node primitive executed; 0666/0777 modes, child-process umask matrix 0002/0022/0077 + Node reference agreement, existing/symlink preservation | Fixed (P3-019–024) |
| F14 | P2 | Compaction threshold addition can overflow | Reproduced, arithmetic reviewed (R15); subtraction-form threshold, MaxInt boundaries, monotonicity, related-arithmetic audit | Fixed (P3-043–048) |
| F15 | P2 | Discovery retains full skill bodies | Reproduced, reference reviewed (R16); metadata-only discovery, ≤4 MiB retained for 256×128 KiB bodies, invoke-after-edit freshness | Fixed (P3-043–048) |

Reference anchors R01–R18: see `temp/PLAN3.md` §4.
Installed `proper-lockfile@4.1.2` provenance recorded in `docs/reference-manifest.md`.

## Measurement record (Phase J, P3-055–060)

Host: Linux amd64 (WSL2 kernel 6.6.87.2-microsoft-standard), AMD Ryzen 9 3950X (32 threads), ext4, Go 1.26.4, Node v24.11.1. Measurements are helper-level unless noted; race instrumentation excluded. Unrun cells stay partial — a passing fixture never replaces a required journey.

| Profile | Workload run | Result | Status |
|---|---|---|---|
| S01 Auth contention | 8-client + mixed Pi/psi contention suites, repeated `-race -count=10` | No torn parse, stale-owner commit, or leak | Pass (`auth_lease_test.go`, repeated race profile) |
| S02 Lease failure | stale/fresh/replaced/killed/cancelled/coarse-clock schedules | One current owner, bounded recovery | Pass |
| S03 Lazy mutation | rewrite/append/truncate/rename/unlink barriers on every lazy API | No stale context success, no provider request on failed projection | Pass (`generation_test.go`) |
| S04 Storage faults | open/write/short-write/sync/close/stat injection + terminal/spool faults | Fail-stop, bytes and logical state unchanged | Pass (`fault_test.go`, `TestF53*`) |
| S05 Delta scale | 1k vs 10k fixed 16-byte deltas (text, thinking+signatures, many-block) | Allocation ratios 9.8–10.4x per 10x (gate ≤15x); exact output | Pass at 1k/10k; 100k cell partial (not run) |
| S06 Editor scale | 1 KiB / 4 KiB / 64 KiB / 256 KiB single words | 236µs / 1.0ms / 22ms / 69ms, linear | Pass (helper); actual-app timings partial |
| S07 Large paste | 256 KiB paste paths via decoder/console tests | Whole-edit admit/reject, bounded notice | Pass (helper); PTY input-to-frame timestamps partial |
| S08 Cancellation | dispatch/tool/compact/cancel + drain group-kill tests | Bounded cleanup, no leaked descendants in tested schedules | Partial (100-sample profile not run) |
| S09 Idle and frames | scheduler 30fps/64KiB tests, flash bounded-frame tests | Coalescing, no ED2/ED3J, ordered text | Pass (helper); 10s-idle + 10k-delta trace partial |
| S10 Shell lifecycle | 6 drain/schedule tests incl. 16-chunk Pi-parity timing (0.67s) | No missing chunks, no leaks, true exit codes | Pass (helper); 100-run profile partial |
| S11 Skills | 16 and 256 skills × 128 KiB bodies | Heap delta <4 MiB (metadata only); invoke-after-edit fresh | Pass (`retention_test.go`) |
| S12 Session scale | 512 MiB fixture: activation 2447ms (<3s), peak RSS 45672 KiB (<160MiB), 15331 entries; 10k-discovery first page 781ms (<2s) | Within budget | Pass (`TestScale512MiBActivation`, `TestScale10000MetadataFirstPage`) |
| S13 Lifecycle | cancel/activate stress, schedule/generation tests | No stale events, no leaked locks/files in tested schedules | Partial (100-schedule profile not run) |
| S14 Full replay | lazy replay + hidden-payload tests | Display tracks current record, no hidden-body output | Pass (helper); large-history RSS profile partial |
| Differential fuzz | exact-key 45M+ execs PASS; decode/index/SSE/editor/decoder 60s+ each PASS | 7 divergences found, fixed, pinned as corpus | Pass |
| Reference matrix | Pi→psi→Pi + pinned projector, both provider families | Agreement | Pass (`TestReferencePi*`, requires `PSI_PI_SESSION_MODULE`) |
| Delivered binary | copied exe, no Node on PATH, 4-tool offline journey | STAN​DALONE_DONE, no body leak | Pass (`TestActualCopiedBinaryWithoutNodeUsesAllFourTools`) |

## Scope rule

Pi defines the boundary. Shared limitations (§5 of plan) do not become psi
requirements. Go-specific regressions stay in scope despite similar sources.
Preserved psi safety choices: bounded input, strict session reads, refusal
after uncertain storage writes.

## Product limits

Four tools (`read/write/edit/bash`), two provider families (Anthropic
Messages, `openai-codex`), inline main-screen terminal, native scrollback,
no tool args/results/diffs in terminal, Pi JSONL v3 compatible, no
extensions/MCP/RPC/sandbox/session-tree/login/telemetry/updates/background
service, no live provider calls without explicit approval.
