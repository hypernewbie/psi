# PLAN4 UI acceptance ledger

## Status

The reduced Pi regular-mode UI is implemented and the local automated suite passes. A source-parity audit is continuing; the picker and resize corrections below address gaps found after the earlier acceptance passes. Timer frames, event-boundary frames, writer frames, and maximum write size are now measured in the REPL profile; ten settled idle seconds produce no output. The release gates are still **partial**: remote CI, native macOS runtime, broader Windows Terminal interactions, and human terminal review remain outstanding. Native Windows test-package suites and the full scripted ConPTY resize-operation matrix pass.

Reference source: Pi `d5b3de22d0e24a3e9c38a9410d3329b099717283` in the ignored `temp/pi-mono` checkout. Latest evidence revision: `8443db9` (`test(terminal): fixture Pi session-name footer row`), following trimmed-name coverage in `c3bde8c`, null-name coverage in `69579ff`, and the Pi session-name semantics correction in `3219438`, the session-name control test in `78e7cb8`, the active-session-name optimization in `b614826`, and stored session-name footer support in `888189e`, the Pi provider-visibility correction in `7e2c1d0`, the thinking-label correction in `f87851e`, and the cancellation test in `c9002e3`, plus the picker and completion fixtures in `6257946`, the tool-row margin change in `9fd37a6`, the one-row picker hint change in `f59bf34`, the active tool-row change in `fdc7c3d`, the picker-region change in `1608df7`, and the Pi picker-parity work in `23e147e`, `22f45e6`, `31776dc`, `cb79a93`, `ebe6337`, and `4a1bb10`; the earlier quantitative UI profiles were collected at `51a56e3`, with the picker remeasurement and resize audit recorded below. The reference was source-reviewed; no pinned Pi comparison session was run.

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
| A4-04–07 | Tool-state privacy/deduplication, shared live/replay Markdown, chunk-boundary, partial-fence, session replay, and settlement tests. The active tool row sits above the editor with the message margin, the settled line replaces it once, and the replayed tool row uses the same margin. | Automated pass |
| A4-08–13 | Inline completion/pickers, 10,000-entry search, model/thinking persistence, startup resume, trust default-deny, pending-input preservation, and picker lifecycle tests. The picker replaces the editor region with its own border and `> ` query row; navigation and filtering do not append a result page to native scrollback. Pi-derived picker and completion fixtures pin the row shape. The footer shows thinking only for reasoning-capable models and spells the disabled state `thinking off`. | Automated pass on Linux PTY; Windows ConPTY opens/cancels and navigates the model picker at all six sizes |
| A4-14–17 | Synthetic compaction phase, cancellation settlement, busy paste, storage-failure, and durable/interrupted/unsaved outcome tests. Escape during a running tool keeps `Cancelling…` visible until settlement and leaves the next prompt usable. | Automated pass |
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
- **R4-04/P4-007:** Pi's footer names a thinking level only when the selected model supports reasoning; its disabled label is `thinking off`. psi no longer shows the misleading `• off` for models without reasoning support. Pi also shows the provider only when more than one provider has models and the complete label fits with its two-cell gap. psi derives that count from local model metadata and omits the optional provider when it would clip usage or model details. The first footer row also includes an imported `session_info` name when present. Following Pi's `getSessionName`, `Store.SessionName` scans indexed entries in reverse file order, trims the latest name, and treats an empty/null name as a clear; it does not load message bodies. The footer cache key still includes the leaf ID for branch-dependent footer state. `TestFooterThinkingLabelFollowsPi`, `TestFooterProviderCountUsesAvailableCatalogs`, `TestFooterSnapshotShowsLatestSessionNameAndTracksLeaf`, and the actual picker PTY cover these rules; terminal tests cover single-provider and narrow-width fallback and sanitize session-name control bytes.
- **P4-014:** Pi's `tool-execution.ts` keeps the running tool row above the editor while the border carries the working indicator, and pads it like the surrounding transcript content. psi renders the active tool row in the owned tail with the shared message margin, and the settled line uses the same margin. The settled line and the row removal share one frame, so no duplicate running row stays visible. The active row is clipped to one physical row at the emission width.

New coverage added with those changes:

