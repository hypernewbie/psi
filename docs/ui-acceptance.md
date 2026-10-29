# PLAN4 UI acceptance ledger

## Status

The reduced Pi regular-mode UI is implemented and the local automated suite passes. A source-parity audit is continuing; the picker and resize corrections below address gaps found after the earlier acceptance passes. Timer frames, event-boundary frames, writer frames, and maximum write size are now measured in the REPL profile; ten settled idle seconds produce no output. The release gates are still **partial**: remote CI, native macOS runtime, broader Windows Terminal interactions, and human terminal review remain outstanding. Native Windows test-package suites and the full scripted ConPTY resize-operation matrix pass.

Reference source: Pi `d5b3de22d0e24a3e9c38a9410d3329b099717283` in the ignored `temp/pi-mono` checkout. Latest evidence revision: `1b22ee5` (`test(terminal): draw the frame that drops the settled tool row`), following the active tool-row change in `fdc7c3d`, the picker-region change in `1608df7`, and the Pi picker-parity work in `23e147e`, `22f45e6`, `31776dc`, `cb79a93`, `ebe6337`, and `4a1bb10`; the earlier quantitative UI profiles were collected at `51a56e3`, with the picker remeasurement and resize audit recorded below. The reference was source-reviewed; no pinned Pi comparison session was run.

## Evidence by level

| Level | Evidence | Result |
|---|---|---|
| Component | `go test ./internal/terminal ./internal/app`; Markdown, styled-row, editor, completion, picker, notice, and settlement fixtures | Pass |
| VT emulator | `testutil.VTModel` screen/cursor/scrollback checks across the 120×40, 80×24, 60×16, 40×12, and 20×6 geometry matrix; 1×1 and 2×2 safety cases | Pass for automated cell and cursor assertions |
| Actual app | Synthetic-provider PTYs and subprocess tests, including offline model/thinking selection, resume, startup consent, picker cancellation, suspend/resume, shutdown, and privacy canaries; Windows ConPTY exercises typing, local SSE streaming, a running Git Bash tool, and model-picker navigation at all six sizes | Automated pass on Linux/WSL2 and native Windows; human checks remain blocked |
| Human terminal | Native selection/copy, perceived redraw flicker, light/dark theme readability, and Pi side-by-side review | Blocked; no human terminal session was available |

No live provider request was made. Tests use synthetic credentials, temporary workspaces, and local protocol servers.

## Journey ledger

| Cases | Automated evidence | Human/platform status |
|---|---|---|
| A4-01–02 | Inline startup/editor, grapheme editing, literal paste, recall, cursor, overflow, and responsive geometry tests; actual-app PTY coverage | Human readability and copy check blocked |
| A4-03 | Slow-stream and actual-app PTY tests preserve the indicator, footer, editor, and partial text. A loopback provider sends 10,000 one-character deltas in 30 paced batches; order and persistence are checked. The REPL profile separately counts timer and event-boundary frames, writer frames/writes, and maximum write size. | Automated pass; human perception/flicker check remains blocked |
| A4-04–07 | Tool-state privacy/deduplication, shared live/replay Markdown, chunk-boundary, partial-fence, session replay, and settlement tests | Automated pass |
| A4-08–13 | Inline completion/pickers, 10,000-entry search, model/thinking persistence, startup resume, trust default-deny, pending-input preservation, and picker lifecycle tests. The picker replaces the editor region with its own border and `> ` query row; navigation and filtering do not append a result page to native scrollback. | Automated pass on Linux PTY; Windows ConPTY opens/cancels and navigates the model picker at all six sizes |
| A4-14–17 | Synthetic compaction phase, cancellation settlement, busy paste, storage-failure, and durable/interrupted/unsaved outcome tests | Automated pass |
| A4-18 | Maintained VT keeps an active draft and committed sentinel through all six sizes. Native Windows ConPTY exercises the actual app at those sizes while typing, streaming from a local SSE provider, running a five-second Git Bash tool, and navigating the model picker. | Automated pass for the resize matrix; human flicker/readability checks remain blocked |
| A4-19 | `NO_COLOR`, semantic token, and no-generated-SGR fixtures | Automated pass; light/dark human checks blocked |
| A4-20 | Unix PTY suspend/resume, EOF, shutdown, and termios restoration; comparison masks only macOS PENDIN. Native Windows ConPTY draft/clear/quit smoke also passes. | Linux lifecycle and Windows ConPTY smoke pass; macOS runtime and human shell restoration remain blocked |
| A4-21 | Malicious control, path, provider, Markdown, tool-body, and secret canaries across display tests | Automated pass for covered paths |
| A4-22 | Native terminal behavior is preserved by the inline writer; no application mouse capture or clipboard path exists | **Blocked:** prose/code selection and copy were not checked by a human |

