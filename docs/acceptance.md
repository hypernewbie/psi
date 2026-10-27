# PLAN2 acceptance ledger

## Status

**PLAN2 is not release-complete.** Passing tests does not close every task or user journey.
The earlier review withdrew unsupported blanket pass claims.
This continuation starts from `085c51a` and adds indexed activation, actual Pi interchange, and public catalog support.

The compatibility reference is Pi revision `d5b3de22d0e24a3e9c38a9410d3329b099717283`, session v3.
The reference adapter uses the unmodified reader and writer from that checkout.
Its manifest records the revision and reader hash.
No live provider request ran during this continuation.

## Release gates

“Partial” means that requirements or evidence remain incomplete.
A passing fixture does not replace a required application journey or human terminal check.

| Gate | Tasks | Status | Evidence and remaining requirements |
|---|---|---|---|
| G01 | 001–006 | Partial | Maintained VT emulator, intermediate writes, PTYs, and canary rejection have tests. Complete harness instrumentation remains open. |
| G02 | 007–014 | Partial | One console owns output. Failed durable configuration changes preserve the exact prior runtime. Full transition coverage remains open. |
| G03 | 015–024 | Partial | Graphemes, paste, recall, Escape timing, whole-edit limits, and visible budget rejection pass. Complete keyboard and latency evidence remains open. |
| G04 | 025–034 | Partial | Changed rows, bounded writes, maintained-emulator scrollback, coalescing, and restoration pass. Sustained timing and human terminal checks remain open. |
| G05 | 035–044 | Partial | Startup history, display-only history, lazy replay, titles, unavailable markers, and complete discovery have tests. Full activation journeys remain open. |
| G06 | 045–053 | Partial | The paged model catalog, effective thinking, stored accounting, and configuration transactions have tests. Complete consent and skill journeys remain open. |
| G07 | 054–061 | Partial | Settlement and sticky storage blocking pass. Twelve injected open/write/short-write/sync/close/stat cases preserve logical state. Full application fault journeys remain open. |
| G08 | 062–068 | Partial | The live index and both pinned-reader continuation directions pass for both provider metadata forms. Complete ownership and filesystem edge coverage remains open. |
| G09 | 069–076 | Partial | All 24 advertised models have application request checks. Adaptive HTTP, protocol replay, and synthetic OAuth tests pass. Approved live acceptance remains open. |
| G10 | 077–084 | Partial | Four-tool tests and a failing-then-fixed coding fixture pass. Complete parity and resource journeys remain open. |
| G11 | 085–091 | Partial | Context estimates include imported images and tool schemas. Bounded recovery and pinned compaction continuation pass. Complete long-session application journeys remain open. |
| G12 | 092–100 | Partial | Routine checks and two quantified resource gates pass. Full timing, platform, privacy-artifact, and delivered-binary acceptance remain open. |

## New regression evidence

All provider requests in these tests use synthetic responses or loopback servers.

| Files | Behavior covered |
|---|---|
| `internal/session/indexed_test.go` | Indexed/eager projection, branch timelines and recall, metadata without payload retention, titles, unavailable sessions, locks, opaque details, external changes, and twelve durability failures. |
| `internal/session/ownership_test.go` | Canonical symlink ownership, hard-link refusal for writes, pre-open replacement, and real read-only permission failure. |
| `internal/session/reference_matrix_test.go` | Actual Pi-generated branches, system patches, tools, images, opaque data, context edits, compaction, and both provider metadata forms. Both continuation directions preserve prefixes. |
| `internal/app/scale_test.go` | Actual store and activation for a 512 MiB fixture. Complete discovery and a usable first page from 10,000 files. |
| `internal/app/catalog_test.go` | All 24 public models through application configuration, protocol fields, tool grouping, replay metadata, thinking levels, catalog paging, and failed durable changes. |
| `internal/app/model_picker_test.go` | Actual-app filtering, offline model selection, effective thinking, command-free recall, durable choices, and restart. |
| `internal/app/standalone_test.go` | A copied executable with only bash on its PATH completes all four tools without Node or a reference checkout. |
| `internal/app/accounting_test.go` | Stored usage after compaction and restart, optional cache/reasoning counters, requested/returned identities, and explicit unknown prices. |
| `internal/provider/accounting_test.go` | Managed adaptive thinking through HTTP/SSE, effective effort, returned identity, and one-hour cache counters. |
| `internal/provider/{adaptive,replay_profiles}_test.go` | Required default effort, valid historical markers, foreign signature conversion, stable call/result IDs, and images after failed-result batches. |
| `internal/terminal/maintained_test.go` | Intermediate frame writes and native scrollback through `charmbracelet/x/vt`, plus overwrite, erase, wrap, cursor, and alternate-screen assertions. |
| `internal/terminal/input_limit_test.go` | Whole-edit rejection, exact-budget insertion, one visible typed-input notice, and recovery after deletion. |
| `internal/agent/context_estimate_test.go` | Imported image arrays, effective system text, and actual tool-schema text contribute to context estimates. |

The previous review regressions remain in `internal/{terminal,app,agent,session}/review_test.go`.
Isolation and artifact-canary checks remain in `internal/testutil`.
The prior suspend regression passed 40 consecutive runs after the SIGCONT barrier fix.

## Acceptance cases