- `TestActualCompactionShowsPiIndicatorAndSettles` shows Pi's compaction indicator in a real PTY, proves the provider received a summary request, and checks the committed summary.
- `TestFailedModelPersistenceKeepsPriorRuntimeAndDraft` proves a refused store keeps the prior provider, configuration, session file, footer, draft, cursor, and recall.
- `TestActualCancelInsidePartialFenceKeepsTextAndNextPrompt` cancels a real run inside an open code fence and checks that the partial text stays visible, the interrupted outcome is stated, no style escape leaks, and the next prompt stays usable.
- `TestActualCtrlDInPickerDoesNotExitTheSession` proves Ctrl+D stays with the focused picker instead of ending the session.
- `TestActualToolRowIsTransientAndSettlesOnce` shows the running tool row above the editor during a real tool call and proves the settled line replaces it without a duplicate running row.
- `TestActiveToolRowKeepsMarginAndOnePhysicalRow` proves the active tool row keeps the message margin and stays on one physical row.
- `TestReplayedToolRowKeepsTheLiveMargin` proves the replayed tool row uses the same margin as the live settled row.
- `TestPickerHelpTextFitsNarrowWidths` and `TestChoicePickerHintFitsNarrowWidths` prove the picker key hints stay on one row instead of clipping a word.
- `TestActualCancelDuringToolKeepsCancellationVisibleAndSettles` proves Escape cancels a running tool, keeps `Cancelling…` visible until settlement, settles the call as `cancelled`, and leaves the next prompt usable.
- `TestPlan4PiPickerFixture` and `TestPlan4PiCompletionFixture` compare the picker and completion rows with Pi-derived expected fixtures.
- `TestFooterThinkingLabelFollowsPi`, `TestFooterProviderCountUsesAvailableCatalogs`, `TestFooterSnapshotShowsLatestSessionNameAndTracksLeaf`, `TestSessionNameUsesLatestEntryRegardlessOfBranch`, `TestSessionNameHonorsLatestClear`, `TestFooterSessionNameSanitizesTerminalControls`, `TestPlan4PiEditorAndFooterFixture`, `TestFooterOmitsProviderWhenOnlyOneProviderIsAvailable`, `TestFooterOmitsProviderWhenPiWouldNeedMoreRoom`, and the actual picker PTY pin Pi's footer behavior.
- `TestActualSessionPickerSelectsAndCancelsInWorkspace` selects A → B → A across workspace sessions and restores the submitted draft when the picker is cancelled.
- `TestBusyRunIgnoresOrdinaryKeysWithoutQueueingThem` proves ordinary keys are ignored during an active run.
- `TestStartupHeaderUsesPiCompactHints` checks Pi's compact startup hints.
- `TestCompactionActivityFollowsPiPhases`, `TestPickerNoMatchTreatmentAndPointerFollowPiComponents`, `TestEditorBorderFollowsPiLayout`, and `TestIndexedFallbackMatchesPiPaletteSlots` cover the transcribed wording, layout, and palette slots.

### Platform and delivery evidence at `3219438`

- Native Windows: all nine package test binaries passed on Windows 11 `10.0.26200.8875` through native PowerShell with Git for Windows on `PATH`. `TestActualWindowsConPTYInteraction` passed three consecutive runs with its six-size matrix after the footer/session-name changes.
- Native Windows fixture update: after adding the Pi-derived session-name footer row, a fresh `internal/terminal` Windows test binary passed with the updated fixture at `8443db9`.
- Cross-builds: `GOOS=windows go vet ./...`, `GOOS=windows go build ./...`, the Darwin amd64/arm64 package builds, and Darwin app/terminal test-binary compilation passed. macOS binaries were not executed.
- CI: the workflow now runs the race suite and the acceptance-tagged idle, metadata, and compacted-session profiles on Linux and macOS. The Windows race step stays out because that runner has no cgo. The workflow itself was not executed by a remote runner.



On the same Linux/amd64 host, 100 edits over 10,000 picker entries measured p50 `2.895408ms`, p95 `3.927489ms`, max `4.799042ms`. All remained below the 50 ms p95 budget. Audit fixture hashes:

```text
internal/app/picker_perf_test.go       3b20091169b79f7ef9eefe220ee8adf7ea5fd0e2b4e640fa8380e910b3a1ae1b
internal/app/selectors_test.go         9bdcc970a77e0e0558db1a9d107db85bee0680c6c731fc1a0a7848f06c478a7e
internal/app/conpty_windows_test.go    071b677bf05e11a8ae80fb4900cf14123713eed1351723a3668d6af55763e95a
internal/terminal/maintained_test.go    cae637bec61339370782861eb3a7cda73f0fbb345541fbeb3028fc8a4e55e912
internal/terminal/picker_region_test.go fbb25a8fd18b5fa5945158932737c7282aeb44e451f4d6743641bc742c69669e
internal/terminal/presentation.go      af7fa260e922a1beb7f0ac3f8361aa805bee232436bd4d498f3a9a435112ec7e
internal/terminal/console.go            dc760be5f20606fdbc4881039b2dbdbf7fbd7ddc95e16ad723b216bec436d573
internal/terminal/picker.go             1bb311edc41922c83fe74bc7b8cde14ac3c56bd689bffbf492e03053953b3edb
internal/terminal/status.go             7a33957c8f92929214ac1fbd3ac0133e22944ad1186c40591c68fe7e76d7bea7
internal/terminal/markdown.go           447ed044d4f91f404d5d60d0b75566ea3f17a86e923ef43149276fca75a5ebb3
internal/app/selectors.go              8e5a45ea167c8869d17ba733499034cf9e7f9f296e9ce7ce4eb58135c67493d9
internal/app/app.go                    4efb013e6f33737196e560e218effd75719d59f457be3cae264754d0b3ca939b
internal/app/app_test.go               74d0302d4ce3e513cf4bb876fe0f03348f6b952fb2e72359819ccfd7d600f92f
internal/app/model_picker_test.go       eab63ee957ca20281b30d1b4715824616da6b47185c7e5f73f89dbee9f6394d6
internal/app/helpers.go                fd29926f236318bb67416e181644ff840b95a6c93f69581f1c8c76a836a63293
internal/app/replay.go                 3c03198aacd46f1f9eaf39b25af088d3984302e4b18d90e195274c31aa063694
internal/app/review_test.go             68b7e10c7c1b786bf3e2e72b762a3cb172430ab410252696d9ffc3e6d59c32a9
internal/app/resume_test.go             b6527a5add4652cdde431d6aec6e40c48135ef813f78aaf3704eed910e8c8e93
internal/app/selectors_fixture_test.go  314331785099fda3e6c783490c168386d618fefcc0d3a4ed5319a00756cb707a
internal/app/testdata/plan4-pi-pickers.json 43760215dd73da24a421852a486e80e16e2a560f7dceaff5e1335ebb3b93ba11
internal/session/store.go               eb0ce40ba792f4c40c0e7555822db46568983344532452493cf8eaa2b812e0d1
internal/session/session_name_test.go   df28e9dd7d684ca8ee7b8aa209b56ec9ec68c54f7591d9b30615f771f88e8c56
internal/terminal/presentation_fixtures_test.go d488a523f1d203fdcbdb16273f11207e4a1a250c6d56127689e1be045f4772fe
internal/terminal/testdata/plan4-pi-completion.json e733af5ede9d711d32adbe94859909d8f1ffd14e1a84ccee4dade300d7153e28
internal/terminal/stream_composition_test.go 40018fb16be221021dd71920d7892391e621022c63120c3776465ee30b5bc6a3
internal/terminal/presentation_test.go  20a5954715e2f7fae900b1e0c865ec2ada2854c5c79e4fea09058113d3842b7f
internal/terminal/presentation_matrix_test.go 0cf1da306aebd6fdf6cc017a56df192fd3075bbfa7fe76cc7deaa8c89038816a
internal/terminal/testdata/plan4-pi-components.json 80a38704a9beba4da3990a1d03cc1fb427d50679498f693eec85cf2119e13435
internal/terminal/status_test.go         3fc2811e5ac75d6365dc53cb9926a0e72c26ec5dc6f04ccaa99c4d88d3b364d8
```

## Resource profiles

Measurements below were collected at revision `51a56e3` on `Linux Minerva 6.6.87.2-microsoft-standard-WSL2`, AMD Ryzen 9 3950X, `linux/amd64`, 32 logical CPUs, Go `1.26.4`. Revalidation after the Pi session-name semantics correction at `47351f1` passed: ten idle seconds produced no scheduled frames; 10,000-session metadata reached a usable first page in `691.733406ms`; the 512 MiB activation completed in `2,467ms` at `44,212 KiB` peak RSS.

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

Built from repository tree revision `8443db9` with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -o psi ./cmd/psi` on Linux amd64 using Go 1.26.4. Component and app tests verify the selected session name, active-leaf cache key, model/thinking labels, and optional-provider width fallback. The documentation-only ledger update follows this build.

```text
SHA-256  4ad6dffd0307631f05715e68f5efc47eabc2c1abb05c9c10df941a7eb27b4148
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