## Source-parity audit at `543eaa3`

The audit found implementation gaps, not just missing external evidence:

- **P4-023:** Removed the unplanned fuzzy search. Queries use case-insensitive substrings and source ordering. A matching selection keeps its identity; otherwise the first match is selected, not the active item elsewhere in the results.
- **P4-023 editor replacement:** The picker now replaces the editor region, as Pi's `showSelector` does. `interactive-mode.ts` clears the editor container, installs the selector component, and moves focus to the selector; the selector owns its top `DynamicBorder`, its `> ` search input, and its list. psi previously rendered the picker rows below the editor's bottom border, in the same slot as the slash-command completion list. The picker region now owns its top border, the `> ` query row, the bounded list, and the closing border, and the editor block is not rendered underneath it. The committed transcript above and the footer below are unchanged. Startup trust and workspace confirmation use the same region without a query row.
- **P4-024:** Matched Pi's `model-selector.ts` rows: `→` pointer, separate `✓` current marker, model ID before `[provider]`, and selected-only credential/API/context details. No full metadata paragraph repeats on every row.
- **P4-025:** Matched `session-selector.ts` primary name/prompt and compact age treatment. An active session's own lock no longer hides its preview behind an unavailable warning.
- **P4-027:** Lists use up to ten entries and the actual available height. Page keys move by the visible count. Tiny views retain a real selected row instead of a duplicate selected-item header.
- **P4-009/P4-031:** Native ConPTY exposed stale differential row assumptions when the old picker exceeded the resized viewport. Resize invalidates only the owned tail and reallocates its rows with CRLF. There is no screen clear or committed transcript replay. Windows now posts changed-size events to the application owner, corresponding to Pi's `ProcessTerminal.start` resize callback. Its 100 ms size watcher produces no events for an unchanged size and stops with console input ownership.

Validation: `./scripts/check.sh`, Windows vet/build, and Darwin amd64/arm64 builds plus app/terminal test-binary compilation passed. The full native Windows app and terminal packages passed. Five repeated ConPTY journeys checked all six sizes, including draft and picker redraws without a new key. Tests wait for the selected row **and** editor cursor, not a partial output chunk. Three synthetic PTY journeys also passed against the rebuilt `./psi` executable: streaming/tools, model/thinking/restart, and resume/recall.

### Picker-region and Pi-parity evidence at `1608df7` through `9b81b8e`

- Component: `internal/terminal/picker_region_test.go` asserts that the picker owns exactly one border pair, renders the `> ` query row, places the cursor on that row, keeps the query scrolling horizontally, stays inside the terminal height at 120×40, 80×24, 60×16, 40×12, 20×6, 20×4, and 20×3, emits no SGR under `NO_COLOR`, and gives the choice picker no query row. `internal/app/selectors_test.go` re-runs the plan-size geometry matrix against the picker composition.
- Actual app: `TestActualPickerReplacesTheEditorWithoutFloodingScrollback` opens the model picker in a real PTY, asserts one border pair and a query row, filters and navigates, and proves that native scrollback gained no result rows. Cancelling restores the submitted `/model` draft.
- Delivered binary: the rebuilt `./psi` executable was driven in a PTY. The startup header and editor border appear, `/model` opens a picker with a query row and a selection pointer, filtering appends no second result page, and `Ctrl+C` restores the draft.
- Native Windows: the `internal/app` and `internal/terminal` package binaries passed on Windows 11 `10.0.26200.8875` through WSL2 interop after the picker-region change, including `TestActualWindowsConPTYInteraction` with the six-size picker resize matrix.

Additional Pi-source transcriptions after the picker-region change:

