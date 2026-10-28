# PLAN4 UI acceptance ledger

## Status

The reduced Pi regular-mode UI is implemented and the local automated suite passes. Timer frames, event-boundary frames, writer frames, and maximum write size are now measured in the REPL profile; ten settled idle seconds produce no output. The release gates are still **partial**: remote macOS/Windows tests, native Windows console execution, human terminal review, and the full actual-app resize matrix remain unavailable.

Reference source: Pi `d5b3de22d0e24a3e9c38a9410d3329b099717283` in the ignored `temp/pi-mono` checkout. Latest measured source revision: `51a56e3ca822c529a3e06f59982391eb37701720` (`51a56e3`). The reference was source-reviewed; no pinned Pi comparison session was run.

## Evidence by level

| Level | Evidence | Result |
|---|---|---|
| Component | `go test ./internal/terminal ./internal/app`; Markdown, styled-row, editor, completion, picker, notice, and settlement fixtures | Pass |
| VT emulator | `testutil.VTModel` screen/cursor/scrollback checks across the 120×40, 80×24, 60×16, 40×12, and 20×6 geometry matrix; 1×1 and 2×2 safety cases | Pass for automated cell and cursor assertions |
| Actual app | Synthetic-provider PTYs and subprocess tests, including offline model/thinking selection, resume, startup consent, picker cancellation, suspend/resume, shutdown, and privacy canaries | Pass on this Linux/WSL2 host |
| Human terminal | Native selection/copy, perceived redraw flicker, light/dark theme readability, and Pi side-by-side review | Blocked; no human terminal session was available |

No live provider request was made. Tests use synthetic credentials, temporary workspaces, and local protocol servers.

## Journey ledger

| Cases | Automated evidence | Human/platform status |
|---|---|---|
| A4-01–02 | Inline startup/editor, grapheme editing, literal paste, recall, cursor, overflow, and responsive geometry tests; actual-app PTY coverage | Human readability and copy check blocked |
| A4-03 | Slow-stream and actual-app PTY tests preserve the indicator, footer, editor, and partial text. A loopback provider sends 10,000 one-character deltas in 30 paced batches; order and persistence are checked. The REPL profile separately counts timer and event-boundary frames, writer frames/writes, and maximum write size. | Automated pass; human perception/flicker check remains blocked |
| A4-04–07 | Tool-state privacy/deduplication, shared live/replay Markdown, chunk-boundary, partial-fence, session replay, and settlement tests | Automated pass |
| A4-08–13 | Inline completion/pickers, 10,000-entry search, model/thinking persistence, startup resume, trust default-deny, pending-input preservation, and picker lifecycle tests | Automated pass on Linux PTY; native console checks remain blocked |
| A4-14–17 | Synthetic compaction phase, cancellation settlement, busy paste, storage-failure, and durable/interrupted/unsaved outcome tests | Automated pass |
| A4-18 | Component VT geometry and resize/tail tests; actual-app lifecycle and picker resize coverage | **Partial:** not every operation was exercised at every size in a single actual-app matrix |
| A4-19 | `NO_COLOR`, semantic token, and no-generated-SGR fixtures | Automated pass; light/dark human checks blocked |
| A4-20 | Unix PTY suspend/resume, EOF, shutdown, and termios restoration; comparison masks only macOS PENDIN | Linux pass; macOS/native Windows execution and human shell restoration blocked |
| A4-21 | Malicious control, path, provider, Markdown, tool-body, and secret canaries across display tests | Automated pass for covered paths |
| A4-22 | Native terminal behavior is preserved by the inline writer; no application mouse capture or clipboard path exists | **Blocked:** prose/code selection and copy were not checked by a human |

## Resource profiles

Measurements below were collected at revision `51a56e3` on `Linux Minerva 6.6.87.2-microsoft-standard-WSL2`, AMD Ryzen 9 3950X, `linux/amd64`, 32 logical CPUs, Go `1.26.4`.

| Profile | Samples / fixture | Raw result | Assessment |
|---|---|---|---|
| Editor key-to-frame | 100 warm local interactions | p50 `175.050µs`, p95 `223.080µs`, max `266.181µs` | Pass, p95 ≤50 ms |
| Cancellation dispatch | 100 busy Ctrl+C dispatches | p50 `30ns`, p95 `50ns`, max `110ns` | Pass for dispatch only; descendant cleanup is separate |
| 256 KiB paste | One warm end-marker-to-frame sample | `93.070487ms` | Pass, ≤200 ms |
| 1,000 vs 10,000 one-character Markdown deltas | One allocation sample each | `107,752` / `887,288` bytes; ratio `8.24×` | Pass, ≤15× allocation growth; component stream path, not a runtime frame-count measurement |
| Picker search/render, 10,000 entries | 100 edits | p50 `2.509537ms`, p95 `3.295760ms`, max `3.421518ms` | Pass, p95 ≤50 ms |
| 10,000-session metadata activation | One synthetic workspace | `663.780031ms` to a usable 20-item first page | Pass for measured activation path |
| 512 MiB compacted session | 15,328 historical tool records; 15,331 total entries | activation `2,280ms`, peak RSS `42,544 KiB` | Pass existing 3 s / 160 MiB budgets |
| 1 MiB prose stream | One benchmark iteration | `477,780,897ns/op`, `2.22 MB/s`, `66,882,488 B/op`, `707,161 allocs/op` | Recorded; allocations are cumulative churn, not retained response size. Active source remains bounded by tests. |
| Long fenced code | 8,192 completed lines plus an over-limit single-line case | active source ≤64 KiB; completed code lines commit incrementally; output matches replay | Pass component bounds and shared formatting |
| Large writes | Existing short-write and 64 KiB batching tests | no write unit exceeds 64 KiB; suffix bytes preserved | Pass |
| Scheduled frame composition | One VT component frame | stable append + active tail + status use one `Writer.Emit` frame; runtime batches timer-flush and spinner updates | Pass |
| Ten-second idle | One settled REPL session | zero new frames, writes, or bytes over 10 seconds | Pass for the measured idle path |
| Idle output | Cached-footer redraw test | unchanged redraw emits zero additional bytes | Pass |
| 10,000 tiny output deltas | Actual-app PTY plus paced in-process REPL profile | PTY `1.095512757s`; REPL `1.145584044s`, 12 timer frames, 6 event frames, 26 writer frames, 24 writes, `20,736` bytes, max write `11,284` bytes | Pass: timer rate <30/s, categories counted separately, write units <64 KiB; PTY confirms ordered persistence and footer |