| Cases | Requirements | Current evidence boundary |
|---|---|---|
| A01–A03 | Multiline editing, 256 KiB paste, Unicode clusters | Partial regression coverage. Complete visible keyboard and timing journeys remain open. |
| A04–A06 | Tiny deltas, resize states, tool-heavy runs | Partial regression coverage. Sustained timing remains open. |
| A07–A08 | Startup resume and every selection form | Startup history covered. Full selection matrix remains open. |
| A09–A11 | A→B→A, cancellation, large candidate lists | Discovery and paging covered. Full actual-app switching journeys remain open. |
| A12–A13 | Branches, compaction, context edits | Eager/indexed parity and actual pinned projections pass. Complete visible journeys remain open. |
| A14–A15 | Display-only history and interrupted tools | Application regressions pass. Full interruption matrix remains open. |
| A16–A18 | Reasoning/images, custom display, skill provenance | Pinned continuation preserves metadata and provenance. Complete provider-to-restart UI journeys remain open. |
| A19–A20 | Model/thinking restart and credentials | Catalog, effective thinking, and transaction tests pass. Complete visible journeys remain open. |
| A21–A22 | Codex refresh and cancellation after rotation | Synthetic OAuth tests pass. Complete application journeys remain open. |
| A23–A25 | Cancellation, storage failures, malformed or changed sessions | Expanded storage matrix passes. Complete visible fault journeys remain open. |
| A26 | One bounded context-limit recovery | Loop/provider regressions pass. Full protocol journey remains open. |
| A27 | Pi→psi→Pi continuation | Passed the actual pinned continuation matrix for both provider metadata forms and both origins. |
| A28 | Indexed 512 MiB activation | Passed the measured local fixture, including projection, recall, and early replay stop. |
| A29 | Exit, signals, suspension, restoration | Repeated actual-app PTYs pass. Full signal and terminal matrix remains open. |
| A30 | Copied binary without Node or catalog network access | Passed the copied-executable four-tool journey with an isolated PATH and local protocol server. |
| A31 | Private-data canaries | Positive and negative regressions pass. Complete artifact audit remains open. |
| A32 | Final root executable | Existing PTY journeys cover the binary. The complete acceptance matrix remains open. |

## Resource measurements

Profile: Linux/amd64, WSL2 kernel `6.6.87.2-microsoft-standard-WSL2`, Go `1.26.4`, local temporary storage.
Fixtures are newly generated, with warm filesystem caches.
These results do not establish cold-cache or network-filesystem performance.

Three consecutive runs produced:

| Workload | Observed | Required limit |
|---|---|---|
| At least 512 MiB, 15,328 historical tool records, small compacted active context | Activation 341–368 ms. Peak RSS 38,616–40,280 KiB. | At most 3 seconds. Less than 160 MiB RSS. |
| 10,000 session files to complete metadata discovery and rendered first page | 386–448 ms | At most 2 seconds |

The large-session child calls the real `Store.OpenFile` and `PrepareActivation` paths.
Its projection contains three active messages and its recall contains two prompts.
A separate callback stops history replay after five items without exposing hidden tool bodies.
The store retains offsets and small metadata instead of historical payloads.

The 200-file metadata smoke and approximately 2 MiB index smoke remain smoke tests only.
They do not establish these resource gates.

Remaining required measurements:

- Ten idle seconds without animation redraws or animation wakeups.
- 10,000 tiny deltas per second with ordered text and at most 30 scheduled frames per second.
- Editor response p95 at most 50 ms and cancellation dispatch p95 at most 100 ms.
- Shell-descendant cleanup within 2 seconds on the reference workload.
- A 256 KiB paste visible within 200 ms after its end marker.
- Complete cancellable-replay memory and output measurements.
- The full supported terminal and platform matrix, including human flicker and selection checks.

These are unfinished PLAN2 requirements, not permanent product exclusions.

## Reproduction

Run routine checks:

```sh
./scripts/check.sh
```

Build the adapter and run actual pinned interchange:

```sh
node scripts/build-reference.mjs temp/pi-mono temp/pi-reference.mjs
PSI_PI_SESSION_MODULE="file://$PWD/temp/pi-reference.mjs" \
  go test ./internal/session -run '^TestReferencePi' -count=2 -v
```

Run the explicit resource gates:

```sh
go test -tags acceptance ./internal/app \
  -run '^TestScale(512MiBActivation|10000MetadataFirstPage)$' -count=3 -v
```

Run repeated application regressions and the same regressions against the root executable:

```sh
go test ./internal/app -run '^TestActual' -count=10
go test ./internal/app -run '^TestActual' -count=3 \
  -args -psi-test-binary="$PWD/psi"
```

Run the bounded indexed-parser fuzz check:

```sh
go test ./internal/session -run '^$' -fuzz '^FuzzIndexedMetadata$' -fuzztime=3s -parallel=2
```

Live tests require the `liveprovider` build tag and explicit operator approval.
An inherited environment flag alone cannot compile live provider tests.
No live test forms part of routine acceptance evidence.

## Local continuation results

- Routine checks passed, including formatting, vet, all tests, race detection, and build.
- The actual pinned projection and four continuation journeys passed twice consecutively.
- Both resource gates passed three consecutive runs.
- The indexed-parser fuzz check passed with two workers and 58,683 executions.
- Actual-app PTY regressions passed ten consecutive helper runs and three consecutive root-executable runs.
- The copied executable completed all four tools with Node absent from its PATH and no reference checkout.
- The offline actual-binary coding smoke also passed without repeated provider requests or tool work on resume.
- Vci submission failed because its client configuration was absent. All reported checks ran locally.

The root `psi` executable is ignored by Git.
The final handoff records its source commit and build separately.
These actual-binary journeys do not establish the complete release matrix.