- **P4-024/P4-026:** Pi's no-match treatment is component-specific: the model selector says `No matching models`, the session list says `No sessions found`, and the thinking selector inherits the shared `SelectList` wording. Pi's thinking selector uses the `SelectList` pointer, so psi's thinking rows use `→ ` and only the session list keeps `› `. The completion menu is a `SelectList` too, so its selected row uses `→ `.
- **P4-024:** Pi's model selector adds `  (position/total)` when the bounded window cannot show every filtered item. psi shows the same indicator and keeps the optional selected detail until the row budget is tight.
- **P4-030:** Pi's `trust-selector.ts` shows a title, the options with a `→ ` pointer and a `✓ ` current marker, the visible folder, and its navigation hint. psi's project-trust and workspace-confirmation regions use that shape and keep psi's reduced allow/deny set with deny selected by default.
- **P4-012:** Pi's `CompactionStatusIndicator` distinguishes a manual compaction from an automatic one and names the overflow reason. psi's compaction activity now uses `Compacting context…`, `Auto-compacting…`, and `Context overflow detected, Auto-compacting…` with the same cancellation hint.
- **P4-006:** Pi's custom editor puts the working status at the left of the top border and centers the overflow count with `createScrollBorder`. psi's editor border now uses that layout.
- **P4-014:** Pi's `tool-execution.ts` keeps the running tool row above the editor while the border carries the working indicator. psi renders the active tool row in the owned tail. The settled line and the row removal share one frame, so no duplicate running row stays visible.

New coverage added with those changes:

- `TestActualCompactionShowsPiIndicatorAndSettles` shows Pi's compaction indicator in a real PTY, proves the provider received a summary request, and checks the committed summary.
- `TestFailedModelPersistenceKeepsPriorRuntimeAndDraft` proves a refused store keeps the prior provider, configuration, session file, footer, draft, cursor, and recall.
- `TestActualCancelInsidePartialFenceKeepsTextAndNextPrompt` cancels a real run inside an open code fence and checks that the partial text stays visible, the interrupted outcome is stated, no style escape leaks, and the next prompt stays usable.
- `TestActualCtrlDInPickerDoesNotExitTheSession` proves Ctrl+D stays with the focused picker instead of ending the session.
- `TestActualToolRowIsTransientAndSettlesOnce` shows the running tool row above the editor during a real tool call and proves the settled line replaces it without a duplicate running row.
- `TestActualSessionPickerSelectsAndCancelsInWorkspace` selects A → B → A across workspace sessions and restores the submitted draft when the picker is cancelled.
- `TestBusyRunIgnoresOrdinaryKeysWithoutQueueingThem` proves ordinary keys are ignored during an active run.
- `TestStartupHeaderUsesPiCompactHints` checks Pi's compact startup hints.
- `TestCompactionActivityFollowsPiPhases`, `TestPickerNoMatchTreatmentAndPointerFollowPiComponents`, `TestEditorBorderFollowsPiLayout`, and `TestIndexedFallbackMatchesPiPaletteSlots` cover the transcribed wording, layout, and palette slots.

### Platform and delivery evidence at `1b22ee5`

- Native Windows: the `internal/app` and `internal/terminal` package binaries passed again on Windows 11 `10.0.26200.8875` through WSL2 interop after the picker-parity, key-ownership, and tool-row work. `TestActualWindowsConPTYInteraction` passed two consecutive runs over the six-size matrix.
- Cross-builds: `GOOS=windows go vet ./...`, `GOOS=windows go build ./...`, the Darwin amd64/arm64 package builds, and Darwin app/terminal test-binary compilation passed. macOS binaries were not executed.
- CI: the workflow now runs the race suite and the acceptance-tagged idle, metadata, and compacted-session profiles on Linux and macOS. The Windows race step stays out because that runner has no cgo. The workflow itself was not executed by a remote runner.



On the same Linux/amd64 host, 100 edits over 10,000 picker entries measured p50 `2.801081ms`, p95 `3.751883ms`, max `3.89351ms`. All remained below the 50 ms p95 budget. Audit fixture hashes:

```text
internal/app/picker_perf_test.go       3b20091169b79f7ef9eefe220ee8adf7ea5fd0e2b4e640fa8380e910b3a1ae1b
internal/app/selectors_test.go         425b9a47fd5a727f31f749cc13a5823fe8a56ebe106abbf6bc26643cba60ef5f
internal/app/conpty_windows_test.go    071b677bf05e11a8ae80fb4900cf14123713eed1351723a3668d6af55763e95a
internal/terminal/maintained_test.go    cae637bec61339370782861eb3a7cda73f0fbb345541fbeb3028fc8a4e55e912
internal/terminal/picker_region_test.go fbb25a8fd18b5fa5945158932737c7282aeb44e451f4d6743641bc742c69669e
internal/terminal/presentation.go      9d56956a66f61c29bd4d7ac5c09d5b31d1a202e127b14205746cc6a022b6372d
internal/terminal/console.go            ac2228bb185e31dfabc6804a8d499159f62c1e7339c6e54129b997e24f8b85c9
internal/terminal/picker.go             08a2b90d7615533e572e673a5aa51b2ef6f07975300a0d87bfc2d6dbe8a67f23
internal/app/selectors.go              346e55e8724e815a1f043078d6e0e0f45987fa7654a28bc8815f68d7791546db
internal/app/app.go                    8928e6458d972592251ac23492441870e84720c45971d0a20091e997d6cbee1b
internal/app/helpers.go                dbc8d1be8e91b08ff4a3270d7b109ac8d189b2878d75ca104356e3c7d39b82de
```