Profile fixture hashes:

```text
internal/app/app_test.go              3f2f4def8bbbe35ef38055d4657ec736010e7fd495ba2540b75ddee6d3309f35
internal/app/review_test.go            2e4c30ee82f217d84427d8bbf7fcaddf3c85a895689ad9a589c06afdfe0e9e9d
internal/app/idle_profile_test.go       f0251fc7cbbed3339d16afd3e3cd2f66dbafd167f7c2be512399e82025117985
internal/terminal/review_test.go        59c4ae810a9156b7acdcbb367a67be2bfd04e544df9a43939f92d99e32b4c6b3
internal/app/picker_perf_test.go       64e1e8bb98fe076a623bca933be25c192648405841748b0485686b6327d553f8
internal/terminal/markdown_stream_test.go f045bef2f2a763154948a278ae6d0143894b0ad7549e7f57cdc18bbb3f886bbf
internal/app/scale_test.go             9cbb18c8593ca28c0997040256ccee1911145feb719ce96f33773bb4c47362af
```

Re-run from the repository root:

```sh
go test ./internal/app -run '^TestTenThousandPickerQueriesStayResponsive$' -count=1 -v
go test ./internal/app -run '^TestActualAppPTYWorkflow$' -count=1 -v
go test ./internal/app -run '^TestRuntimeFrameMetricsForPacedText$' -count=1 -v
go test -tags acceptance ./internal/app -run '^TestTenIdleSecondsProduceNoScheduledFrames$' -count=1 -v
go test ./internal/app -tags acceptance -run 'TestScale10000MetadataFirstPage|TestScale512MiBActivation' -count=1 -v
go test ./internal/terminal -run 'Test(EditorKeyToFrameLatencyP95|CancellationDispatchP95|256KiBPasteUpdatesWithinBudget|MarkdownStreamDeltaAllocationGrowthBounded|ScheduledFrameBatchCombinesAppendAndStatus)$' -count=1 -v
go test ./internal/terminal -run '^$' -bench '^BenchmarkMarkdownStream1MiB$' -benchtime=1x -benchmem
```

## Build and platform evidence

- Local `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build ./...` passed.
- Package builds and test-binary compilation passed for `darwin/amd64`, `darwin/arm64`, and `windows/amd64`; `GOOS=windows go vet ./internal/app` also passed. These are compile/static checks only; no foreign test binary was executed.
- A Windows-only ConPTY test now launches the actual interactive app test process, types a draft, clears it, and exits. It cross-compiles but has not run on a native Windows host.
- `vci wait-ready` could not run because `/home/hypernewbie/.vci/config.toml` is absent. The Vci client was not configured because no existing coordinator destination was supplied.
- Native Windows ConPTY execution, macOS runtime tests, SSH/tmux checks, remote CI, and all human terminal checks remain blocked. A successful cross-build is not runtime evidence.

### Final executable provenance

Built from repository tree revision `51a56e3ca822c529a3e06f59982391eb37701720` with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /tmp/psi-plan4-final-51a56e3 ./cmd/psi` on Linux amd64 using Go 1.26.4. Production Go sources last changed at `9d64580`; subsequent changes are test/documentation only. The final-tree rebuild was byte-identical.

```text
SHA-256  ab811a7a72c08de11b7aea3fdff847717cfd3253256e8a3f761d25863acfd870
```

## Gate disposition

| Gate | Status | Reason |
|---|---|---|
| G4-01 Pi subset / no unrelated UI | Partial | Source mapping and omission list exist; pinned Pi side-by-side human review was not run |
| G4-02 editor/footer/scrollback | Automated pass; human partial | VT and PTY evidence passes; human flicker/selection check unavailable |
| G4-03 messages/Markdown/tools/restart | Automated pass | Shared formatter and synthetic replay/settlement tests pass |
| G4-04 completion and pickers | Automated pass | Search, keyboard precedence, persistence, and lifecycle tests pass |
| G4-05 startup/cancellation/lifecycle | Linux automated pass; platform partial | Unix PTY lifecycle passes; macOS/Windows execution remains unverified |
| G4-06 supported platforms/delivery | Blocked | Final Linux binary provenance is recorded. Remote CI, Windows console smoke, and human checks remain outstanding |
