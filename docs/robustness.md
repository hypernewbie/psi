# Robustness ledger (PLAN3)

**PLAN3 acceptance remains incomplete.** Implementation commits do not close unrun acceptance cells.
This review starts at `2bb1c55`, after the original implementation against baseline `d45a88b`.
The pinned Pi revision is `d5b3de22d0e24a3e9c38a9410d3329b099717283`, with session JSONL v3.

## Evidence classes

| Label | Meaning |
|---|---|
| Reproduced | A synthetic psi probe observed the defect. |
| Reference executed | A probe ran pinned Pi source or the stated Node primitive. |
| Reference reviewed | The comparison uses pinned Pi source. |
| Review risk | A code path needs a deterministic test. No failure claim applies. |
| Shared limitation | Pi shares the limitation. No new psi requirement applies. |
| Existing protection | An executable regression covers the existing safeguard. |

Survey probes report the observed defect, not successful acceptance.
Permanent regressions assert the corrected behavior.
The tests use synthetic data, isolated subprocess environments, and loopback provider fixtures.
No live provider calls ran.

## Finding ledger

“Corrected” describes the implementation and the listed regressions, not completion of every stress profile.
Reference anchors R01–R18 remain in `temp/PLAN3.md`, section 4.
Reference provenance and optional test inputs are in [reference-manifest.md](reference-manifest.md).

| ID | Finding | Correction and executable evidence |
|---|---|---|
| F01 | Auth reads ignore the writer lease | Corrected. Both credential readers coordinate through the lease. Torn-write and mixed Pi/psi tests pass. |
| F02 | Abandoned auth leases never recover | Corrected after review. Empty lock directories permit Pi recovery. Identity checks, heartbeat advancement, replacement ownership, and killed-holder recovery have regressions. |
| F03 | OAuth refresh changes existing permissions | Corrected. Tests cover modes 0600/0640/0660, unrelated fields, chmod failure, rename failure, and temporary-file cleanup. |
| F04 | Lazy reads mix file generations | Corrected. Descriptor identity, size, mtime, and record hashes guard lazy reads. Mutation barriers cover rewrite, replacement, truncation, append, and unlink. |
| F05 | Decoders accept case aliases | Corrected after review. Schema-based raw views replace the hand scanner. Semantic regressions cover headers, images, usage, duplicates, provenance, and nested exact keys. |
| F06 | Mutation continues after cancellation | Corrected after review. Checkpoints cover queue waits, reads, transforms, and writes. Barriers prove settlement of an in-flight write before return. |
| F07 | A fixed drain deadline loses descendant output | Corrected after review. Idle grace starts at leader exit. Post-exit cancellation kills the group. Closing the reader bounds quiet escaped-pipe waits. |
| F08 | Anthropic accumulation is quadratic | Corrected. Builders and 1k/10k allocation tests cover text, thinking, signatures, and many-block assembly. The 100k cell remains unrun. |
| F09 | Word deletion repeatedly scans prefixes | Corrected after review. ASCII navigation needs no grapheme table. Symbol and emoji cases match pinned Pi. CJK dictionary segmentation remains different. |
| F10 | Multiline status breaks row accounting | Corrected. Console and VT helpers cover single-row status and a committed scrollback sentinel. The provider-to-app journey remains unrun. |
| F11 | Recall bypasses the editor limit | Corrected. Whole-draft admission covers history installation and recall. Oversized prompts produce a notice without a truncated replacement. |
| F12 | Stream failure discards known usage | Corrected after review. Cancellation retains typed partial metadata. An HTTP fixture proves durable usage exactly once, correct provider identity, and no tool execution. |
| F13 | Tool modes differ from Node defaults | Corrected. Child-process umask tests cover 0002/0022/0077 and Node agreement. Existing-file and symlink cases retain their behavior. |
| F14 | Compaction threshold addition can overflow | Corrected after review. Subtraction preserves strict comparison, including empty context with reserve above the window. Boundary and monotonicity tests pass. |
| F15 | Discovery retains skill bodies | Corrected. Discovery retains metadata. Tests cover 16/256 skills, retained heap, invocation after file changes, and symlink cycles. |

## Additional review corrections

- The original auth lease contained an owner file. Pi uses directory removal, which fails for a nonempty directory.
- The shell drain used the last pre-exit chunk as its timer origin. A quiet leader consumed the descendant grace period.
- The metadata scanner rejected valid image results and lost usage totals. Identity-only fuzz comparisons did not detect these differences.
- Cancellation replaced a typed stream interruption with a bare context error. The agent then lost known usage.
- Interrupted usage records used an API name instead of the provider identity.
- An edit waiter checked cancellation before its mutex wait, but not before the subsequent read.
- Word navigation treated emoji and symbols as letters. Ctrl+W also deleted the preceding word.
- The compaction special case changed the strict threshold for an empty context with an excessive reserve.
- Fault tests now close injected descriptors and assert unchanged logical state and an intact durable prefix.
- The status helper no longer claims to run the actual app. Its scrollback sentinel is now an executable assertion.

## Local measurement record

Host: Linux amd64, WSL2 kernel `6.6.87.2-microsoft-standard-WSL2`, ext4, AMD Ryzen 9 3950X.
Toolchain: Go 1.26.4, module Go 1.24.2, Node v24.11.1.
Review implementation revision: `414202c`, followed by test and ledger corrections.
Absolute performance measurements exclude race instrumentation.
Helper timings do not establish actual-app latency budgets.