## Resource profiles

Measurements below were collected at revision `51a56e3` on `Linux Minerva 6.6.87.2-microsoft-standard-WSL2`, AMD Ryzen 9 3950X, `linux/amd64`, 32 logical CPUs, Go `1.26.4`.

| Profile | Samples / fixture | Raw result | Assessment |
|---|---|---|---|
| Editor key-to-frame | 100 warm local interactions | p50 `175.050µs`, p95 `223.080µs`, max `266.181µs` | Pass, p95 ≤50 ms |
| Cancellation dispatch | 100 busy Ctrl+C dispatches | p50 `30ns`, p95 `50ns`, max `110ns` | Pass for dispatch only; descendant cleanup is separate |
| 256 KiB paste | One warm end-marker-to-frame sample | `93.070487ms` | Pass, ≤200 ms |
| 1,000 vs 10,000 one-character Markdown deltas | One allocation sample each | `107,752` / `887,288` bytes; ratio `8.24×` | Pass, ≤15× allocation growth; component stream path, not a runtime frame-count measurement |
| Picker search/render, 10,000 entries | 100 edits | p50 `2.509537ms`, p95 `3.295760ms`, max `3.421518ms` | Pass, p95 ≤50 ms |
| 10,000-session metadata activation | One synthetic workspace | `673.730235ms` to a usable 20-item first page | Pass for measured activation path |
| 512 MiB compacted session | 15,328 historical tool records; 15,331 total entries | activation `2,408ms`, peak RSS `43,868 KiB` | Pass existing 3 s / 160 MiB budgets |
| 1 MiB prose stream | One benchmark iteration | `477,780,897ns/op`, `2.22 MB/s`, `66,882,488 B/op`, `707,161 allocs/op` | Recorded; allocations are cumulative churn, not retained response size. Active source remains bounded by tests. |
| Long fenced code | 8,192 completed lines plus an over-limit single-line case | active source ≤64 KiB; completed code lines commit incrementally; output matches replay | Pass component bounds and shared formatting |
| Large writes | Existing short-write and 64 KiB batching tests | no write unit exceeds 64 KiB; suffix bytes preserved | Pass |
| Scheduled frame composition | One VT component frame | stable append + active tail + status use one `Writer.Emit` frame; runtime batches timer-flush and spinner updates | Pass |
| Six-size resize | Maintained VT plus actual Windows ConPTY app with synthetic SSE and Git Bash tool | component retains draft/sentinel; actual app exercises typing, streaming, running-tool redraws, and picker selection at each size | Pass for all four scripted operations; human flicker review remains blocked |
| Ten-second idle | One settled REPL session | zero new frames, writes, or bytes over 10 seconds | Pass for the measured idle path |
| Idle output | Cached-footer redraw test | unchanged redraw emits zero additional bytes | Pass |
| 10,000 tiny output deltas | Actual-app PTY plus paced in-process REPL profile | PTY `1.095512757s`; REPL `1.143269396s`, 12 timer frames, 6 event frames, 26 writer frames, 24 writes, `20,761` bytes, max write `11,284` bytes | Pass: timer rate <30/s, categories counted separately, write units <64 KiB; PTY confirms ordered persistence and footer |

Historical profile fixture hashes (the newer picker/resize fixtures are recorded above):

```text
internal/app/conpty_windows_test.go    ecdc7b90cdace9da295bf450bfe7d04999c373a59d4c4e38614849b270bf306c
internal/app/conpty_provider_windows_test.go f557d3b375d586583499da9365d0404feeb0083dabd911934cc4bcb6ec5a1844
internal/app/app_test.go              2358b6fe0abb0686f0ab37e38f0921075994adc0c8c7903068555fd0419a7001
internal/app/review_test.go            2e4c30ee82f217d84427d8bbf7fcaddf3c85a895689ad9a589c06afdfe0e9e9d
internal/app/idle_profile_test.go       f0251fc7cbbed3339d16afd3e3cd2f66dbafd167f7c2be512399e82025117985
internal/terminal/review_test.go        59c4ae810a9156b7acdcbb367a67be2bfd04e544df9a43939f92d99e32b4c6b3
internal/terminal/maintained_test.go    7cfc51ad0a4119a6170d57efa248c7f9226c3f74ce44d30ba7c97de172a8f442
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
go test ./internal/terminal -run 'Test(EditorKeyToFrameLatencyP95|CancellationDispatchP95|256KiBPasteUpdatesWithinBudget|MarkdownStreamDeltaAllocationGrowthBounded|ScheduledFrameBatchCombinesAppendAndStatus|MaintainedVTResizeKeepsActiveDraftAndCommittedHistory)$' -count=1 -v
go test ./internal/terminal -run '^$' -bench '^BenchmarkMarkdownStream1MiB$' -benchtime=1x -benchmem
```

