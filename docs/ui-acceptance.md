# PLAN4 UI acceptance ledger

## Status

The reduced Pi regular-mode UI is implemented and the local automated suite passes. The release gates are **partial**, not complete: the strict scheduled-frame profile is not directly instrumented, remote macOS/Windows tests did not run, native Windows console input/output was not exercised, and human visual/copy checks remain unavailable.

Reference source: Pi `d5b3de22d0e24a3e9c38a9410d3329b099717283` in the ignored `temp/pi-mono` checkout. Latest measured source revision: `943abd282fe8aed02ea2ca9eef0540c10ab79be9` (`943abd2`). The reference was source-reviewed; no pinned Pi comparison session was run.

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
| A4-03 | Slow synthetic stream tests preserve the indicator, footer, editor, and partial text. Streamer tests coalesce 10,000 tiny deltas. | **Partial:** the runtime uses one 30 Hz timer, but the actual-app test does not separately count timer frames, event-boundary frames, and write batches over a controlled second |
| A4-04–07 | Tool-state privacy/deduplication, shared live/replay Markdown, chunk-boundary, partial-fence, session replay, and settlement tests | Automated pass |
| A4-08–13 | Inline completion/pickers, 10,000-entry search, model/thinking persistence, startup resume, trust default-deny, pending-input preservation, and picker lifecycle tests | Automated pass on Linux PTY; native console checks remain blocked |
| A4-14–17 | Synthetic compaction phase, cancellation settlement, busy paste, storage-failure, and durable/interrupted/unsaved outcome tests | Automated pass |
| A4-18 | Component VT geometry and resize/tail tests; actual-app lifecycle and picker resize coverage | **Partial:** not every operation was exercised at every size in a single actual-app matrix |
| A4-19 | `NO_COLOR`, semantic token, and no-generated-SGR fixtures | Automated pass; light/dark human checks blocked |
| A4-20 | Unix PTY suspend/resume, EOF, shutdown, and termios restoration; comparison masks only macOS PENDIN | Linux pass; macOS/native Windows execution and human shell restoration blocked |
| A4-21 | Malicious control, path, provider, Markdown, tool-body, and secret canaries across display tests | Automated pass for covered paths |
| A4-22 | Native terminal behavior is preserved by the inline writer; no application mouse capture or clipboard path exists | **Blocked:** prose/code selection and copy were not checked by a human |

## Resource profiles

Measurements below were collected at revision `943abd2` on `Linux Minerva 6.6.87.2-microsoft-standard-WSL2`, AMD Ryzen 9 3950X, `linux/amd64`, 32 logical CPUs, Go `1.26.4`.

| Profile | Samples / fixture | Raw result | Assessment |
|---|---|---|---|
| Editor key-to-frame | 100 warm local interactions | p50 `173.63µs`, p95 `215.011µs`, max `247.541µs` | Pass, p95 ≤50 ms |
| Cancellation dispatch | 100 busy Ctrl+C dispatches | p50 `40ns`, p95 `50ns`, max `140ns` | Pass for dispatch only; descendant cleanup is separate |
| 256 KiB paste | One warm end-marker-to-frame sample | `95.971959ms` | Pass, ≤200 ms |
| 1,000 vs 10,000 one-character Markdown deltas | One allocation sample each | `107,752` / `869,512` bytes; ratio `8.07×` | Pass, ≤15× allocation growth; component stream path, not a runtime frame-count measurement |
| Picker search/render, 10,000 entries | 100 edits | p50 `2.567858ms`, p95 `3.627072ms`, max `4.028164ms` | Pass, p95 ≤50 ms |
| 10,000-session metadata activation | One synthetic workspace | `681.364841ms` to a usable 20-item first page | Pass for measured activation path |
| 512 MiB compacted session | 15,328 historical tool records; 15,331 total entries | activation `2,357ms`, peak RSS `43,844 KiB` | Pass existing 3 s / 160 MiB budgets |
| 1 MiB prose stream | One benchmark iteration | `493,061,147ns/op`, `2.15 MB/s`, `66,765,640 B/op`, `706,474 allocs/op` | Recorded; allocations are cumulative churn, not retained response size. Active source remains bounded by tests. |
| Long fenced code | 8,192 completed lines plus an over-limit single-line case | active source ≤64 KiB; completed code lines commit incrementally; output matches replay | Pass component bounds and shared formatting |
| Large writes | Existing short-write and 64 KiB batching tests | no write unit exceeds 64 KiB; suffix bytes preserved | Pass |
| Idle output | Cached-footer redraw test | unchanged redraw emits zero additional bytes | Pass for unchanged redraw; a dedicated ten-second timer/frame counter is not present |
| 10,000 tiny output deltas | Streamer coalescing tests | ordered bytes retained; no per-delta writer call | Pass at stream-adapter level; strict scheduled frame rate remains partial |

Profile fixture hashes:

```text
internal/app/picker_perf_test.go       64e1e8bb98fe076a623bca933be25c192648405841748b0485686b6327d553f8
internal/terminal/markdown_stream_test.go f045bef2f2a763154948a278ae6d0143894b0ad7549e7f57cdc18bbb3f886bbf
internal/app/scale_test.go             9cbb18c8593ca28c0997040256ccee1911145feb719ce96f33773bb4c47362af
```

Re-run from the repository root:

```sh
go test ./internal/app -run '^TestTenThousandPickerQueriesStayResponsive$' -count=1 -v
go test ./internal/app -tags acceptance -run 'TestScale10000MetadataFirstPage|TestScale512MiBActivation' -count=1 -v
go test ./internal/terminal -run 'Test(EditorKeyToFrameLatencyP95|CancellationDispatchP95|256KiBPasteUpdatesWithinBudget|MarkdownStreamDeltaAllocationGrowthBounded)$' -count=1 -v
go test ./internal/terminal -run '^$' -bench '^BenchmarkMarkdownStream1MiB$' -benchtime=1x -benchmem
```

## Build and platform evidence

- Local `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build ./...` passed.
- Package builds and test-binary compilation passed for `darwin/amd64`, `darwin/arm64`, and `windows/amd64`; `GOOS=windows go vet ./internal/app` also passed. These are compile/static checks only; no foreign test binary was executed.
- A Windows-only ConPTY test now launches the actual interactive app test process, types a draft, clears it, and exits. It cross-compiles but has not run on a native Windows host.
- `vci wait-ready` could not run because `/home/hypernewbie/.vci/config.toml` is absent. The Vci client was not configured because no existing coordinator destination was supplied.
- Native Windows ConPTY execution, macOS runtime tests, SSH/tmux checks, remote CI, and all human terminal checks remain blocked. A successful cross-build is not runtime evidence.

### Final executable provenance

Built from source revision `2e74bd311efd8193ee9e06207bcee66f914fe4ba` with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /tmp/psi-plan4-final-2e74bd3 ./cmd/psi` on Linux amd64 using Go 1.26.4.

```text
SHA-256  a1756d8850d13bf01b872114a2ab8a2e37244936cd7394250ff51dc68d67af3e
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