| Profile | Evidence | Status and missing cells |
|---|---|---|
| S01 Auth contention | Two concurrent refresh clients, leased readers, mixed Pi/psi fixtures. Targeted race suites pass ten repetitions. | Partial. Eight child clients and 100 mixed operations remain unrun. |
| S02 Lease failure | Stale/fresh/replaced leases, cancelled waits, observed heartbeat advancement, killed child holder, and Pi recovery. | Partial. The coarse-clock schedule remains unrun. |
| S03 Lazy mutation | Deterministic generation tests and indexed/eager semantic regressions pass. | Partial. Complete every lazy-API and provider-request-count cell before acceptance. |
| S04 Storage faults | Open/write/short-write/sync/close/stat faults stop further writes. Append tests assert unchanged logical state and an intact prefix. | Partial. First-flush and full app fault journeys remain incomplete. An uncertain appended tail is not rolled back. |
| S05 Delta scale | Existing 1k/10k allocation regressions pass the relative 15-fold growth limit. | Partial. The 100k cell and complete heap profiles remain unrun. |
| S06 Editor scale | One-word helpers cover 1/4/64/256 KiB. The 256-KiB ASCII helper took 0.316 ms in the recorded sample. | Partial. Mixed-Unicode and actual-app p95 measurements remain incomplete. |
| S07 Large paste | Decoder and console helpers cover whole-edit admission or rejection at 256 KiB. | Partial. PTY end-marker-to-frame timestamps remain unrun. |
| S08 Cancellation | Queue-wait, in-flight write, stream interruption, and post-exit shell cancellation regressions pass. | Partial. The 100-sample dispatch and descendant-liveness profile remains unrun. |
| S09 Idle and frames | Scheduler, bounded-write, and flash regressions pass. | Partial. The actual-app ten-second idle and 10k-delta trace remain unrun. |
| S10 Shell lifecycle | Active chunks, quiet pipes, failures, timeout, post-exit cancellation, and escaped quiet pipes have regressions. | Partial. The 100-run descriptor/process profile remains unrun. |
| S11 Skills | Metadata retention, fresh invocation, duplicate, and cycle tests pass for the recorded fixtures. | Partial. Complete the post-GC heap-profile evidence before closing the full profile. |
| S12 Session scale | Three runs: activation 2443–2466 ms, peak RSS 40492–44248 KiB, 15331 entries. Discovery first page: 698–746 ms. | Pass for these fixtures. Budgets are 3 s, 160 MiB RSS, and 2 s discovery. |
| S13 Lifecycle | Existing schedule, stale-generation, and actual-app regressions pass. | Partial. The 100 lifecycle schedules remain unrun. |
| S14 Full replay | Lazy replay and hidden-payload regressions pass. | Partial. Large visible-history RSS, output-size, and canary measurements remain incomplete. |

### Review checks

| Check | Result | Scope |
|---|---|---|
| `./scripts/check.sh` | Pass | Local formatting, vet, ordinary tests, and race tests. |
| Semantic session fuzzing | Pass, 6394895 executions over 300 seconds | Eager/indexed agreement across retained semantic state, not just record identity. |
| Targeted auth and shell race suites | Pass, ten repetitions | Includes explicit public `proper-lockfile@4.1.2` input. |
| Pinned Pi session matrix | Pass, two repetitions | Both continuation directions and both supported provider families. |
| Pinned Pi word navigation | Pass, three repetitions | 24 supported cases. CJK dictionary segmentation remains a documented exception. |
| Actual-app executable suite | Pass, three repetitions | Explicit binary input, including copied-executable four-tool coverage without Node on PATH. |
| Vci | Blocked | No client configuration. All reported checks ran locally. |

The old 45M-execution claim used a weaker identity comparison.
It does not establish the stronger semantic property from this review.
Other fuzz targets need their final timed release runs before full PLAN3 acceptance.
The delivered executable revision and SHA-256 accompany the final handoff.

## Gate status

No full PLAN3 gate closes from these helper results alone.

| Gate | Status | Remaining evidence |
|---|---|---|
| G3-01 Baseline and provenance | Partial | Complete retained artifact and fixture-hash records. |
| G3-02 Shared auth | Partial | Full S01/S02 profiles and remaining rotation-failure cells. |
| G3-03 Session reads and semantics | Partial | Complete S03 API coverage and acceptance mapping. |
| G3-04 Tools and processes | Partial | Full S04/S08/S10 fault and cleanup profiles. |
| G3-05 Provider resources and settlement | Partial | The 100k allocation cells and remaining timed fuzz runs. |
| G3-06 Terminal and editor | Partial | Actual-app latency, paste, idle, frame, platform, and human checks. |
| G3-07 Numeric, discovery, race, and faults | Partial | Complete arithmetic audit, heap profiles, lifecycle schedules, and fuzz budgets. |
| G3-08 Executable and evidence ledger | Partial | Remaining actual-app measurements and supported-platform cells. |

Unavailable SSH and human terminal cells remain blocked.
No emulator result replaces selection, copy, flicker, or native-scrollback checks by a person.
These open requirements remain separate from the PLAN2 acceptance ledger.

## Scope rule

Pi defines the retained feature boundary.
Shared limitations do not become new psi requirements.
Go-specific regressions remain in scope.
Existing psi safeguards include bounded input, strict session reads, and refusal after uncertain storage writes.
Atomic tool rollback, hostile-filesystem snapshots, and concurrent Pi/psi session writers remain outside scope.

The product retains four tools, two provider families, and an inline main-screen terminal.
It excludes extensions, MCP, RPC, sandboxing, login, telemetry, and updates.
Live provider tests require explicit operator approval and the `liveprovider` build tag.