## Build and platform evidence

- Local `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build ./...` passed.
- Package builds and test-binary compilation passed for `darwin/amd64`, `darwin/arm64`, and `windows/amd64`; `GOOS=windows go vet ./...` and `GOOS=windows go build ./...` passed. macOS binaries were not executed.
- All Windows test-package binaries passed on native Windows 11 `10.0.26200.8875` through WSL2 interop: `agent`, `app`, `config`, `provider`, `session`, `skills`, `terminal`, `testutil`, and `tools`. Git for Windows `usr/bin` was added to `PATH` for shell tests, as in `.github/workflows/ci.yml`. Unix-only PTY tests were skipped.
- The Windows ConPTY test launched the real app with a local Anthropic-protocol server. It exercised typing, a paused stream, a `sleep 5` Bash call, and model-picker selection through the six-size matrix. Five repeated journeys and the full Windows app and terminal test packages passed; draft and picker resize redraws no longer require a key. No live provider request ran; no alternate-screen or full-screen clear occurred.
- `vci wait-ready` could not run because `/home/hypernewbie/.vci/config.toml` is absent. The Vci client was not configured because no existing coordinator destination was supplied.
- Remote Linux/macOS/Windows CI, macOS runtime tests, SSH/tmux checks, Windows Terminal/Git Bash human checks, and all human visual/copy checks remain blocked. The ConPTY test covers only its scripted Windows interaction.

### Final executable provenance

Built from repository tree revision `1b22ee5` with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -o psi ./cmd/psi` on Linux amd64 using Go 1.26.4. This replaces the stale ignored root executable. The delivered binary was driven in a PTY to confirm the startup header, the picker region with its `> ` query row, scrollback-stable filtering, and draft restoration. The documentation-only ledger update follows this build.

```text
SHA-256  77442a6184ef23fda66f84d8cb7a6b86934bd2f893b50fe2e72c59f2dc5e975a
```

## Gate disposition

| Gate | Status | Reason |
|---|---|---|
| G4-01 Pi subset / no unrelated UI | Partial | Source mapping and omission list exist; pinned Pi side-by-side human review was not run |
| G4-02 editor/footer/scrollback | Automated pass; human partial | VT and PTY evidence passes; human flicker/selection check unavailable |
| G4-03 messages/Markdown/tools/restart | Automated pass | Shared formatter and synthetic replay/settlement tests pass |
| G4-04 completion and pickers | Automated pass | Search, keyboard precedence, persistence, lifecycle, and editor-replacement picker-region tests pass |
| G4-05 startup/cancellation/lifecycle | Linux and Windows automated pass; platform partial | Windows package suites and ConPTY draft/clear/quit plus the six-size interaction matrix pass; macOS execution and human terminal lifecycle remain unverified |
| G4-06 supported platforms/delivery | Blocked | Final Linux binary provenance and native Windows test evidence are recorded. Remote CI, macOS runtime, and Windows Terminal/human checks remain outstanding |

## Handoff for blocked acceptance

| Required input | Acceptance work | Record in this ledger |
|---|---|---|
| An existing VCI coordinator destination and explicit client-configuration authorization | Configure only the VCI client, then run the repository CI workflow through VCI. Do not create or administer a coordinator. | Run ID, target states, and any unavailable targets for P4-034/G4-06 |
| A native macOS host or runner | Execute the repository macOS CI jobs, including PTY and terminal-state comparisons. Cross-compilation is not runtime evidence. | Job result and any platform-specific failures for A4-20/G4-05/G4-06 |
| A human with access to a supported terminal and a pinned-Pi comparison runtime | Compare regular-mode structure; check delayed-stream flicker, light/dark and `NO_COLOR` readability, shell restoration, and native selection/copy of prose and code. No application-controlled mouse or clipboard path should appear. | Terminal/emulator/version, comparison basis, and pass/fail notes for A4-19/A4-20/A4-22 and G4-01/G4-02/G4-06 |
